// Backend-neutral reference telemetry implementation that records spans in
// process memory.
//
// This is a Go port of packages/telemetry/src/memory.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package telemetry

import (
	"context"
	"sync"
)

// RecordedTelemetryEvent is a recorded span event snapshot.
type RecordedTelemetryEvent struct {
	Name       string         `json:"name"`
	Attributes SpanAttributes `json:"attributes"`
}

// RecordedTelemetrySpan is a detached span snapshot.
type RecordedTelemetrySpan struct {
	Id          int                      `json:"id"`
	ParentId    *int                     `json:"parentId"`
	Name        string                   `json:"name"`
	Attributes  SpanAttributes           `json:"attributes"`
	Events      []RecordedTelemetryEvent `json:"events"`
	Status      SpanStatus               `json:"status"`
	Settled     bool                     `json:"settled"`
	EndSequence *int                     `json:"endSequence,omitempty"`
}

type mutableRecordedTelemetryEvent struct {
	name       string
	attributes SpanAttributes
}

type mutableRecordedTelemetrySpan struct {
	id             int
	parentId       *int
	name           string
	attributes     SpanAttributes
	events         []mutableRecordedTelemetryEvent
	status         SpanStatus
	explicitStatus bool
	settled        bool
	endSequence    *int
}

// inMemoryTelemetryState is the mutable recording state of one in-memory
// telemetry context. The guard protects span allocation, event/attribute writes
// and settlement; it is never held while a user callback runs.
type inMemoryTelemetryState struct {
	mu              sync.Mutex
	spans           []*mutableRecordedTelemetrySpan
	nextSpanId      int
	nextEndSequence int
}

// lock acquires the recording guard.
func (s *inMemoryTelemetryState) lock() { s.mu.Lock() }

// unlock releases the recording guard.
func (s *inMemoryTelemetryState) unlock() { s.mu.Unlock() }

// copyAttributeValue copies an attribute value, cloning array payloads.
func copyAttributeValue(value AttributeValue) AttributeValue {
	return value.Clone()
}

// copyAttributes builds a detached copy of an attribute map. Present keys with
// an undefined value (an empty type) are dropped, matching upstream. An
// unreadable value is copied verbatim so a caller inspecting an unreadable
// payload can still see it; readAttributes is what enforces atomicity.
func copyAttributes(attributes SpanAttributes) SpanAttributes {
	copy := SpanAttributes{}
	if attributes == nil {
		return copy
	}
	for name, value := range attributes {
		copied, ok := readAttributeValue(value)
		if !ok {
			continue
		}
		copy[name] = copyAttributeValue(copied)
	}
	return copy
}

// attributesUnreadable reports whether any value in the map is marked
// unreadable. A single unreadable value makes the whole payload unreadable,
// matching upstream's atomic read of a throwing Proxy.
func attributesUnreadable(attributes SpanAttributes) bool {
	for _, value := range attributes {
		if value.Type == AttributeTypeUnreadable {
			return true
		}
	}
	return false
}

// readAttributeValue reads a single attribute value defensively. A malformed
// payload reports ok=false instead of failing the caller. An undefined value
// (empty type) reports ok=false so it is dropped rather than recorded.
func readAttributeValue(value AttributeValue) (result AttributeValue, ok bool) {
	defer func() {
		if recover() != nil {
			result, ok = AttributeValue{}, false
		}
	}()
	if value.Type == "" || value.Type == AttributeTypeUnreadable {
		return AttributeValue{}, false
	}
	return value, true
}

// readAttributes reads a whole attribute map defensively. If any value is
// unreadable the whole payload reports ok=false so the caller records nothing
// from it and never fails the request.
func readAttributes(attributes SpanAttributes) (result SpanAttributes, ok bool) {
	defer func() {
		if recover() != nil {
			result, ok = nil, false
		}
	}()
	if attributesUnreadable(attributes) {
		return nil, false
	}
	return copyAttributes(attributes), true
}

// mergeAttributes merges attributes over the current values. Present keys with
// an undefined value are skipped rather than clearing the existing value.
func mergeAttributes(current SpanAttributes, attributes SpanAttributes) SpanAttributes {
	merged := copyAttributes(current)
	if attributes == nil {
		return merged
	}
	for name, value := range attributes {
		copied, ok := readAttributeValue(value)
		if !ok {
			continue
		}
		merged[name] = copyAttributeValue(copied)
	}
	return merged
}

