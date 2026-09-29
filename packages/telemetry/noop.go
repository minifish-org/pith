// NOOP_TELEMETRY_CONTEXT: the shared telemetry context used when an application
// does not provide one.
//
// This is a Go port of packages/telemetry/src/noop.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package telemetry

import "context"

// StartNoopSpan admits the callback with the shared no-op span. A panic in the
// callback is not converted into an error; it propagates to the caller, matching
// the upstream synchronous admit path.
func StartNoopSpan(ctx context.Context, _ SpanOptions, callback SpanCallback) error {
	return callback(ctx, noopTelemetrySpan)
}

// noopTelemetrySpanType is the shared frozen no-op span. Its method set is inert
// and it never records anything.
type noopTelemetrySpanType struct{}

// noopTelemetrySpan is the shared no-op span instance.
var noopTelemetrySpan TelemetrySpan = noopTelemetrySpanType{}

// StartSpan admits the child callback through the shared no-op span.
func (noopTelemetrySpanType) StartSpan(ctx context.Context, options SpanOptions, callback SpanCallback) error {
	return StartNoopSpan(ctx, options, callback)
}

// AddEvent is inert.
func (noopTelemetrySpanType) AddEvent(string, SpanAttributes) {}

// SetAttributes is inert.
func (noopTelemetrySpanType) SetAttributes(SpanAttributes) {}

// SetStatus is inert.
func (noopTelemetrySpanType) SetStatus(SpanStatus) {}

// NoopTelemetryContext is the type of the shared no-op telemetry context.
type NoopTelemetryContext struct{}

// StartSpan admits the callback with the shared no-op span.
func (NoopTelemetryContext) StartSpan(ctx context.Context, options SpanOptions, callback SpanCallback) error {
	return StartNoopSpan(ctx, options, callback)
}

// NOOP_TELEMETRY_CONTEXT is the shared telemetry context used when an
// application does not provide one.
var NOOP_TELEMETRY_CONTEXT TelemetryContext = NoopTelemetryContext{}
