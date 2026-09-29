package testing

import (
	"context"
	"strings"
	"testing"

	telemetry "github.com/minifish-org/pith/packages/telemetry"
)

// Verifies the conformance suite covers the documented groups and that the
// reference in-memory fixture satisfies every case.
func TestTelemetryAdapterConformanceCases(t *testing.T) {
	ctx := context.Background()
	cases := CreateTelemetryAdapterConformance(ReferenceTelemetryFixtureFactory)
	if len(cases) == 0 {
		t.Fatal("no conformance cases")
	}

	groups := map[string]int{}
	for _, testCase := range cases {
		if testCase.Group == "" || testCase.Name == "" || testCase.Run == nil {
			t.Fatalf("incomplete conformance case: %+v", testCase)
		}
		groups[testCase.Group]++
	}
	for _, group := range []string{"callback lifecycle", "status", "recording", "parentage", "passivity"} {
		if groups[group] == 0 {
			t.Fatalf("missing conformance group %q", group)
		}
	}

	for _, testCase := range cases {
		testCase := testCase
		if err := testCase.Run(ctx); err != nil {
			t.Fatalf("%s / %s: %v", testCase.Group, testCase.Name, err)
		}
	}
}

// Verifies the reference fixture is isolated per case and exposes the recorded
// spans through the adapter interface.
func TestReferenceTelemetryFixture(t *testing.T) {
	ctx := context.Background()
	fixture, err := ReferenceTelemetryFixtureFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close(ctx)

	if err := fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "probe"},
		func(context.Context, telemetry.TelemetrySpan) error { return nil }); err != nil {
		t.Fatal(err)
	}
	spans, err := fixture.GetSpans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 1 || spans[0].Name != "probe" {
		t.Fatalf("spans = %+v", spans)
	}

	other, err := ReferenceTelemetryFixtureFactory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	otherSpans, err := other.GetSpans(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherSpans) != 0 {
		t.Fatalf("fixtures are not isolated: %+v", otherSpans)
	}
}

// Verifies the unreadable-payload helpers describe exactly what the contract
// requires: a value whose read fails, so nothing from the payload is recorded.
func TestUnreadablePayloadHelpers(t *testing.T) {
	value := UnreadableAttributeValue()
	if value.Type != "unreadable" {
		t.Fatalf("value = %+v", value)
	}
	attributes := UnreadableAttributes()
	if len(attributes) != 1 {
		t.Fatalf("attributes = %+v", attributes)
	}
	if _, ok := attributes["unreadable"]; !ok {
		t.Fatalf("attributes = %+v", attributes)
	}
}

// Verifies a broken adapter fixture is reported instead of silently passing.
func TestConformanceReportsAdapterFailure(t *testing.T) {
	ctx := context.Background()
	failing := func(context.Context) (TelemetryAdapterFixture, error) {
		return &failingFixture{}, nil
	}
	cases := CreateTelemetryAdapterConformance(failing)
	var failures int
	for _, testCase := range cases {
		if err := testCase.Run(ctx); err != nil {
			failures++
		}
	}
	if failures == 0 {
		t.Fatal("expected the broken adapter to fail at least one case")
	}
}

// failingFixture records nothing and never settles spans, so every conformance
// case that reads recorded state must fail.
type failingFixture struct{}

func (f *failingFixture) Context() telemetry.TelemetryContext { return f }

func (f *failingFixture) StartSpan(ctx context.Context, _ telemetry.SpanOptions, _ telemetry.SpanCallback) error {
	return nil
}

func (f *failingFixture) GetSpans(context.Context) ([]telemetry.RecordedTelemetrySpan, error) {
	return nil, nil
}

func (f *failingFixture) Close(context.Context) error { return nil }

// Verifies the assertion helper produces a readable failure message.
func TestConformanceAssertionMessage(t *testing.T) {
	err := requireEqual(1, 2, "count")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "count") {
		t.Fatalf("message = %q", err.Error())
	}
	var typed *ConformanceAssertionFailed
	if !asConformanceFailure(err, &typed) {
		t.Fatal("expected a ConformanceAssertionFailed")
	}
}

func asConformanceFailure(err error, target **ConformanceAssertionFailed) bool {
	typed, ok := err.(*ConformanceAssertionFailed)
	if !ok {
		return false
	}
	*target = typed
	return true
}