// copyStatus copies a status value.
func copyStatus(status SpanStatus) SpanStatus {
	if status.Status == "ok" || status.Status == "" {
		return SpanStatus{Status: "ok"}
	}
	if status.Error != nil {
		return SpanStatus{Status: "error", Error: &SpanError{Name: status.Error.Name, Message: status.Error.Message}}
	}
	return SpanStatus{Status: "error"}
}

// readStatus reads a status defensively. A status whose discriminant is not a
// recognized value is treated as unreadable so the caller records nothing.
func readStatus(status SpanStatus) (result SpanStatus, ok bool) {
	if status.Status != "ok" && status.Status != "error" {
		return SpanStatus{}, false
	}
	return copyStatus(status), true
}

// automaticErrorStatus derives an error status from a callback failure. It
// returns an error status without details when the failure cannot be inspected.
func automaticErrorStatus(err error) SpanStatus {
	if err == nil {
		return SpanStatus{Status: "error"}
	}
	return SpanStatus{Status: "error", Error: &SpanError{Name: errorName(err), Message: err.Error()}}
}

// errorName reports the provider-neutral error name. It defaults to "Error" so
// that wrappers whose dynamic type has no explicit name still classify as an
// error, matching the JavaScript Error convention.
func errorName(err error) string {
	if named, ok := err.(interface{ Name() string }); ok {
		if name := named.Name(); name != "" {
			return name
		}
	}
	return "Error"
}

// settleSpan marks a span settled and assigns its end sequence. It is a no-op
// when the span already settled. An automatic error status never overwrites an
// explicitly set status.
func settleSpan(state *inMemoryTelemetryState, span *mutableRecordedTelemetrySpan, failed bool, err error) {
	state.lock()
	defer state.unlock()

	if span.settled {
		return
	}
	if failed && !span.explicitStatus {
		span.status = automaticErrorStatus(err)
	}
	span.settled = true
	endSequence := state.nextEndSequence
	state.nextEndSequence++
	span.endSequence = &endSequence
}

// createSpan allocates a span record. It returns ok=false when the options
// cannot be read, in which case the caller falls back to the no-op context.
//
// The caller must hold the recording guard.
func createSpan(state *inMemoryTelemetryState, parent *mutableRecordedTelemetrySpan, options SpanOptions) (span *mutableRecordedTelemetrySpan, ok bool) {
	attributes, attributesOK := readAttributes(options.Attributes)
	if !attributesOK {
		return nil, false
	}
	id := state.nextSpanId
	state.nextSpanId++
	var parentId *int
	if parent != nil {
		parentId = &parent.id
	}
	return &mutableRecordedTelemetrySpan{
		id:         id,
		parentId:   parentId,
		name:       options.Name,
		attributes: attributes,
		status:     SpanStatus{Status: "ok"},
	}, true
}

// startInMemorySpan starts a recorded span and invokes the callback.
//
// A child of an already settled span is admitted through the shared no-op
// context so the callback still runs but nothing is recorded. Failures to
// allocate or record a span likewise fall back to no-op rather than failing the
// request, matching upstream's passivity guarantee.
//
// The recording guard is held only to allocate the span record; the callback
// runs without it so callbacks may reenter the context and so a panic cannot
// leave the guard held.
func startInMemorySpan(ctx context.Context, state *inMemoryTelemetryState, parent *mutableRecordedTelemetrySpan, options SpanOptions, callback SpanCallback) error {
	if parent != nil {
		state.lock()
		settled := parent.settled
		state.unlock()
		if settled {
			return StartNoopSpan(ctx, options, callback)
		}
	}

	state.lock()
	recordedSpan, ok := createSpan(state, parent, options)
	if !ok {
		state.unlock()
		return StartNoopSpan(ctx, options, callback)
	}
	state.spans = append(state.spans, recordedSpan)
	state.unlock()

	span := &inMemorySpan{state: state, recordedSpan: recordedSpan}

	err := callback(ctx, span)
	if err != nil {
		settleSpan(state, recordedSpan, true, err)
		return err
	}
	settleSpan(state, recordedSpan, false, nil)
	return nil
}

