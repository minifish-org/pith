// Package harnesstelemetry is the Go port of
// packages/agent/src/harness/telemetry.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The package re-exports the provider-neutral telemetry contract and declares
// the agent-owned schema vocabulary: the AI request schema and the harness
// schema. Typed span starters bind a schema to a telemetry context so a span
// started by the harness inherits its parent's child-span vocabulary.
package harnesstelemetry

import (
	"context"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	telemetry "github.com/minifish-org/pith/packages/telemetry"
)

// Re-exported telemetry contract types.
type (
	// AttributeValue is one telemetry attribute value.
	AttributeValue = telemetry.AttributeValue
	// ExactTelemetryAttributes documents attribute maps without extra keys.
	ExactTelemetryAttributes = telemetry.ExactTelemetryAttributes
	// SchemaTelemetrySpan is a span bound to a schema.
	SchemaTelemetrySpan = telemetry.SchemaTelemetrySpan
	// SpanAttributes are the attributes of a span or event.
	SpanAttributes = telemetry.SpanAttributes
	// SpanOptions are the options for starting a span.
	SpanOptions = telemetry.SpanOptions
	// SpanStatus is the settled span status.
	SpanStatus = telemetry.SpanStatus
	// TelemetryAttributeDefinition describes one schema attribute.
	TelemetryAttributeDefinition = telemetry.TelemetryAttributeDefinition
	// TelemetryAttributeMetadata is attribute documentation metadata.
	TelemetryAttributeMetadata = telemetry.TelemetryAttributeMetadata
	// TelemetryAttributeType names an attribute value shape.
	TelemetryAttributeType = telemetry.AttributeValueType
	// TelemetryContext admits callbacks into spans.
	TelemetryContext = telemetry.TelemetryContext
	// TelemetryEventAttributeDefinition describes one event attribute.
	TelemetryEventAttributeDefinition = telemetry.TelemetryEventAttributeDefinition
	// TelemetryEventDefinition describes one span event.
	TelemetryEventDefinition = telemetry.TelemetryEventDefinition
	// TelemetryParentDefinition restricts a span's parents.
	TelemetryParentDefinition = telemetry.TelemetryParentDefinition
	// TelemetrySchemaDefinition is a versioned telemetry schema.
	TelemetrySchemaDefinition = telemetry.TelemetrySchemaDefinition
	// TelemetrySchemaSpanUnion is one member of a schema vocabulary.
	TelemetrySchemaSpanUnion = telemetry.TelemetrySchemaSpanUnion
	// TelemetrySpan is a started span.
	TelemetrySpan = telemetry.TelemetrySpan
	// TelemetrySpanDefinition describes one span.
	TelemetrySpanDefinition = telemetry.TelemetrySpanDefinition
	// TelemetryStartAttributeDefinition describes one start attribute.
	TelemetryStartAttributeDefinition = telemetry.TelemetryStartAttributeDefinition
	// TypedSpanStarter starts schema-typed spans.
	TypedSpanStarter = telemetry.TypedSpanStarter
	// TelemetrySchemaSpanName is a span name from a schema.
	TelemetrySchemaSpanName = telemetry.TelemetrySchemaSpanName
	// TelemetrySchemaSpanEventName is an event name from a schema span.
	TelemetrySchemaSpanEventName = telemetry.TelemetrySchemaSpanEventName
)

// TelemetrySchemaSpanStartAttributes returns the start attributes of a span.
func TelemetrySchemaSpanStartAttributes(schema TelemetrySchemaDefinition, name string) SpanAttributes {
	return telemetry.TelemetrySchemaSpanStartAttributes(schema, name)
}

// TelemetrySchemaSpanEndAttributes returns the end attributes of a span.
func TelemetrySchemaSpanEndAttributes(schema TelemetrySchemaDefinition, name string) SpanAttributes {
	return telemetry.TelemetrySchemaSpanEndAttributes(schema, name)
}

// TelemetrySchemaSpanEventAttributes returns the attributes of a span event.
func TelemetrySchemaSpanEventAttributes(schema TelemetrySchemaDefinition, spanName string, eventName string) SpanAttributes {
	return telemetry.TelemetrySchemaSpanEventAttributes(schema, spanName, eventName)
}

// AiSpanName names a span in the AI telemetry schema.
type AiSpanName = TelemetrySchemaSpanName

// AiSpanStartAttributes are the start attributes of an AI span.
type AiSpanStartAttributes = SpanAttributes

