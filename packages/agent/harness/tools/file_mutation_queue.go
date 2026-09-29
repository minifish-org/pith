// Serialized file mutations for the built-in tools.
//
// This is a Go port of packages/agent/src/harness/tools/file-mutation-queue.ts
// at Pi revision f07218c4d4bbc12bef056a7058c3dd49dfe41be.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The upstream implementation keys a WeakMap by execution-environment object
// identity. Go has no weak references, so the registry is a process-global map
// keyed by the environment's pointer identity and the canonical path. Entries
// are removed once their queue drains, so the map tracks active mutations
// rather than every environment ever used.
package tools

import (
	"reflect"
	"sync"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

type mutationQueueState struct {
	mu     sync.Mutex
	queues map[string]chan struct{}
}

var (
	mutationStatesMu sync.Mutex
	mutationStates   = map[any]*mutationQueueState{}
)

func environmentKey(env harnesstypes.ExecutionEnv) any {
	value := reflect.ValueOf(env)
	if value.IsValid() && value.Kind() == reflect.Ptr {
		return value.Pointer()
	}
	return env
}

func getMutationState(env harnesstypes.ExecutionEnv) *mutationQueueState {
	key := environmentKey(env)
	mutationStatesMu.Lock()
	defer mutationStatesMu.Unlock()
	state, ok := mutationStates[key]
	if !ok {
		state = &mutationQueueState{queues: map[string]chan struct{}{}}
		mutationStates[key] = state
	}
	return state
}

func getMutationQueueKey(env harnesstypes.ExecutionEnv, path string, ctx harnesscontext.Context) (string, error) {
	absolutePath := harnesstypes.GetOrThrow(env.AbsolutePath(path, ctx))
	canonicalPath := env.CanonicalPath(absolutePath, ctx)
	if canonicalPath.OK {
		return canonicalPath.Value, nil
	}
	if canonicalPath.Error.Code == harnesstypes.FileErrorNotFound || canonicalPath.Error.Code == harnesstypes.FileErrorNotSupported {
		return absolutePath, nil
	}
	canonicalErr := canonicalPath.Error
	return "", &canonicalErr
}

// WithFileMutationQueue serializes file mutations targeting the same
// environment and canonical path. Work is enqueued in registration order and
// runs one mutation at a time; a panic in fn still releases the queue.
func WithFileMutationQueue[T any](
	env harnesstypes.ExecutionEnv,
	path string,
	fn func() (T, error),
	ctx harnesscontext.Context,
) (T, error) {
	state := getMutationState(env)

	state.mu.Lock()
	key, err := getMutationQueueKey(env, path, ctx)
	if err != nil {
		state.mu.Unlock()
		var zero T
		return zero, err
	}
	previous := state.queues[key]
	done := make(chan struct{})
	state.queues[key] = done
	state.mu.Unlock()

	if previous != nil {
		<-previous
	}
	defer func() {
		close(done)
		state.mu.Lock()
		if state.queues[key] == done {
			delete(state.queues, key)
		}
		state.mu.Unlock()
	}()

	return fn()
}
