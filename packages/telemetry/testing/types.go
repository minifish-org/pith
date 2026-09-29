// Telemetry adapter conformance fixture types.
//
// This is a Go port of packages/telemetry/src/testing/types.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package testing

import (
	"context"

	telemetry "github.com/minifish-org/pith/packages/telemetry"
)

// TelemetryAdapterFixture is a fresh adapter instance plus a normalized snapshot
// reader, owned by one conformance case.
type TelemetryAdapterFixture interface {
	// Context is the adapter's telemetry context under test.
	Context() telemetry.TelemetryContext
	// GetSpans returns the adapter's normalized span snapshots.
	GetSpans(ctx context.Context) ([]telemetry.RecordedTelemetrySpan, error)
	// Close releases the fixture. It replaces the upstream AsyncDisposable
	// contract; the conformance runner always calls it.
	Close(ctx context.Context) error
}

// TelemetryAdapterFixtureFactory creates an isolated adapter fixture for one
// conformance case.
type TelemetryAdapterFixtureFactory func(ctx context.Context) (TelemetryAdapterFixture, error)

// TelemetryAdapterConformanceCase is a runner-independent conformance case that
// can be registered with any test framework.
type TelemetryAdapterConformanceCase struct {
	Group string
	Name  string
	Run   func(ctx context.Context) error
}

// ConformanceAssertionFailed reports a conformance expectation that did not hold.
type ConformanceAssertionFailed struct {
	Message string
}

// Error implements error.
func (e *ConformanceAssertionFailed) Error() string { return e.Message }

// ReferenceTelemetryFixture builds a fixture backed by
// InMemoryTelemetryContext; used by the reference conformance run and by
// downstream adapters that want the shared behavior as a baseline.
type ReferenceTelemetryFixture struct {
	telemetryContext *telemetry.InMemoryTelemetryContext
}

// NewReferenceTelemetryFixture creates the reference in-memory fixture.
func NewReferenceTelemetryFixture() *ReferenceTelemetryFixture {
	return &ReferenceTelemetryFixture{telemetryContext: telemetry.NewInMemoryTelemetryContext()}
}

// Context returns the in-memory telemetry context.
func (f *ReferenceTelemetryFixture) Context() telemetry.TelemetryContext { return f.telemetryContext }

// GetSpans returns the recorded span snapshots.
func (f *ReferenceTelemetryFixture) GetSpans(context.Context) ([]telemetry.RecordedTelemetrySpan, error) {
	return f.telemetryContext.GetSpans(), nil
}

// Close releases the fixture. The in-memory fixture owns no resources.
func (f *ReferenceTelemetryFixture) Close(context.Context) error { return nil }

// ReferenceTelemetryFixtureFactory creates reference fixtures for the shared
// conformance cases.
func ReferenceTelemetryFixtureFactory(ctx context.Context) (TelemetryAdapterFixture, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return NewReferenceTelemetryFixture(), nil
}
