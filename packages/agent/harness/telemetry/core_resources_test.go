package harnesstelemetry

import (
	"context"
	"testing"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	telemetry "github.com/minifish-org/pith/packages/telemetry"
)

func TestSchemasPresent(t *testing.T) {
	if AITelemetrySchema.Version != 1 || HarnessTelemetrySchema.Version != 1 {
		t.Fatalf("schema version mismatch: %d %d", AITelemetrySchema.Version, HarnessTelemetrySchema.Version)
	}
	if _, ok := AITelemetrySchema.Spans["pi.ai.request"]; !ok {
		t.Fatal("missing pi.ai.request span")
	}
	if _, ok := HarnessTelemetrySchema.Spans["pi.harness.run"]; !ok {
		t.Fatal("missing pi.harness.run span")
	}
	if len(AgentTelemetrySchemas) != 2 {
		t.Fatalf("combined schema count = %d", len(AgentTelemetrySchemas))
	}
}

func TestSchemaAttributeLookup(t *testing.T) {
	start := TelemetrySchemaSpanStartAttributes(HarnessTelemetrySchema, "pi.harness.run")
	if _, ok := start["pi.operation.kind"]; !ok {
		t.Fatalf("required start attribute missing: %#v", start)
	}
	end := TelemetrySchemaSpanEndAttributes(HarnessTelemetrySchema, "pi.harness.tool")
	if _, ok := end["pi.tool.is_error"]; !ok {
		t.Fatalf("end attribute missing: %#v", end)
	}
}

func TestStartHarnessSpanRecordsAttributes(t *testing.T) {
	parent := telemetry.NewInMemoryTelemetryContext()
	ctx := harnesscontext.WithTelemetryContext(parent, context.Background())
	attributes := SpanAttributes{
		"pi.lane.name":    telemetry.StringAttribute("main"),
		"pi.operation.id": telemetry.StringAttribute("op-1"),
		"pi.hook.name":    telemetry.StringAttribute("before_tool"),
	}
	value, err := StartHarnessSpan(ctx, "pi.harness.hook", attributes,
		func(spanContext harnesscontext.Context, span HarnessTelemetrySpan) (int, error) {
			span.SetAttributes(SpanAttributes{"pi.hook.outcome": telemetry.StringAttribute("completed")})
			return 42, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if value != 42 {
		t.Fatalf("callback result = %d", value)
	}
	spans := parent.GetSpans()
	if len(spans) != 1 || spans[0].Name != "pi.harness.hook" {
		t.Fatalf("unexpected spans: %#v", spans)
	}
	if spans[0].Attributes["pi.lane.name"].String == nil || *spans[0].Attributes["pi.lane.name"].String != "main" {
		t.Fatalf("start attributes not recorded: %#v", spans[0].Attributes)
	}
	if _, ok := spans[0].Attributes["pi.hook.outcome"]; !ok {
		t.Fatalf("end attributes not recorded: %#v", spans[0].Attributes)
	}
}