// AiSpanEndAttributes are the end attributes of an AI span.
type AiSpanEndAttributes = SpanAttributes

// AiSpanAttributes are the start and end attributes of an AI span.
type AiSpanAttributes = SpanAttributes

// AiSpanEventName names an event of an AI span.
type AiSpanEventName = TelemetrySchemaSpanEventName

// AiSpanEventAttributes are the attributes of an AI span event.
type AiSpanEventAttributes = SpanAttributes

// AiTelemetrySpan is an AI span bound to the AI schema.
type AiTelemetrySpan = SchemaTelemetrySpan

// AiSpan is one member of the AI span vocabulary.
type AiSpan = TelemetrySchemaSpanUnion

// HarnessSpanName names a span in the harness telemetry schema.
type HarnessSpanName = TelemetrySchemaSpanName

// HarnessSpanStartAttributes are the start attributes of a harness span.
type HarnessSpanStartAttributes = SpanAttributes

// HarnessSpanEndAttributes are the end attributes of a harness span.
type HarnessSpanEndAttributes = SpanAttributes

// HarnessSpanAttributes are the start and end attributes of a harness span.
type HarnessSpanAttributes = SpanAttributes

// HarnessSpanEventName names an event of a harness span.
type HarnessSpanEventName = TelemetrySchemaSpanEventName

// HarnessSpanEventAttributes are the attributes of a harness span event.
type HarnessSpanEventAttributes = SpanAttributes

// HarnessTelemetrySpan is a harness span bound to the harness schema.
type HarnessTelemetrySpan = SchemaTelemetrySpan

// HarnessSpan is one member of the harness span vocabulary.
type HarnessSpan = TelemetrySchemaSpanUnion

// AITelemetrySchema is the agent-owned AI request schema.
var AITelemetrySchema = buildAITelemetrySchema()

// HarnessTelemetrySchema is the agent-owned harness schema.
var HarnessTelemetrySchema = buildHarnessTelemetrySchema()

// AgentTelemetrySchemas is the combined typed span vocabulary.
var AgentTelemetrySchemas = []TelemetrySchemaDefinition{AITelemetrySchema, HarnessTelemetrySchema}

// StartAiSpan starts an AI span and runs callback inside it.
func StartAiSpan[TResult any](
	ctx harnesscontext.Context,
	name string,
	attributes SpanAttributes,
	callback func(spanContext harnesscontext.Context, span AiTelemetrySpan) (TResult, error),
) (TResult, error) {
	return startSchemaSpan(ctx, name, attributes, callback)
}

// StartHarnessSpan starts a harness span and runs callback inside it.
func StartHarnessSpan[TResult any](
	ctx harnesscontext.Context,
	name string,
	attributes SpanAttributes,
	callback func(spanContext harnesscontext.Context, span HarnessTelemetrySpan) (TResult, error),
) (TResult, error) {
	return startSchemaSpan(ctx, name, attributes, callback)
}

func startSchemaSpan[TResult any](
	ctx harnesscontext.Context,
	name string,
	attributes SpanAttributes,
	callback func(spanContext harnesscontext.Context, span SchemaTelemetrySpan) (TResult, error),
) (TResult, error) {
	var result TResult
	parent := harnesscontext.GetTelemetryContext(ctx)
	err := parent.StartSpan(ctx, SpanOptions{Name: name, Attributes: attributes}, func(spanCtx context.Context, span telemetry.TelemetrySpan) error {
		typed := SchemaTelemetrySpan{TelemetrySpan: span}
		value, callbackErr := callback(harnesscontext.WithTelemetryContext(typed, ctx), typed)
		if callbackErr != nil {
			return callbackErr
		}
		result = value
		return nil
	})
	if err != nil {
		var zero TResult
		return zero, err
	}
	return result, nil
}

// --- schema builders ------------------------------------------------------

func startDef(def TelemetryAttributeDefinition, required bool) TelemetryStartAttributeDefinition {
	return TelemetryStartAttributeDefinition{TelemetryAttributeDefinition: def, Required: required}
}

func eventAttr(def TelemetryAttributeDefinition, required bool) TelemetryEventAttributeDefinition {
	return TelemetryEventAttributeDefinition{TelemetryAttributeDefinition: def, Required: required}
}

func stringDef(description string) TelemetryAttributeDefinition {
	return TelemetryAttributeDefinition{
		TelemetryAttributeMetadata: TelemetryAttributeMetadata{Description: description},
		Type:                       telemetry.AttributeTypeString,
	}
}

