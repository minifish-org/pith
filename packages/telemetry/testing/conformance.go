// Runner-independent conformance cases for the callback telemetry adapter
// contract.
//
// This is a Go port of packages/telemetry/src/testing/conformance.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The upstream suite relies on JavaScript Proxy objects that throw on every
// property access to prove that telemetry recording is passive. Go has no
// equivalent transparent proxy, so the same guarantee is expressed by
// UnreadableAttributeValue: a value whose every read reports failure. The
// adapter must ignore it and keep the request alive.
package testing

import (
	"context"
	"errors"
	"fmt"

	telemetry "github.com/minifish-org/pith/packages/telemetry"
)

type conformanceTest = func(ctx context.Context, fixture TelemetryAdapterFixture) error

func createCase(factory TelemetryAdapterFixtureFactory, group, name string, test conformanceTest) TelemetryAdapterConformanceCase {
	return TelemetryAdapterConformanceCase{
		Group: group,
		Name:  name,
		Run: func(ctx context.Context) error {
			fixture, err := factory(ctx)
			if err != nil {
				return err
			}
			defer func() {
				_ = fixture.Close(ctx)
			}()
			return test(ctx, fixture)
		},
	}
}

func findSpan(spans []telemetry.RecordedTelemetrySpan, name string) (telemetry.RecordedTelemetrySpan, error) {
	for _, candidate := range spans {
		if candidate.Name == name {
			return candidate, nil
		}
	}
	return telemetry.RecordedTelemetrySpan{}, &ConformanceAssertionFailed{Message: fmt.Sprintf("Expected recorded span %s", name)}
}

func require(condition bool, message string) error {
	if !condition {
		return &ConformanceAssertionFailed{Message: message}
	}
	return nil
}

func requireEqual[T comparable](got, want T, message string) error {
	if got != want {
		return &ConformanceAssertionFailed{Message: fmt.Sprintf("%s: got %v, want %v", message, got, want)}
	}
	return nil
}

func requireMapsEqual(got, want telemetry.SpanAttributes, message string) error {
	normalized := func(attributes telemetry.SpanAttributes) telemetry.SpanAttributes {
		if attributes == nil {
			return telemetry.SpanAttributes{}
		}
		return attributes
	}
	got, want = normalized(got), normalized(want)
	if len(got) != len(want) {
		return &ConformanceAssertionFailed{Message: fmt.Sprintf("%s: got %v, want %v", message, got, want)}
	}
	for key, wantValue := range want {
		gotValue, ok := got[key]
		if !ok {
			return &ConformanceAssertionFailed{Message: fmt.Sprintf("%s: missing key %s", message, key)}
		}
		gotJSON, gotErr := gotValue.MarshalJSON()
		wantJSON, wantErr := wantValue.MarshalJSON()
		if gotErr != nil || wantErr != nil || string(gotJSON) != string(wantJSON) {
			return &ConformanceAssertionFailed{Message: fmt.Sprintf("%s: key %s got %s, want %s", message, key, gotJSON, wantJSON)}
		}
	}
	return nil
}

func requireEventsEqual(got, want []telemetry.RecordedTelemetryEvent, message string) error {
	if len(got) != len(want) {
		return &ConformanceAssertionFailed{Message: fmt.Sprintf("%s: got %d events, want %d", message, len(got), len(want))}
	}
	for i := range want {
		if got[i].Name != want[i].Name {
			return &ConformanceAssertionFailed{Message: fmt.Sprintf("%s: event %d named %s, want %s", message, i, got[i].Name, want[i].Name)}
		}
		if err := requireMapsEqual(got[i].Attributes, want[i].Attributes, message); err != nil {
			return err
		}
	}
	return nil
}

func requireSameError(got, want error) error {
	if !errors.Is(got, want) {
		return &ConformanceAssertionFailed{Message: fmt.Sprintf("operation rejected with %v, want %v", got, want)}
	}
	return nil
}

func requireRejected(got error, message string) error {
	if got == nil {
		return &ConformanceAssertionFailed{Message: message}
	}
	return nil
}

// UnreadableAttributeValue marks an attribute value whose reads must fail. Any
// adapter that reads it must treat the whole payload as unreadable and record
// nothing from it, rather than surfacing an error to the caller.
func UnreadableAttributeValue() telemetry.AttributeValue {
	return telemetry.UnreadableValue()
}

// UnreadableAttributes builds an attribute map whose value cannot be read.
func UnreadableAttributes() telemetry.SpanAttributes {
	return telemetry.SpanAttributes{"unreadable": UnreadableAttributeValue()}
}

