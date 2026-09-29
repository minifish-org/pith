// Package chordcontext is the Go adaptation of Chord's invocation-scoped
// context values (packages/chord/src/context/index.ts and the Context /
// ContextKey declarations of packages/chord/src/types.ts).
//
// This is a Go port of Chord at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Cancellation is carried by the standard context.Context instead of a separate
// AbortSignal value: the Go context tree already provides parent/child
// cancellation and deadline propagation. Context values keep the upstream
// identity-key semantics through the unexported pointer token in ContextKey, so
// recreating a key with the same description never aliases the original.
package chordcontext

import (
	"context"
)

// Context is an immutable invocation-scoped value carrier. It is the Go
// adaptation of the upstream Context interface. Cancellation lives in the
// standard context tree.
type Context = context.Context

// ContextKey is a typed identity for one value carried by a Context. Keys
// compare by identity, matching the upstream Symbol token.
type ContextKey[T any] struct {
	token *contextKeyToken
}

type contextKeyToken struct {
	description string
}

// CreateContextKey returns a fresh key tagged with the supplied description.
// The description mirrors the upstream Symbol description and is used for
// diagnostics only; it does not participate in key identity.
func CreateContextKey[T any](description string) ContextKey[T] {
	return ContextKey[T]{token: &contextKeyToken{description: description}}
}

// WithContextValue derives a context with one additional or replaced value.
func WithContextValue[T any](key ContextKey[T], value T, parent Context) Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, key, value)
}

// Value reads the value stored under key. The second result is false when the
// key is absent.
func Value[T any](ctx Context, key ContextKey[T]) (T, bool) {
	var zero T
	if ctx == nil {
		return zero, false
	}
	raw := ctx.Value(key)
	if raw == nil {
		return zero, false
	}
	value, ok := raw.(T)
	if !ok {
		return zero, false
	}
	return value, true
}

// namedContext is an empty context that only differs from another empty context
// by its String() description, mirroring the upstream EmptyContext.
type namedContext struct {
	context.Context
	name string
}

func (c namedContext) String() string { return c.name }

// BackgroundContext is the shared empty context for unscoped work.
var BackgroundContext Context = namedContext{Context: context.Background(), name: "[Context BACKGROUND_CONTEXT]"}

// TodoContext is the shared empty context used where no context is meaningful.
var TodoContext Context = namedContext{Context: context.Background(), name: "[Context TODO_CONTEXT]"}

// WithAbortSignal derives a context cancelled by either parent or signal. The
// parent context remains unchanged. A nil signal leaves the parent untouched.
func WithAbortSignal(signal Context, parent Context) Context {
	if signal == nil {
		return parent
	}
	if parent == nil {
		parent = context.Background()
	}
	derived, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(signal, cancel)
	context.AfterFunc(derived, func() { stop() })
	return derived
}

// WithoutAbortSignal derives a context retaining all values except caller
// cancellation. Intended for mandatory cleanup only.
func WithoutAbortSignal(parent Context) Context {
	if parent == nil {
		return context.Background()
	}
	return context.WithoutCancel(parent)
}

// WithCancel derives an independently cancellable child context.
func WithCancel(parent Context) (Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithCancel(parent)
}

// AwaitWithContext observes work until it settles or the invocation is
// cancelled. Cancellation rejects only this waiter; it does not cancel the
// underlying work.
func AwaitWithContext[T any](ctx Context, work func() (T, error)) (T, error) {
	var zero T
	if ctx == nil {
		return work()
	}
	type outcome struct {
		value T
		err   error
	}
	settled := make(chan outcome, 1)
	go func() {
		value, err := work()
		settled <- outcome{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case result := <-settled:
		return result.value, result.err
	}
}