func numberDef(description string) TelemetryAttributeDefinition {
	return TelemetryAttributeDefinition{
		TelemetryAttributeMetadata: TelemetryAttributeMetadata{Description: description},
		Type:                       telemetry.AttributeTypeNumber,
	}
}

func boolDef(description string) TelemetryAttributeDefinition {
	return TelemetryAttributeDefinition{
		TelemetryAttributeMetadata: TelemetryAttributeMetadata{Description: description},
		Type:                       telemetry.AttributeTypeBoolean,
	}
}

func withCardinality(def TelemetryAttributeDefinition, cardinality string) TelemetryAttributeDefinition {
	def.Cardinality = &cardinality
	return def
}

func withStringValues(def TelemetryAttributeDefinition, values ...string) TelemetryAttributeDefinition {
	def.Values = make([]AttributeValue, 0, len(values))
	for _, value := range values {
		def.Values = append(def.Values, telemetry.StringAttribute(value))
	}
	return def
}

func withStringElements(def TelemetryAttributeDefinition, values ...string) TelemetryAttributeDefinition {
	def.ElementValues = make([]AttributeValue, 0, len(values))
	for _, value := range values {
		def.ElementValues = append(def.ElementValues, telemetry.StringAttribute(value))
	}
	return def
}

var aiHookNames = []string{
	"before_run",
	"before_drive",
	"before_run_end",
	"transform_context",
	"before_request",
	"before_payload",
	"after_response",
	"before_tool",
	"after_tool",
	"before_compaction",
	"before_navigation",
}

var aiEventTypes = []string{
	"run_start",
	"run_resume",
	"run_suspend",
	"operation_abort",
	"run_end",
	"fault",
	"handler_error",
	"turn_start",
	"turn_end",
	"retry_scheduled",
	"retry_start",
	"retry_end",
	"message_start",
	"message_update",
	"message_end",
	"tool_start",
	"tool_update",
	"tool_end",
	"entry_added",
	"queue_update",
	"value_update",
	"config_update",
	"compaction_start",
	"compaction_end",
	"navigation_start",
	"navigation_end",
	"lane_created",
	"usage",
}

func buildAITelemetrySchema() TelemetrySchemaDefinition {
	return TelemetrySchemaDefinition{
		Version: 1,
		Spans: map[string]TelemetrySpanDefinition{
			"pi.ai.request": {
				Description: "One logical request to an AI provider",
				Parents:     telemetry.ParentAny(),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.ai.operation": startDef(withStringValues(stringDef("Logical provider operation"), "stream", "fetch_deferred", "cancel_deferred", "generate_images"), true),
					"pi.ai.provider":  startDef(stringDef("Selected provider id"), true),
					"pi.ai.model":     startDef(stringDef("Requested model id"), true),
					"pi.ai.api":       startDef(stringDef("Provider API id"), true),
					"pi.ai.streaming": startDef(boolDef("Whether this operation returns a stream"), true),
					"pi.ai.deferred":  startDef(boolDef("Whether the operation requests or participates in deferred execution"), false),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{
					"pi.ai.response.model":                stringDef("Concrete response model"),
					"pi.ai.response.id":                   withCardinality(stringDef("Provider response id"), "high"),
					"pi.ai.response.stop_reason":          withStringValues(stringDef("Normalized terminal response reason"), "stop", "length", "tool_use", "error", "aborted", "deferred"),
					"pi.ai.http.status_code":              numberDef("Final HTTP status"),
					"pi.ai.usage.input_tokens":            numberDef("Reported input tokens"),
					"pi.ai.usage.output_tokens":           numberDef("Reported output tokens"),
					"pi.ai.usage.cache_read_tokens":       numberDef("Reported cache-read tokens"),
					"pi.ai.usage.cache_write_tokens":      numberDef("Reported cache-write tokens"),
					"pi.ai.usage.reasoning_tokens":        numberDef("Reported reasoning tokens"),
					"pi.ai.usage.total_tokens":            numberDef("Reported total tokens"),
					"pi.ai.usage.cost":                    numberDef("Reported total cost"),
					"pi.ai.stream.chunk_count":            numberDef("Streamed update chunk count"),
					"pi.ai.stream.time_to_first_chunk_ms": numberDef("Elapsed milliseconds to first update chunk"),
					"pi.ai.error.type":                    withCardinality(stringDef("Provider or transport error class"), "low"),
				},
				Status: telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "The operation throws or returns an error result"},
			},
		},
	}
}

