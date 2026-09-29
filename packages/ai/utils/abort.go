// This file is a Go port of packages/ai/src/utils/abort.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package utils

import (
	"context"
)

// abortReason returns the context's cancellation error, defaulting to
// context.Canceled. Upstream exposes `signal.reason`, or an AbortError with the
// message "The operation was aborted" when no reason was supplied.
func abortReason(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return context.Canceled
}

// OperationSignal creates an operation-local signal for public APIs whose signal
// is optional. A nil context becomes context.Background().
func OperationSignal(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// RaceWithAbortSignal stops waiting for an operation when its signal aborts,
// while continuing to observe the abandoned operation so a later completion is
// always handled. The operation is started immediately; its result is dropped
// once the signal aborts.
func RaceWithAbortSignal[T any](operation func() (T, error), signal context.Context) (T, error) {
	var zero T
	if signal == nil {
		signal = context.Background()
	}
	if signal.Err() != nil {
		// Observe the abandoned operation so a later rejection is handled.
		go func() {
			var discard T
			discard, _ = operation()
			_ = discard
		}()
		return zero, abortReason(signal)
	}

	type outcome struct {
		value T
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		value, err := operation()
		done <- outcome{value: value, err: err}
	}()

	select {
	case result := <-done:
		return result.value, result.err
	case <-signal.Done():
		// Keep observing the abandoned operation; the buffered channel already
		// drains the goroutine, so it cannot leak.
		return zero, abortReason(signal)
	}
}
