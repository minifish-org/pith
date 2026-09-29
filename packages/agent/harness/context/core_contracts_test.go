package harnesscontext

import (
	"fmt"
	"testing"

	"github.com/minifish-org/pith/packages/telemetry"
)

func TestHarnessContextReExportsChordBehavior(t *testing.T) {
	key := CreateContextKey[string]("harness.example")
	ctx := WithContextValue(key, "value", BackgroundContext)
	if value, ok := Value(ctx, key); !ok || value != "value" {
		t.Fatalf("value = %q/%v", value, ok)
	}
	if fmt.Sprint(BackgroundContext) == fmt.Sprint(TodoContext) {
		t.Fatal("background and todo contexts must stay distinguishable")
	}
}

func TestTelemetryContextDefaultsAndOverride(t *testing.T) {
	if GetTelemetryContext(BackgroundContext) != telemetry.NOOP_TELEMETRY_CONTEXT {
		t.Fatal("expected the shared no-op telemetry context")
	}
	memory := telemetry.NewInMemoryTelemetryContext()
	ctx := WithTelemetryContext(memory, BackgroundContext)
	if GetTelemetryContext(ctx) != telemetry.TelemetryContext(memory) {
		t.Fatal("telemetry context override was not retained")
	}
}
