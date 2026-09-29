// Package harnesscontext is the Go port of packages/agent/src/harness/context.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The harness re-exports the Chord context contract and adds the telemetry
// parent lookup used by harness-owned spans. Cancellation is carried by the
// standard context.Context, as in packages/chord/context.
package harnesscontext

import (
	"context"

	chordcontext "github.com/minifish-org/pith/packages/chord/context"
	"github.com/minifish-org/pith/packages/telemetry"
)

// Context is the invocation-scoped value carrier shared by the harness.
type Context = chordcontext.Context

// ContextKey is a typed identity for one Context value.
type ContextKey[T any] = chordcontext.ContextKey[T]

// BackgroundContext is the shared empty context for unscoped work.
var BackgroundContext Context = chordcontext.BackgroundContext

// TodoContext is the shared empty context used where no context is meaningful.
var TodoContext Context = chordcontext.TodoContext

// CreateContextKey returns a fresh key tagged with the supplied description.
func CreateContextKey[T any](description string) ContextKey[T] {
	return chordcontext.CreateContextKey[T](description)
}

// WithContextValue derives a context with one additional or replaced value.
func WithContextValue[T any](key ContextKey[T], value T, parent Context) Context {
	return chordcontext.WithContextValue(key, value, parent)
}

// Value reads the value stored under key.
func Value[T any](ctx Context, key ContextKey[T]) (T, bool) {
	return chordcontext.Value(ctx, key)
}

// WithAbortSignal derives a context cancelled by either parent or signal.
func WithAbortSignal(signal Context, parent Context) Context {
	return chordcontext.WithAbortSignal(signal, parent)
}

// WithoutAbortSignal derives a context retaining all values except caller
// cancellation. Intended for mandatory cleanup only.
func WithoutAbortSignal(parent Context) Context {
	return chordcontext.WithoutAbortSignal(parent)
}

// WithCancel derives an independently cancellable child context.
func WithCancel(parent Context) (Context, context.CancelFunc) {
	return chordcontext.WithCancel(parent)
}

// AwaitWithContext observes work until it settles or the invocation is
// cancelled, without cancelling the underlying work.
func AwaitWithContext[T any](ctx Context, work func() (T, error)) (T, error) {
	return chordcontext.AwaitWithContext(ctx, work)
}

var telemetryContextKey = chordcontext.CreateContextKey[telemetry.TelemetryContext]("pi.telemetryContext")

// GetTelemetryContext returns the telemetry parent attached to a context, or
// the shared no-op parent.
func GetTelemetryContext(ctx Context) telemetry.TelemetryContext {
	if value, ok := chordcontext.Value(ctx, telemetryContextKey); ok && value != nil {
		return value
	}
	return telemetry.NOOP_TELEMETRY_CONTEXT
}

// WithTelemetryContext derives a context whose telemetry children use the
// supplied parent.
func WithTelemetryContext(telemetryContext telemetry.TelemetryContext, ctx Context) Context {
	return chordcontext.WithContextValue(telemetryContextKey, telemetryContext, ctx)
}
