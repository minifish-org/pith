// Serialized file mutations for the Durable coding tools.
//
// This is a Go port of packages/durable/src/tools/file-mutation-queue.ts at Pi
// revision a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License. See the repository
// LICENSE for the full text.
//
// The upstream implementation keys a process-global Map by the file system id
// and the canonical path. Go has no async promise chain, so the tail of each
// key's chain is a channel that the next mutation waits on. Entries are removed
// once their queue drains, so the map tracks active mutations rather than every
// path ever used. This is a same-process serialization of edit/write, not a
// lock against bash or other processes.
package tools

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/minifish-org/pith/packages/durable/env"
)

var mutationQueues = struct {
	mu     sync.Mutex
	queues map[string]chan struct{}
}{queues: map[string]chan struct{}{}}

// mutationKey identifies one file within one file system namespace: the
// environment id and the canonical path.
func mutationKey(ctx context.Context, e env.ExecutionEnv, path string) (string, error) {
	absolutePath, err := e.AbsolutePath(ctx, path)
	if err != nil {
		return "", err
	}
	canonical, err := canonicalMutationPath(ctx, e, absolutePath)
	if err != nil {
		return "", err
	}
	return e.ID() + "\x00" + canonical, nil
}

// canonicalMutationPath returns the canonical path; for a file that does not
// exist yet it is the canonical parent joined with the name, so a write that
// creates a file and a later mutation of it share one key even under a
// symlinked directory.
func canonicalMutationPath(ctx context.Context, e env.ExecutionEnv, absolutePath string) (string, error) {
	canonical, err := e.CanonicalPath(ctx, absolutePath)
	if err == nil {
		return canonical, nil
	}
	var fileErr *env.FileError
	if !errors.As(err, &fileErr) {
		return "", err
	}
	if fileErr.Code == env.FileErrorNotSupported {
		return absolutePath, nil
	}
	if fileErr.Code != env.FileErrorNotFound {
		return "", err
	}
	parent, err := e.JoinPath(ctx, absolutePath, "..")
	if err != nil {
		return "", err
	}
	if parent == absolutePath || !strings.HasPrefix(absolutePath, parent) {
		return absolutePath, nil
	}
	nameStart := len(parent)
	if !strings.HasSuffix(parent, "/") && !strings.HasSuffix(parent, "\\") {
		nameStart++
	}
	if nameStart > len(absolutePath) {
		return absolutePath, nil
	}
	name := absolutePath[nameStart:]
	canonicalParent, err := canonicalMutationPath(ctx, e, parent)
	if err != nil {
		return "", err
	}
	return e.JoinPath(ctx, canonicalParent, name)
}

// WithFileMutationQueue serializes file mutations targeting the same file
// system namespace and canonical path. Work is enqueued in registration order
// and runs one mutation at a time; a panic in fn still releases the queue.
func WithFileMutationQueue[T any](
	ctx context.Context,
	e env.ExecutionEnv,
	path string,
	fn func() (T, error),
) (T, error) {
	var zero T
	key, err := mutationKey(ctx, e, path)
	if err != nil {
		return zero, err
	}

	mutationQueues.mu.Lock()
	previous := mutationQueues.queues[key]
	done := make(chan struct{})
	mutationQueues.queues[key] = done
	mutationQueues.mu.Unlock()

	if previous != nil {
		<-previous
	}
	defer func() {
		close(done)
		mutationQueues.mu.Lock()
		if mutationQueues.queues[key] == done {
			delete(mutationQueues.queues, key)
		}
		mutationQueues.mu.Unlock()
	}()

	return fn()
}