func operationStartAttributes() map[string]TelemetryStartAttributeDefinition {
	return map[string]TelemetryStartAttributeDefinition{
		"pi.session.id":         startDef(withCardinality(stringDef("Session id"), "high"), true),
		"pi.lane.name":          startDef(withCardinality(stringDef("Lane name"), "high"), true),
		"pi.operation.id":       startDef(withCardinality(stringDef("Durable operation id"), "high"), true),
		"pi.operation.recovery": startDef(boolDef("Whether this invocation resumes durable work"), true),
	}
}

func operationErrorAttributes() map[string]TelemetryAttributeDefinition {
	return map[string]TelemetryAttributeDefinition{
		"pi.error.code": withCardinality(stringDef("Stable operation error code"), "low"),
		"pi.error.type": withCardinality(stringDef("Low-cardinality operation error class"), "low"),
	}
}

func buildHarnessTelemetrySchema() TelemetrySchemaDefinition {
	operationOutcome := func(values ...string) map[string]TelemetryAttributeDefinition {
		attrs := operationErrorAttributes()
		attrs["pi.operation.outcome"] = withStringValues(stringDef("Invocation outcome"), values...)
		return attrs
	}
	return TelemetrySchemaDefinition{
		Version: 1,
		Spans: map[string]TelemetrySpanDefinition{
			"pi.harness.run": {
				Description: "One admitted in-process run invocation",
				Parents:     telemetry.ParentRootOrExternal(),
				StartAttributes: func() map[string]TelemetryStartAttributeDefinition {
					attrs := operationStartAttributes()
					attrs["pi.operation.kind"] = startDef(withStringValues(stringDef("Run operation kind"), "run"), true)
					return attrs
				}(),
				EndAttributes: operationOutcome("completed", "aborted", "failed", "suspended"),
				Status:        telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "The run fails or throws"},
			},
			"pi.harness.compaction": {
				Description: "One admitted in-process manual compaction invocation",
				Parents:     telemetry.ParentRootOrExternal(),
				StartAttributes: func() map[string]TelemetryStartAttributeDefinition {
					attrs := operationStartAttributes()
					attrs["pi.operation.kind"] = startDef(withStringValues(stringDef("Compaction operation kind"), "compaction"), true)
					return attrs
				}(),
				EndAttributes: operationOutcome("completed", "declined", "aborted", "failed"),
				Status:        telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "The compaction fails or throws"},
			},
			"pi.harness.navigation": {
				Description: "One admitted in-process navigation invocation",
				Parents:     telemetry.ParentRootOrExternal(),
				StartAttributes: func() map[string]TelemetryStartAttributeDefinition {
					attrs := operationStartAttributes()
					attrs["pi.operation.kind"] = startDef(withStringValues(stringDef("Navigation operation kind"), "navigation"), true)
					return attrs
				}(),
				EndAttributes: operationOutcome("completed", "declined", "aborted", "failed"),
				Status:        telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "The navigation fails or throws"},
			},
			"pi.harness.checkpoint": {
				Description: "One run checkpoint",
				Parents:     telemetry.ParentSpans([]string{"pi.harness.run"}),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.lane.name":       startDef(withCardinality(stringDef("Lane name"), "high"), true),
					"pi.operation.id":    startDef(withCardinality(stringDef("Durable operation id"), "high"), true),
					"pi.checkpoint.kind": startDef(withStringValues(stringDef("Checkpoint purpose"), "normal", "abort_reconcile"), true),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{},
				Status:        telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "Checkpoint work throws"},
			},
			"pi.harness.turn": {
				Description: "One assistant response and its tool batch",
				Parents:     telemetry.ParentSpans([]string{"pi.harness.run"}),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.lane.name":    startDef(withCardinality(stringDef("Lane name"), "high"), true),
					"pi.operation.id": startDef(withCardinality(stringDef("Durable operation id"), "high"), true),
					"pi.turn.id":      startDef(withCardinality(stringDef("Invocation-local turn id"), "high"), true),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{},
				Status:        telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "Turn work throws"},
			},
			"pi.harness.step": {
				Description: "One durable retry attempt",
				Parents:     telemetry.ParentSpans([]string{"pi.harness.turn", "pi.harness.checkpoint", "pi.harness.compaction", "pi.harness.navigation"}),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.lane.name":         startDef(withCardinality(stringDef("Lane name"), "high"), true),
					"pi.operation.id":      startDef(withCardinality(stringDef("Durable operation id"), "high"), true),
					"pi.step.kind":         startDef(withStringValues(stringDef("Retryable step kind"), "assistant", "compaction", "branch_summary"), true),
					"pi.step.attempt":      startDef(numberDef("One-based durable attempt number"), true),
					"pi.compaction.reason": startDef(withStringValues(stringDef("Compaction trigger"), "manual", "threshold", "overflow"), false),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{
					"pi.step.outcome": withStringValues(stringDef("Attempt outcome"), "succeeded", "retry", "failed", "aborted", "deferred", "overflow"),
				},
				Status: telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "The attempt retries, fails, or throws"},
			},
			"pi.harness.tool": {
				Description: "One raw phase-2 tool execution",
				Parents:     telemetry.ParentSpans([]string{"pi.harness.turn", "pi.harness.run"}),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.lane.name":     startDef(withCardinality(stringDef("Lane name"), "high"), true),
					"pi.operation.id":  startDef(withCardinality(stringDef("Durable operation id"), "high"), true),
					"pi.turn.id":       startDef(withCardinality(stringDef("Invocation-local live turn id"), "high"), false),
					"pi.tool.name":     startDef(stringDef("Tool name"), true),
					"pi.tool.call_id":  startDef(withCardinality(stringDef("Tool call id"), "high"), true),
					"pi.tool.replay":   startDef(withStringValues(stringDef("Declared replay policy"), "never", "safe"), true),
					"pi.tool.recovery": startDef(boolDef("Whether this is recovery execution"), true),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{
					"pi.tool.is_error": boolDef("Whether raw phase-2 execution returned an error"),
				},
				Status: telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "Raw phase-2 execution returns an error"},
			},
			"pi.harness.hook": {
				Description: "One registered hook handler invocation",
				Parents:     telemetry.ParentAny(),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.lane.name":            startDef(withCardinality(stringDef("Lane name"), "high"), true),
					"pi.operation.id":         startDef(withCardinality(stringDef("Durable operation id when accepted"), "high"), false),
					"pi.hook.name":            startDef(withStringValues(stringDef("Hook name"), aiHookNames...), true),
					"pi.hook.registration_id": startDef(stringDef("Optional hook registration metadata"), false),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{
					"pi.hook.outcome": withStringValues(stringDef("Handler outcome"), "completed", "skipped", "blocked", "failed"),
				},
				Status: telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "The handler throws"},
			},
			"pi.harness.sleep": {
				Description: "One retry delay",
				Parents:     telemetry.ParentSpans([]string{"pi.harness.run", "pi.harness.compaction", "pi.harness.navigation", "pi.harness.turn", "pi.harness.checkpoint"}),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.operation.id":   startDef(withCardinality(stringDef("Durable operation id"), "high"), true),
					"pi.sleep.delay_ms": startDef(numberDef("Requested delay in milliseconds"), true),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{
					"pi.sleep.outcome": withStringValues(stringDef("Delay outcome"), "elapsed", "aborted"),
				},
				Status: telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "Sleep work throws"},
			},
			"pi.harness.event_handler": {
				Description: "One passive event listener invocation",
				Parents:     telemetry.ParentAny(),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.event.type": startDef(withStringValues(stringDef("Delivered harness event type"), aiEventTypes...), true),
					"pi.lane.name":  startDef(withCardinality(stringDef("Lane name for lane-scoped events"), "high"), false),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{},
				Status:        telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "The listener throws"},
			},
			"pi.session.write": {
				Description: "One committed session transaction",
				Parents:     telemetry.ParentAny(),
				StartAttributes: map[string]TelemetryStartAttributeDefinition{
					"pi.session.id":         startDef(withCardinality(stringDef("Session id"), "high"), true),
					"pi.lane.name":          startDef(withCardinality(stringDef("Lane name when supplied by the caller"), "high"), false),
					"pi.operation.id":       startDef(withCardinality(stringDef("Durable operation id when supplied by the caller"), "high"), false),
					"pi.session.item_count": startDef(numberDef("Number of writes in the transaction"), true),
					"pi.session.item_kinds": startDef(withStringElements(stringDef("Distinct write kinds in the transaction"), "entry", "usage", "value", "list"), true),
				},
				EndAttributes: map[string]TelemetryAttributeDefinition{
					"pi.session.first_seq": numberDef("First committed sequence in the transaction"),
					"pi.session.last_seq":  numberDef("Last committed sequence in the transaction"),
				},
				Status: telemetry.TelemetrySpanStatusDefinition{Default: "ok", ErrorWhen: "Storage rejects the transaction"},
			},
		},
	}
}