// inMemorySpan is the live span handle handed to callbacks.
type inMemorySpan struct {
	state        *inMemoryTelemetryState
	recordedSpan *mutableRecordedTelemetrySpan
}

// StartSpan starts a child span of this span.
func (s *inMemorySpan) StartSpan(ctx context.Context, options SpanOptions, callback SpanCallback) error {
	return startInMemorySpan(ctx, s.state, s.recordedSpan, options, callback)
}

// AddEvent records a span event. Calls after settlement are inert.
func (s *inMemorySpan) AddEvent(name string, attributes SpanAttributes) {
	copied, ok := readAttributes(attributes)
	if !ok {
		return
	}
	s.state.lock()
	defer s.state.unlock()
	if s.recordedSpan.settled {
		return
	}
	s.recordedSpan.events = append(s.recordedSpan.events, mutableRecordedTelemetryEvent{name: name, attributes: copied})
}

// SetAttributes merges attributes into the span. A failed read leaves the span
// attributes unchanged, so a partially readable payload never survives.
func (s *inMemorySpan) SetAttributes(attributes SpanAttributes) {
	copied, ok := readAttributes(attributes)
	if !ok {
		return
	}
	s.state.lock()
	defer s.state.unlock()
	if s.recordedSpan.settled {
		return
	}
	s.recordedSpan.attributes = mergeAttributes(s.recordedSpan.attributes, copied)
}

// SetStatus sets the span status explicitly. An explicit status is retained even
// when the callback later fails.
func (s *inMemorySpan) SetStatus(status SpanStatus) {
	copied, ok := readStatus(status)
	if !ok {
		return
	}
	s.state.lock()
	defer s.state.unlock()
	if s.recordedSpan.settled {
		return
	}
	s.recordedSpan.status = copied
	s.recordedSpan.explicitStatus = true
}

// InMemoryTelemetryContext is a backend-neutral reference telemetry context that
// records spans in process memory. Create a fresh instance to isolate tests or
// independent recording scopes.
//
// The recorded state is guarded so concurrent callers cannot lose spans or
// duplicate identifiers. Callbacks are never invoked while the guard is held.
type InMemoryTelemetryContext struct {
	stateMu sync.Mutex
	state   *inMemoryTelemetryState
}

// NewInMemoryTelemetryContext creates an isolated in-memory telemetry context.
func NewInMemoryTelemetryContext() *InMemoryTelemetryContext {
	return &InMemoryTelemetryContext{
		state: &inMemoryTelemetryState{nextSpanId: 1, nextEndSequence: 1},
	}
}

// StartSpan starts a root recorded span and invokes the callback.
func (c *InMemoryTelemetryContext) StartSpan(ctx context.Context, options SpanOptions, callback SpanCallback) error {
	return startInMemorySpan(ctx, c.state, nil, options, callback)
}

// GetSpans returns detached snapshots in span-start order.
func (c *InMemoryTelemetryContext) GetSpans() []RecordedTelemetrySpan {
	state := c.state
	state.mu.Lock()
	defer state.mu.Unlock()

	spans := make([]RecordedTelemetrySpan, 0, len(state.spans))
	for _, span := range state.spans {
		events := make([]RecordedTelemetryEvent, 0, len(span.events))
		for _, event := range span.events {
			events = append(events, RecordedTelemetryEvent{
				Name:       event.name,
				Attributes: copyAttributes(event.attributes),
			})
		}
		snapshot := RecordedTelemetrySpan{
			Id:         span.id,
			ParentId:   span.parentId,
			Name:       span.name,
			Attributes: copyAttributes(span.attributes),
			Events:     events,
			Status:     copyStatus(span.status),
			Settled:    span.settled,
		}
		if span.endSequence != nil {
			endSequence := *span.endSequence
			snapshot.EndSequence = &endSequence
		}
		spans = append(spans, snapshot)
	}
	return spans
}

// Spans is an alias for GetSpans, matching the upstream `spans` accessor used by
// the in-memory reference implementation.
func (c *InMemoryTelemetryContext) Spans() []RecordedTelemetrySpan {
	return c.GetSpans()
}
