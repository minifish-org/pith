package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

// Mirrors the memory.ts fixture recorded by the conformance cases: a nested
// parent/child span with an event, preserving attributes, status, settlement
// and end sequence.
func TestInMemoryTelemetryRecordsNestedSpans(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()

	err := telemetryContext.StartSpan(ctx, SpanOptions{
		Name: "parent",
		Attributes: SpanAttributes{
			"empty": StringAttribute(""),
			"zero":  NumberAttribute(0),
			"flag":  BoolAttribute(false),
		},
	}, func(spanCtx context.Context, span TelemetrySpan) error {
		span.AddEvent("begin", SpanAttributes{"values": NumbersAttribute([]float64{1, 2})})
		return span.StartSpan(spanCtx, SpanOptions{Name: "child"}, func(context.Context, TelemetrySpan) error {
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	spans := telemetryContext.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans = %d; want 2", len(spans))
	}
	parent, child := spans[0], spans[1]
	if parent.Id != 1 || parent.ParentId != nil || parent.Name != "parent" {
		t.Fatalf("parent = %+v", parent)
	}
	if child.Id != 2 || child.ParentId == nil || *child.ParentId != 1 || child.Name != "child" {
		t.Fatalf("child = %+v", child)
	}
	if len(parent.Events) != 1 || parent.Events[0].Name != "begin" {
		t.Fatalf("parent events = %+v", parent.Events)
	}
	values, ok := parent.Events[0].Attributes["values"]
	if !ok || values.Type != AttributeTypeNumberArr || len(values.Numbers) != 2 {
		t.Fatalf("event attribute = %+v", parent.Events[0].Attributes)
	}
	if parent.Status.Status != "ok" || !parent.Settled || parent.EndSequence == nil {
		t.Fatalf("parent status = %+v", parent)
	}
	if child.EndSequence == nil || parent.EndSequence == nil || *child.EndSequence >= *parent.EndSequence {
		t.Fatalf("end sequences = %v / %v", child.EndSequence, parent.EndSequence)
	}

	zero, ok := parent.Attributes["zero"]
	if !ok || zero.Type != AttributeTypeNumber || zero.Number == nil || *zero.Number != 0 {
		t.Fatalf("zero attribute = %+v", parent.Attributes)
	}
	empty, ok := parent.Attributes["empty"]
	if !ok || empty.String == nil || *empty.String != "" {
		t.Fatalf("empty attribute = %+v", parent.Attributes)
	}
	flag, ok := parent.Attributes["flag"]
	if !ok || flag.Bool == nil || *flag.Bool {
		t.Fatalf("flag attribute = %+v", parent.Attributes)
	}
}

// Mirrors the fixture's failure variant: the automatic error status is recorded
// on both the failing span and its parent.
func TestInMemoryTelemetryAutomaticErrorStatus(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()
	fixtureError := errors.New("fixture error")

	err := telemetryContext.StartSpan(ctx, SpanOptions{Name: "parent"}, func(spanCtx context.Context, span TelemetrySpan) error {
		return span.StartSpan(spanCtx, SpanOptions{Name: "child"}, func(context.Context, TelemetrySpan) error {
			return fixtureError
		})
	})
	if !errors.Is(err, fixtureError) {
		t.Fatalf("error = %v; want the original failure", err)
	}

	for _, span := range telemetryContext.GetSpans() {
		if span.Status.Status != "error" {
			t.Fatalf("%s status = %+v", span.Name, span.Status)
		}
		if span.Status.Error == nil || span.Status.Error.Message != "fixture error" {
			t.Fatalf("%s error = %+v", span.Name, span.Status.Error)
		}
	}
}

// Mirrors the explicit-status scenarios: the last explicit status wins and is
// never overwritten by an automatic error status.
func TestInMemoryTelemetryExplicitStatus(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()
	thrown := errors.New("after explicit status")

	if err := telemetryContext.StartSpan(ctx, SpanOptions{Name: "last-status"}, func(_ context.Context, span TelemetrySpan) error {
		span.SetStatus(StatusError("Expected", "first"))
		span.SetStatus(StatusOK())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := telemetryContext.StartSpan(ctx, SpanOptions{Name: "explicit-before-throw"}, func(_ context.Context, span TelemetrySpan) error {
		span.SetStatus(StatusOK())
		return thrown
	}); !errors.Is(err, thrown) {
		t.Fatalf("error = %v", err)
	}

	spans := telemetryContext.GetSpans()
	statuses := map[string]SpanStatus{}
	for _, span := range spans {
		statuses[span.Name] = span.Status
	}
	if statuses["last-status"].Status != "ok" {
		t.Fatalf("last-status = %+v", statuses["last-status"])
	}
	if statuses["explicit-before-throw"].Status != "ok" {
		t.Fatalf("explicit-before-throw = %+v", statuses["explicit-before-throw"])
	}
}

// Mirrors the recording scenarios: attributes merge without undefined values
// and events keep their order.
func TestInMemoryTelemetryRecording(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()

	err := telemetryContext.StartSpan(ctx, SpanOptions{
		Name: "recording",
		Attributes: SpanAttributes{
			"start":     StringAttribute("value"),
			"overwrite": StringAttribute("start"),
			"ignored":   {}, // undefined value must be dropped
		},
	}, func(_ context.Context, span TelemetrySpan) error {
		span.SetAttributes(SpanAttributes{"count": NumberAttribute(1), "overwrite": StringAttribute("middle")})
		span.SetAttributes(SpanAttributes{"count": {}, "overwrite": StringAttribute("end")})
		span.AddEvent("first", SpanAttributes{"index": NumberAttribute(1), "ignored": {}})
		span.AddEvent("second", SpanAttributes{"index": NumberAttribute(2)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	spans := telemetryContext.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d", len(spans))
	}
	span := spans[0]
	if len(span.Attributes) != 3 {
		t.Fatalf("attributes = %+v", span.Attributes)
	}
	overwrite := span.Attributes["overwrite"]
	if overwrite.String == nil || *overwrite.String != "end" {
		t.Fatalf("overwrite = %+v", overwrite)
	}
	count := span.Attributes["count"]
	if count.Number == nil || *count.Number != 1 {
		t.Fatalf("count = %+v", count)
	}
	if len(span.Events) != 2 || span.Events[0].Name != "first" || span.Events[1].Name != "second" {
		t.Fatalf("events = %+v", span.Events)
	}
	if _, ok := span.Events[0].Attributes["ignored"]; ok {
		t.Fatalf("undefined event attribute recorded: %+v", span.Events[0].Attributes)
	}
}

// Mirrors the atomicity scenario: an unreadable attribute payload must not leak
// partially readable keys.
func TestInMemoryTelemetryAtomicAttributeFailure(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()

	err := telemetryContext.StartSpan(ctx, SpanOptions{
		Name:       "atomic-attributes",
		Attributes: SpanAttributes{"retained": StringAttribute("value")},
	}, func(_ context.Context, span TelemetrySpan) error {
		span.SetAttributes(SpanAttributes{
			"partial":    StringAttribute("must not survive"),
			"unreadable": UnreadableValue(),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	span := telemetryContext.GetSpans()[0]
	if len(span.Attributes) != 1 {
		t.Fatalf("attributes = %+v", span.Attributes)
	}
	if _, ok := span.Attributes["partial"]; ok {
		t.Fatalf("partial attribute survived: %+v", span.Attributes)
	}
}

// Mirrors the settlement scenario: post-settlement calls are inert and the
// child callback still runs.
func TestInMemoryTelemetrySettledCallsInert(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()

	var settledSpan TelemetrySpan
	err := telemetryContext.StartSpan(ctx, SpanOptions{
		Name:       "settled",
		Attributes: SpanAttributes{"value": StringAttribute("initial")},
	}, func(_ context.Context, span TelemetrySpan) error {
		settledSpan = span
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if settledSpan == nil {
		t.Fatal("expected the callback span")
	}

	settledSpan.SetAttributes(SpanAttributes{"value": StringAttribute("late")})
	settledSpan.AddEvent("late", SpanAttributes{"value": BoolAttribute(true)})
	settledSpan.SetStatus(StatusErrorBare())
	childAdmitted := false
	if err := settledSpan.StartSpan(ctx, SpanOptions{Name: "late-child"}, func(context.Context, TelemetrySpan) error {
		childAdmitted = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !childAdmitted {
		t.Fatal("late child callback not admitted")
	}

	spans := telemetryContext.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("spans = %d; want 1", len(spans))
	}
	if spans[0].Attributes["value"].String == nil || *spans[0].Attributes["value"].String != "initial" {
		t.Fatalf("attributes = %+v", spans[0].Attributes)
	}
	if len(spans[0].Events) != 0 {
		t.Fatalf("events = %+v", spans[0].Events)
	}
	if spans[0].Status.Status != "ok" {
		t.Fatalf("status = %+v", spans[0].Status)
	}
}

// Verifies parentage for concurrent children.
func TestInMemoryTelemetryConcurrentChildren(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)

	err := telemetryContext.StartSpan(ctx, SpanOptions{Name: "parent"}, func(parentCtx context.Context, parent TelemetrySpan) error {
		go func() {
			firstDone <- parent.StartSpan(parentCtx, SpanOptions{Name: "first-child"}, func(context.Context, TelemetrySpan) error {
				<-releaseFirst
				return nil
			})
		}()
		if err := parent.StartSpan(parentCtx, SpanOptions{Name: "second-child"}, func(context.Context, TelemetrySpan) error {
			return nil
		}); err != nil {
			return err
		}
		close(releaseFirst)
		return <-firstDone
	})
	if err != nil {
		t.Fatal(err)
	}

	byName := map[string]RecordedTelemetrySpan{}
	for _, span := range telemetryContext.GetSpans() {
		byName[span.Name] = span
	}
	parent := byName["parent"]
	first := byName["first-child"]
	second := byName["second-child"]
	if parent.ParentId != nil || first.ParentId == nil || *first.ParentId != parent.Id {
		t.Fatalf("parentage = %+v / %+v", parent, first)
	}
	if second.ParentId == nil || *second.ParentId != parent.Id {
		t.Fatalf("second child parentage = %+v", second)
	}
	if *second.EndSequence >= *first.EndSequence || *first.EndSequence >= *parent.EndSequence {
		t.Fatalf("end sequences = %v / %v / %v", second.EndSequence, first.EndSequence, parent.EndSequence)
	}
}

// Mirrors the passivity scenarios: unreadable payloads never fail a request.
func TestInMemoryTelemetryPassivity(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()
	calls := 0

	err := telemetryContext.StartSpan(ctx, SpanOptions{
		Name:       "unreadable-options",
		Attributes: SpanAttributes{"secret": UnreadableValue()},
	}, func(context.Context, TelemetrySpan) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("unreadable options failed the request: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d; want 1", calls)
	}
	if len(telemetryContext.GetSpans()) != 0 {
		t.Fatalf("unreadable options recorded a span")
	}

	err = telemetryContext.StartSpan(ctx, SpanOptions{Name: "unreadable-recording"}, func(_ context.Context, span TelemetrySpan) error {
		span.SetAttributes(SpanAttributes{"secret": UnreadableValue()})
		span.AddEvent("unreadable-event", SpanAttributes{"secret": UnreadableValue()})
		span.SetStatus(SpanStatus{Status: "unreadable"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	recorded := telemetryContext.GetSpans()
	if len(recorded) != 1 {
		t.Fatalf("spans = %d; want 1", len(recorded))
	}
	if len(recorded[0].Attributes) != 0 || len(recorded[0].Events) != 0 {
		t.Fatalf("unreadable payload recorded: %+v", recorded[0])
	}
	if recorded[0].Status.Status != "ok" {
		t.Fatalf("status = %+v", recorded[0].Status)
	}
}

// Verifies the snapshots are detached and that a status failure does not clear
// the automatic error classification.
func TestInMemoryTelemetryStatusFailureKeepsAutoError(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()
	rejection := errors.New("rejected after unreadable status")

	err := telemetryContext.StartSpan(ctx, SpanOptions{Name: "unreadable-status"}, func(_ context.Context, span TelemetrySpan) error {
		span.SetStatus(SpanStatus{Status: "unreadable", Error: &SpanError{Name: "x"}})
		return rejection
	})
	if !errors.Is(err, rejection) {
		t.Fatalf("error = %v", err)
	}
	span := telemetryContext.GetSpans()[0]
	if span.Status.Status != "error" {
		t.Fatalf("status = %+v", span.Status)
	}
	if span.Status.Error == nil || span.Status.Error.Message != "rejected after unreadable status" {
		t.Fatalf("error = %+v", span.Status.Error)
	}
}

// Verifies GetSpans returns snapshots that cannot mutate the recorded state.
func TestInMemoryTelemetrySnapshotsAreDetached(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()

	err := telemetryContext.StartSpan(ctx, SpanOptions{
		Name:       "snapshot",
		Attributes: SpanAttributes{"list": StringsAttribute([]string{"a"})},
	}, func(_ context.Context, span TelemetrySpan) error {
		span.SetAttributes(SpanAttributes{"extra": StringAttribute("x")})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	first := telemetryContext.GetSpans()[0]
	first.Attributes["extra"] = StringAttribute("mutated")
	first.Attributes["list"].Strings[0] = "mutated"

	second := telemetryContext.GetSpans()[0]
	if second.Attributes["extra"].String == nil || *second.Attributes["extra"].String != "x" {
		t.Fatalf("snapshot mutation leaked: %+v", second.Attributes)
	}
	if second.Attributes["list"].Strings[0] != "a" {
		t.Fatalf("array mutation leaked: %+v", second.Attributes["list"])
	}
}

// Verifies the no-op context admits callbacks and records nothing.
func TestNoopTelemetryContext(t *testing.T) {
	calls := 0
	err := NOOP_TELEMETRY_CONTEXT.StartSpan(context.Background(), SpanOptions{Name: "noop"}, func(_ context.Context, span TelemetrySpan) error {
		calls++
		span.AddEvent("event", SpanAttributes{"x": NumberAttribute(1)})
		span.SetAttributes(SpanAttributes{"x": NumberAttribute(1)})
		span.SetStatus(StatusErrorBare())
		return span.StartSpan(context.Background(), SpanOptions{Name: "child"}, func(context.Context, TelemetrySpan) error {
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d; want 1", calls)
	}

	expected := errors.New("noop failure")
	err = NOOP_TELEMETRY_CONTEXT.StartSpan(context.Background(), SpanOptions{Name: "noop"}, func(context.Context, TelemetrySpan) error {
		return expected
	})
	if !errors.Is(err, expected) {
		t.Fatalf("error = %v; want the callback failure", err)
	}
}

// Verifies schema-driven attribute inference and the typed span starter.
func TestTelemetrySchemaHelpers(t *testing.T) {
	schema := DefineTelemetrySchema(TelemetrySchemaDefinition{
		Version: 1,
		Spans: map[string]TelemetrySpanDefinition{
			"pi.ai.request": {
				Description: "AI request",
				Parents:     ParentRootOrExternal(),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.ai.operation": {TelemetryAttributeDefinition: TelemetryAttributeDefinition{Type: AttributeTypeString}, Required: true},
					"pi.ai.deferred":  {TelemetryAttributeDefinition: TelemetryAttributeDefinition{Type: AttributeTypeBoolean}, Required: false},
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{
					"pi.ai.response.stop_reason": {Type: AttributeTypeString},
				},
				Events: map[string]TelemetryEventDefinition{
					"chunk": {
						Description: "chunk",
						Attributes: map[string]TelemetryEventAttributeDefinition{
							"pi.ai.chunk.index": {TelemetryAttributeDefinition: TelemetryAttributeDefinition{Type: AttributeTypeNumber}, Required: true},
						},
					},
				},
				Status: TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "pi.ai.failed"},
			},
		},
	})

	start := TelemetrySchemaSpanStartAttributes(schema, "pi.ai.request")
	if _, ok := start["pi.ai.operation"]; !ok {
		t.Fatalf("required start attribute missing: %+v", start)
	}
	if _, ok := start["pi.ai.deferred"]; ok {
		t.Fatalf("optional start attribute treated as required: %+v", start)
	}

	end := TelemetrySchemaSpanEndAttributes(schema, "pi.ai.request")
	if _, ok := end["pi.ai.response.stop_reason"]; !ok {
		t.Fatalf("end attributes = %+v", end)
	}

	event := TelemetrySchemaSpanEventAttributes(schema, "pi.ai.request", "chunk")
	if _, ok := event["pi.ai.chunk.index"]; !ok {
		t.Fatalf("event attributes = %+v", event)
	}
	if TelemetrySchemaSpanStartAttributes(schema, "missing") != nil {
		t.Fatal("unknown span should resolve to nil attributes")
	}

	telemetryContext := NewInMemoryTelemetryContext()
	starter := CreateTypedSpanStarter(telemetryContext, []TelemetrySchemaDefinition{schema})
	err := starter(context.Background(), "pi.ai.request", SpanAttributes{"pi.ai.operation": StringAttribute("stream")},
		func(ctx context.Context, span TelemetrySpan, startChild TypedSpanStarter) error {
			span.AddEvent("chunk", SpanAttributes{"pi.ai.chunk.index": NumberAttribute(1)})
			span.SetAttributes(SpanAttributes{"pi.ai.response.stop_reason": StringAttribute("stop")})
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	spans := telemetryContext.GetSpans()
	if len(spans) != 1 || len(spans[0].Events) != 1 {
		t.Fatalf("typed starter recorded %+v", spans)
	}
	if spans[0].Attributes["pi.ai.response.stop_reason"].String == nil {
		t.Fatalf("typed starter attributes = %+v", spans[0].Attributes)
	}
}

// Verifies the attribute value JSON shapes survive a round trip.
func TestAttributeValueJSON(t *testing.T) {
	cases := map[string]AttributeValue{
		`"text"`:       StringAttribute("text"),
		`0`:            NumberAttribute(0),
		`false`:        BoolAttribute(false),
		`["a","b"]`:    StringsAttribute([]string{"a", "b"}),
		`[1,2]`:        NumbersAttribute([]float64{1, 2}),
		`[true,false]`: BoolsAttribute([]bool{true, false}),
	}
	for raw, want := range cases {
		var decoded AttributeValue
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		data, err := decoded.MarshalJSON()
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if strings.TrimSpace(string(data)) != raw {
			t.Fatalf("round trip = %s; want %s", data, raw)
		}
		if decoded.Type != want.Type {
			t.Fatalf("%s type = %s; want %s", raw, decoded.Type, want.Type)
		}
	}
}

// Verifies concurrent recording does not lose spans or corrupt end sequences.
func TestInMemoryTelemetryConcurrentSpans(t *testing.T) {
	telemetryContext := NewInMemoryTelemetryContext()
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = telemetryContext.StartSpan(ctx, SpanOptions{Name: "concurrent"}, func(context.Context, TelemetrySpan) error {
				return nil
			})
		}()
	}
	wg.Wait()

	spans := telemetryContext.GetSpans()
	if len(spans) != 50 {
		t.Fatalf("spans = %d; want 50", len(spans))
	}
	seenIds := map[int]bool{}
	seenSequences := map[int]bool{}
	for _, span := range spans {
		if seenIds[span.Id] {
			t.Fatalf("duplicate span id %d", span.Id)
		}
		seenIds[span.Id] = true
		if span.EndSequence == nil {
			t.Fatalf("span %d has no end sequence", span.Id)
		}
		if seenSequences[*span.EndSequence] {
			t.Fatalf("duplicate end sequence %d", *span.EndSequence)
		}
		seenSequences[*span.EndSequence] = true
	}
}
