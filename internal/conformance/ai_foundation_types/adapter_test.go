package conformance

import (
	"context"
	"encoding/json"
	"fmt"

	aitests "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
	telemetry "github.com/minifish-org/pith/packages/telemetry"
	telemetrytesting "github.com/minifish-org/pith/packages/telemetry/testing"
)

// RunCase is the only bridge the frozen judge uses. It translates one input
// operation into calls against the real exported Go SDK and returns the
// normalized result. It never reads expected results, golden files or TS
// sources, and it does not implement SDK behavior itself.
//
// The supported operations are the ones the ai-foundation-types batch defines:
// telemetry (the in-memory reference implementation) and assistant-stream (the
// assistant message event stream). Any other operation is reported as an error
// rather than silently succeeding.
func RunCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var envelope struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(input, &envelope); err != nil {
		return nil, fmt.Errorf("conformance: invalid input: %w", err)
	}

	switch envelope.Op {
	case "telemetry":
		return runTelemetryCase(ctx, input)
	case "assistant-stream":
		return runAssistantStreamCase(ctx, input)
	default:
		return nil, fmt.Errorf("conformance: unsupported operation %q", envelope.Op)
	}
}

// runTelemetryCase replays the shared telemetry fixture shape through the real
// in-memory telemetry context and returns the recorded span snapshots.
func runTelemetryCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Fail bool `json:"fail"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("conformance: invalid telemetry input: %w", err)
	}

	telemetryContext := telemetry.NewInMemoryTelemetryContext()

	// A nested parent/child span with a recorded event. The callback failure is
	// observed exactly like upstream: the error settles both spans.
	_ = telemetryContext.StartSpan(ctx,
		telemetry.SpanOptions{
			Name: "parent",
			Attributes: telemetry.SpanAttributes{
				"empty": telemetry.StringAttribute(""),
				"zero":  telemetry.NumberAttribute(0),
				"flag":  telemetry.BoolAttribute(false),
			},
		},
		func(spanCtx context.Context, span telemetry.TelemetrySpan) error {
			span.AddEvent("begin", telemetry.SpanAttributes{
				"values": telemetry.NumbersAttribute([]float64{1, 2}),
			})
			return span.StartSpan(spanCtx, telemetry.SpanOptions{Name: "child"},
				func(context.Context, telemetry.TelemetrySpan) error {
					if request.Fail {
						return fmt.Errorf("fixture error")
					}
					return nil
				})
		})

	return encodeSpans(telemetryContext.GetSpans())
}

// encodeSpans converts the recorded span snapshots into the JSON shape the
// evaluator compares against: the recorded fields only, with error names
// normalized to the JavaScript Error convention.
func encodeSpans(spans []telemetry.RecordedTelemetrySpan) (json.RawMessage, error) {
	out := make([]map[string]any, 0, len(spans))
	for _, span := range spans {
		attributes := make(map[string]any, len(span.Attributes))
		for name, value := range span.Attributes {
			attributes[name] = attributeJSON(value)
		}

		events := make([]map[string]any, 0, len(span.Events))
		for _, event := range span.Events {
			eventAttributes := make(map[string]any, len(event.Attributes))
			for name, value := range event.Attributes {
				eventAttributes[name] = attributeJSON(value)
			}
			events = append(events, map[string]any{
				"name":       event.Name,
				"attributes": eventAttributes,
			})
		}

		status := map[string]any{"status": span.Status.Status}
		if span.Status.Error != nil {
			// Go error values have no JavaScript Error subclass here, so the
			// recorded name is normalized to the JS convention. The message is
			// preserved verbatim.
			name := span.Status.Error.Name
			if name == "" || name == "Error" || name == "*errors.errorString" {
				name = "Error"
			}
			status["error"] = map[string]any{"name": name, "message": span.Status.Error.Message}
		}

		parentId := any(nil)
		if span.ParentId != nil {
			parentId = *span.ParentId
		}

		entry := map[string]any{
			"id":         span.Id,
			"parentId":   parentId,
			"name":       span.Name,
			"attributes": attributes,
			"events":     events,
			"status":     status,
			"settled":    span.Settled,
		}
		if span.EndSequence != nil {
			entry["endSequence"] = *span.EndSequence
		}
		out = append(out, entry)
	}
	return json.Marshal(out)
}

// attributeJSON renders an attribute value in its JSON form: scalars stay
// scalars, arrays stay arrays.
func attributeJSON(value telemetry.AttributeValue) any {
	switch value.Type {
	case telemetry.AttributeTypeString:
		if value.String == nil {
			return nil
		}
		return *value.String
	case telemetry.AttributeTypeNumber:
		if value.Number == nil {
			return nil
		}
		return *value.Number
	case telemetry.AttributeTypeBoolean:
		if value.Bool == nil {
			return nil
		}
		return *value.Bool
	case telemetry.AttributeTypeStringArr:
		if value.Strings == nil {
			return []string{}
		}
		return value.Strings
	case telemetry.AttributeTypeNumberArr:
		if value.Numbers == nil {
			return []float64{}
		}
		return value.Numbers
	case telemetry.AttributeTypeBooleanArr:
		if value.Bools == nil {
			return []bool{}
		}
		return value.Bools
	default:
		return nil
	}
}

// runAssistantStreamCase drives the real assistant message event stream: it
// pushes a start event, then either a done or an error terminal, drains every
// event and reads the stream result.
func runAssistantStreamCase(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Message  json.RawMessage `json:"message"`
		Terminal string          `json:"terminal"`
	}
	if err := json.Unmarshal(input, &request); err != nil {
		return nil, fmt.Errorf("conformance: invalid assistant-stream input: %w", err)
	}

	var partial aitests.AssistantMessage
	if len(request.Message) > 0 {
		if err := json.Unmarshal(request.Message, &partial); err != nil {
			return nil, fmt.Errorf("conformance: invalid assistant message: %w", err)
		}
	}

	stream := aiutils.NewAssistantMessageEventStream()
	stream.Push(aitests.NewStartEvent(partial))

	if request.Terminal == "done" {
		stream.Push(aitests.NewDoneEvent(partial.StopReason, partial))
	} else {
		errorMessage := partial
		errorMessage.StopReason = aitests.StopReasonError
		text := "fixture"
		errorMessage.ErrorMessage = &text
		stream.Push(aitests.NewErrorEvent(aitests.StopReasonError, errorMessage))
	}

	events := []recordedEvent{}
	for {
		item := <-stream.Next()
		if item.Done {
			break
		}
		events = append(events, recordEvent(item.Value))
	}

	result, err := stream.Result(ctx)
	if err != nil {
		return nil, fmt.Errorf("conformance: stream result: %w", err)
	}

	return json.Marshal(map[string]any{
		"events": events,
		"result": result,
	})
}

// recordedEvent is the normalized assistant stream event shape. Undefined
// fields are omitted entirely so they stay absent rather than null.
type recordedEvent map[string]any

func recordEvent(event aitests.AssistantMessageEvent) recordedEvent {
	out := recordedEvent{"type": string(event.Type)}
	if event.ContentIndex != nil {
		out["contentIndex"] = *event.ContentIndex
	}
	if event.Delta != nil {
		out["delta"] = *event.Delta
	}
	if event.ContentBlock != nil {
		out["content"] = *event.ContentBlock
	}
	if event.ToolCall != nil {
		out["toolCall"] = *event.ToolCall
	}
	if event.Partial != nil {
		out["partial"] = *event.Partial
	}
	if event.Message != nil {
		out["message"] = *event.Message
	}
	if event.Reason != nil {
		out["reason"] = string(*event.Reason)
	}
	if event.Error != nil {
		out["error"] = *event.Error
	}
	return out
}

// referenceFixtureFactory is the conformance factory the package ships for
// downstream adapters; it keeps the shared conformance cases runnable without a
// production telemetry backend.
func referenceFixtureFactory(ctx context.Context) (telemetrytesting.TelemetryAdapterFixture, error) {
	return telemetrytesting.ReferenceTelemetryFixtureFactory(ctx)
}