// CreateTelemetryAdapterConformance creates runner-independent cases for the
// callback telemetry adapter contract.
func CreateTelemetryAdapterConformance(factory TelemetryAdapterFixtureFactory) []TelemetryAdapterConformanceCase {
	return []TelemetryAdapterConformanceCase{
		createCase(factory, "callback lifecycle", "admits once synchronously and preserves the result", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			admitted := false
			calls := 0
			expected := 42
			var result int
			err := fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "success"},
				func(context.Context, telemetry.TelemetrySpan) error {
					admitted = true
					calls++
					result = expected
					return nil
				})
			if err != nil {
				return err
			}
			if err := requireEqual(admitted, true, "callback admitted synchronously"); err != nil {
				return err
			}
			if err := requireEqual(calls, 1, "callback call count"); err != nil {
				return err
			}
			if err := requireEqual(result, expected, "callback result"); err != nil {
				return err
			}
			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			span, err := findSpan(spans, "success")
			if err != nil {
				return err
			}
			if err := requireEqual(span.Status.Status, "ok", "success status"); err != nil {
				return err
			}
			return requireEqual(span.Settled, true, "success settled")
		}),

		createCase(factory, "callback lifecycle", "preserves synchronous and asynchronous rejection values", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			syncError := errors.New("sync")
			if err := requireSameError(fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "sync-error"},
				func(context.Context, telemetry.TelemetrySpan) error { return syncError }), syncError); err != nil {
				return err
			}

			asyncError := errors.New("async")
			if err := requireSameError(fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "async-error"},
				func(context.Context, telemetry.TelemetrySpan) error { return asyncError }), asyncError); err != nil {
				return err
			}

			if err := requireRejected(fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "nil-error"},
				func(context.Context, telemetry.TelemetrySpan) error { return context.Canceled }), "expected rejection"); err != nil {
				return err
			}

			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			for _, name := range []string{"sync-error", "async-error", "nil-error"} {
				span, err := findSpan(spans, name)
				if err != nil {
					return err
				}
				if err := requireEqual(span.Status.Status, "error", name+" status"); err != nil {
					return err
				}
			}
			return nil
		}),

		createCase(factory, "status", "uses last explicit status without automatic overwrite", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			err := fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "last-status"},
				func(_ context.Context, span telemetry.TelemetrySpan) error {
					span.SetStatus(telemetry.StatusError("Expected", "first"))
					span.SetStatus(telemetry.StatusOK())
					return nil
				})
			if err != nil {
				return err
			}

			thrown := errors.New("after explicit status")
			if err := requireSameError(fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "explicit-before-throw"},
				func(_ context.Context, span telemetry.TelemetrySpan) error {
					span.SetStatus(telemetry.StatusOK())
					return thrown
				}), thrown); err != nil {
				return err
			}

			rejected := errors.New("after async explicit status")
			if err := requireSameError(fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "explicit-before-rejection"},
				func(_ context.Context, span telemetry.TelemetrySpan) error {
					span.SetStatus(telemetry.StatusError("Expected", "async failure"))
					return rejected
				}), rejected); err != nil {
				return err
			}

			err = fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "expected-failure"},
				func(_ context.Context, span telemetry.TelemetrySpan) error {
					span.SetStatus(telemetry.StatusError("Expected", "returned failure"))
					return nil
				})
			if err != nil {
				return err
			}

			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			expectations := map[string]telemetry.SpanStatus{
				"last-status":               telemetry.StatusOK(),
				"explicit-before-throw":     telemetry.StatusOK(),
				"explicit-before-rejection": telemetry.StatusError("Expected", "async failure"),
				"expected-failure":          telemetry.StatusError("Expected", "returned failure"),
			}
			for name, want := range expectations {
				span, err := findSpan(spans, name)
				if err != nil {
					return err
				}
				if span.Status.Status != want.Status {
					return &ConformanceAssertionFailed{Message: fmt.Sprintf("%s status = %s, want %s", name, span.Status.Status, want.Status)}
				}
				if want.Error == nil {
					continue
				}
				if span.Status.Error == nil || span.Status.Error.Name != want.Error.Name || span.Status.Error.Message != want.Error.Message {
					return &ConformanceAssertionFailed{Message: fmt.Sprintf("%s error = %+v, want %+v", name, span.Status.Error, want.Error)}
				}
			}
			return nil
		}),

		createCase(factory, "recording", "merges attributes and records ordered events", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			err := fixture.Context().StartSpan(ctx, telemetry.SpanOptions{
				Name: "recording",
				Attributes: telemetry.SpanAttributes{
					"start":     telemetry.StringAttribute("value"),
					"overwrite": telemetry.StringAttribute("start"),
					"ignored":   {},
				},
			}, func(_ context.Context, span telemetry.TelemetrySpan) error {
				span.SetAttributes(telemetry.SpanAttributes{
					"count":     telemetry.NumberAttribute(1),
					"overwrite": telemetry.StringAttribute("middle"),
				})
				span.SetAttributes(telemetry.SpanAttributes{
					"count":     {},
					"overwrite": telemetry.StringAttribute("end"),
				})
				span.AddEvent("first", telemetry.SpanAttributes{"index": telemetry.NumberAttribute(1), "ignored": {}})
				span.AddEvent("second", telemetry.SpanAttributes{"index": telemetry.NumberAttribute(2)})
				return nil
			})
			if err != nil {
				return err
			}

			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			span, err := findSpan(spans, "recording")
			if err != nil {
				return err
			}
			if err := requireMapsEqual(span.Attributes, telemetry.SpanAttributes{
				"start":     telemetry.StringAttribute("value"),
				"overwrite": telemetry.StringAttribute("end"),
				"count":     telemetry.NumberAttribute(1),
			}, "recording attributes"); err != nil {
				return err
			}
			return requireEventsEqual(span.Events, []telemetry.RecordedTelemetryEvent{
				{Name: "first", Attributes: telemetry.SpanAttributes{"index": telemetry.NumberAttribute(1)}},
				{Name: "second", Attributes: telemetry.SpanAttributes{"index": telemetry.NumberAttribute(2)}},
			}, "recording events")
		}),

		createCase(factory, "recording", "ignores failed attribute calls atomically", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			err := fixture.Context().StartSpan(ctx, telemetry.SpanOptions{
				Name:       "atomic-attributes",
				Attributes: telemetry.SpanAttributes{"retained": telemetry.StringAttribute("value")},
			}, func(_ context.Context, span telemetry.TelemetrySpan) error {
				span.SetAttributes(telemetry.SpanAttributes{
					"partial":    telemetry.StringAttribute("must not survive"),
					"unreadable": UnreadableAttributeValue(),
				})
				return nil
			})
			if err != nil {
				return err
			}

			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			span, err := findSpan(spans, "atomic-attributes")
			if err != nil {
				return err
			}
			return requireMapsEqual(span.Attributes, telemetry.SpanAttributes{
				"retained": telemetry.StringAttribute("value"),
			}, "atomic attributes")
		}),

		createCase(factory, "recording", "makes calls after settlement inert", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			var settledSpan telemetry.TelemetrySpan
			err := fixture.Context().StartSpan(ctx, telemetry.SpanOptions{
				Name:       "settled",
				Attributes: telemetry.SpanAttributes{"value": telemetry.StringAttribute("initial")},
			}, func(_ context.Context, span telemetry.TelemetrySpan) error {
				settledSpan = span
				return nil
			})
			if err != nil {
				return err
			}
			if settledSpan == nil {
				return &ConformanceAssertionFailed{Message: "Expected callback span"}
			}

			settledSpan.SetAttributes(telemetry.SpanAttributes{"value": telemetry.StringAttribute("late")})
			settledSpan.AddEvent("late", telemetry.SpanAttributes{"value": telemetry.BoolAttribute(true)})
			settledSpan.SetStatus(telemetry.StatusErrorBare())

			childAdmitted := false
			childResult := 0
			if err := settledSpan.StartSpan(ctx, telemetry.SpanOptions{Name: "late-child"},
				func(context.Context, telemetry.TelemetrySpan) error {
					childAdmitted = true
					childResult = 7
					return nil
				}); err != nil {
				return err
			}
			if err := requireEqual(childAdmitted, true, "late child admitted"); err != nil {
				return err
			}
			if err := requireEqual(childResult, 7, "late child result"); err != nil {
				return err
			}

			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			if err := requireEqual(len(spans), 1, "settled span count"); err != nil {
				return err
			}
			if err := requireMapsEqual(spans[0].Attributes, telemetry.SpanAttributes{"value": telemetry.StringAttribute("initial")}, "settled attributes"); err != nil {
				return err
			}
			if err := requireEventsEqual(spans[0].Events, nil, "settled events"); err != nil {
				return err
			}
			return requireEqual(spans[0].Status.Status, "ok", "settled status")
		}),

		createCase(factory, "parentage", "records nested and concurrent child relationships", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			releaseFirst := make(chan struct{})
			firstDone := make(chan error, 1)

			err := fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "parent"},
				func(parentCtx context.Context, parent telemetry.TelemetrySpan) error {
					go func() {
						firstDone <- parent.StartSpan(parentCtx, telemetry.SpanOptions{Name: "first-child"},
							func(context.Context, telemetry.TelemetrySpan) error {
								<-releaseFirst
								return nil
							})
					}()
					secondDone := false
					if err := parent.StartSpan(parentCtx, telemetry.SpanOptions{Name: "second-child"},
						func(context.Context, telemetry.TelemetrySpan) error {
							secondDone = true
							return nil
						}); err != nil {
						return err
					}
					if err := requireEqual(secondDone, true, "second child admitted"); err != nil {
						return err
					}
					close(releaseFirst)
					return <-firstDone
				})
			if err != nil {
				return err
			}

			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			parent, err := findSpan(spans, "parent")
			if err != nil {
				return err
			}
			first, err := findSpan(spans, "first-child")
			if err != nil {
				return err
			}
			second, err := findSpan(spans, "second-child")
			if err != nil {
				return err
			}
			if err := requireEqual(parent.ParentId == nil, true, "parent has no parent"); err != nil {
				return err
			}
			if err := requireEqual(first.ParentId != nil && *first.ParentId == parent.Id, true, "first child parent"); err != nil {
				return err
			}
			if err := requireEqual(second.ParentId != nil && *second.ParentId == parent.Id, true, "second child parent"); err != nil {
				return err
			}
			if parent.EndSequence == nil || first.EndSequence == nil || second.EndSequence == nil {
				return &ConformanceAssertionFailed{Message: "expected end sequences"}
			}
			if err := requireEqual(*second.EndSequence < *first.EndSequence, true, "second child settles before first"); err != nil {
				return err
			}
			return requireEqual(*first.EndSequence < *parent.EndSequence, true, "children settle before parent")
		}),

		createCase(factory, "passivity", "suppresses unreadable telemetry payload failures", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			calls := 0
			result := 0
			err := fixture.Context().StartSpan(ctx, telemetry.SpanOptions{
				Name:       "unreadable-options",
				Attributes: UnreadableAttributes(),
			}, func(context.Context, telemetry.TelemetrySpan) error {
				calls++
				result = 9
				return nil
			})
			if err != nil {
				return err
			}
			if err := requireEqual(calls, 1, "unreadable options callback"); err != nil {
				return err
			}
			if err := requireEqual(result, 9, "unreadable options result"); err != nil {
				return err
			}

			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			if err := requireEqual(len(spans), 0, "unreadable options recorded spans"); err != nil {
				return err
			}

			err = fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "unreadable-recording"},
				func(_ context.Context, span telemetry.TelemetrySpan) error {
					span.SetAttributes(UnreadableAttributes())
					span.AddEvent("unreadable-event", UnreadableAttributes())
					span.SetStatus(telemetry.SpanStatus{Status: "unreadable"})
					return nil
				})
			if err != nil {
				return err
			}

			recorded, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			if err := requireEqual(len(recorded), 1, "unreadable recording span count"); err != nil {
				return err
			}
			if err := requireMapsEqual(recorded[0].Attributes, telemetry.SpanAttributes{}, "unreadable recording attributes"); err != nil {
				return err
			}
			if err := requireEventsEqual(recorded[0].Events, nil, "unreadable recording events"); err != nil {
				return err
			}
			return requireEqual(recorded[0].Status.Status, "ok", "unreadable recording status")
		}),

		createCase(factory, "passivity", "ignores failed status calls atomically", func(ctx context.Context, fixture TelemetryAdapterFixture) error {
			rejection := errors.New("rejected after unreadable status")
			if err := requireSameError(fixture.Context().StartSpan(ctx, telemetry.SpanOptions{Name: "unreadable-status"},
				func(_ context.Context, span telemetry.TelemetrySpan) error {
					span.SetStatus(telemetry.SpanStatus{Status: "unreadable"})
					return rejection
				}), rejection); err != nil {
				return err
			}

			spans, err := fixture.GetSpans(ctx)
			if err != nil {
				return err
			}
			span, err := findSpan(spans, "unreadable-status")
			if err != nil {
				return err
			}
			return requireEqual(span.Status.Status, "error", "unreadable status")
		}),
	}
}
