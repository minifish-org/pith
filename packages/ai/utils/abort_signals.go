// This file is a Go port of packages/ai/src/utils/abort-signals.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Upstream combines AbortSignals; the Go equivalent combines context.Context
// values. The returned CancelFunc releases the combined context and any waiter
// goroutines, mirroring upstream `cleanup`.
package utils

import (
	"context"
	"sync"
)

// CombinedAbortSignals is the Go representation of upstream's
// `CombinedAbortSignal`: the combined cancellation context together with the
// cleanup that releases its waiter goroutines.
type CombinedAbortSignals struct {
	// Signal is the combined context cancelled by any source signal.
	Signal context.Context
	// Cleanup releases the waiter goroutines and the combined context.
	Cleanup context.CancelFunc
}

// CombineAbortSignals combines several optional cancel signals into one. A nil
// entry is ignored. When no signal is active the combined context is
// context.Background() and cleanup is a no-op. When exactly one signal is active
// it is returned with a no-op cleanup. Otherwise the first cancellation wins and
// cleanup releases the waiter.
func CombineAbortSignals(signals ...context.Context) CombinedAbortSignals {
	active := make([]context.Context, 0, len(signals))
	for _, signal := range signals {
		if signal != nil {
			active = append(active, signal)
		}
	}
	if len(active) == 0 {
		return CombinedAbortSignals{Signal: context.Background(), Cleanup: func() {}}
	}
	if len(active) == 1 {
		return CombinedAbortSignals{Signal: active[0], Cleanup: func() {}}
	}

	ctx, cancel := context.WithCancel(context.Background())
	var once sync.Once
	stop := make(chan struct{})
	var wg sync.WaitGroup

	abort := func() {
		once.Do(cancel)
	}

	for _, signal := range active {
		if signal.Err() != nil {
			abort()
			break
		}
		wg.Add(1)
		go func(signal context.Context) {
			defer wg.Done()
			select {
			case <-signal.Done():
				abort()
			case <-stop:
			}
		}(signal)
	}

	cleanup := func() {
		close(stop)
		wg.Wait()
		cancel()
	}
	return CombinedAbortSignals{Signal: ctx, Cleanup: cleanup}
}
