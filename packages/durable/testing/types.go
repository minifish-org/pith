// Package durabletesting is the native, runner-independent port of Pi Durable
// `packages/durable/src/testing`. It exports the reusable storage conformance
// suite and benchmark dataset builders so any Storage adapter can be validated
// through the public durable.Storage contract.
//
// The package is named durabletesting (import path .../packages/durable/testing)
// so it never shadows the standard library testing package. It depends only on
// the shared durable records package; it never imports a concrete adapter, so
// adapters may depend on it without creating a cycle.
package durabletesting

import (
	"context"

	"github.com/minifish-org/pith/packages/durable"
)

// StorageProvider opens a fresh adapter for exactly one conformance case, calls
// use once while the adapter is open, and returns use's error. It must not reuse
// an adapter across cases. This mirrors the source StorageConformanceProvider
// `(use) => Promise<void>` with Go error propagation instead of a rejected
// promise.
type StorageProvider func(context.Context, func(durable.Storage) error) error

// StorageConformanceCase is one named, runner-independent conformance case. Run
// returns a descriptive error, not a panic, so callers may register it with any
// test runner.
type StorageConformanceCase struct {
	Name string
	Run  func(context.Context) error
}

// StorageConformanceAssertions is the native form of the source assertion facade
// (ok/strictEqual/deepEqual/partialDeepEqual/greaterThan/rejects). Each check
// returns a descriptive error instead of throwing, which is the documented Go
// adaptation of the JavaScript assertion helpers. The concrete implementation is
// provided by NativeAssertions; independent callers may supply their own.
type StorageConformanceAssertions interface {
	// OK fails when value is false, reporting message.
	OK(value bool, message string) error
	// StrictEqual compares two values for strict identity (=== after JSON
	// normalization of comparable scalar types).
	StrictEqual(actual, expected any) error
	// DeepEqual compares two values structurally.
	DeepEqual(actual, expected any) error
	// PartialDeepEqual checks that expected is a structural subset of actual.
	PartialDeepEqual(actual, expected any) error
	// GreaterThan fails unless actual > expected.
	GreaterThan(actual, expected int64) error
	// Rejects fails unless err is non-nil and its message contains
	// messageIncludes.
	Rejects(err error, messageIncludes string) error
}
