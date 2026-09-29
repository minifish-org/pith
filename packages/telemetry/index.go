// Package telemetry defines the provider-neutral telemetry protocol used across
// Pi, together with its no-op and in-memory implementations.
//
// This is a Go port of packages/telemetry/src/index.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Attribute values are read defensively: the upstream adapter contract requires
// that malformed or unreadable telemetry payloads never fail a request, so the
// recording implementations recover from panics while reading attributes and
// treat a failed read as "record nothing for this attribute".
package telemetry

import (
	"context"
	"encoding/json"
)

// AttributeValueType names the supported attribute value shapes.
type AttributeValueType string

// Attribute value types.
const (
	AttributeTypeString     AttributeValueType = "string"
	AttributeTypeNumber     AttributeValueType = "number"
	AttributeTypeBoolean    AttributeValueType = "boolean"
	AttributeTypeStringArr  AttributeValueType = "string[]"
	AttributeTypeNumberArr  AttributeValueType = "number[]"
	AttributeTypeBooleanArr AttributeValueType = "boolean[]"
	// AttributeTypeUnreadable marks a value that cannot be read. It is the Go
	// stand-in for the JavaScript throwing Proxy: a payload containing such a
	// value must be ignored atomically instead of failing the request.
	AttributeTypeUnreadable AttributeValueType = "unreadable"
)

// AttributeValue is a single telemetry attribute value.
//
// Exactly one of the variant fields is populated, selected by Type. Scalars use
// the pointer forms so that an explicitly recorded zero value stays distinct
// from an absent attribute.
type AttributeValue struct {
	Type AttributeValueType

	// String and Bool are the scalar variants.
	String *string
	Number *float64
	Bool   *bool

	// Strings, Numbers and Bools are the array variants.
	Strings []string
	Numbers []float64
	Bools   []bool
}

// StringAttribute builds a string attribute value.
func StringAttribute(value string) AttributeValue {
	return AttributeValue{Type: AttributeTypeString, String: &value}
}

// NumberAttribute builds a number attribute value.
func NumberAttribute(value float64) AttributeValue {
	return AttributeValue{Type: AttributeTypeNumber, Number: &value}
}

// BoolAttribute builds a boolean attribute value.
func BoolAttribute(value bool) AttributeValue {
	return AttributeValue{Type: AttributeTypeBoolean, Bool: &value}
}

// StringsAttribute builds a string array attribute value.
func StringsAttribute(values []string) AttributeValue {
	return AttributeValue{Type: AttributeTypeStringArr, Strings: values}
}

// NumbersAttribute builds a number array attribute value.
func NumbersAttribute(values []float64) AttributeValue {
	return AttributeValue{Type: AttributeTypeNumberArr, Numbers: values}
}

// BoolsAttribute builds a boolean array attribute value.
func BoolsAttribute(values []bool) AttributeValue {
	return AttributeValue{Type: AttributeTypeBooleanArr, Bools: values}
}

// UnreadableValue returns an attribute value whose reads report failure.
//
// It is the explicit marker for the passivity guarantee: JavaScript models an
// unreadable payload with a Proxy that throws on every property access, and Go
// has no transparent equivalent. A map that contains such a value is treated as
// unreadable as a whole, so nothing from it is recorded and the request is never
// failed by telemetry.
func UnreadableValue() AttributeValue {
	return AttributeValue{Type: AttributeTypeUnreadable}
}

// Clone returns a detached copy of the attribute value, mirroring the upstream
// copyAttributeValue behavior for arrays.
func (v AttributeValue) Clone() AttributeValue {
	clone := v
	if v.Strings != nil {
		clone.Strings = append([]string(nil), v.Strings...)
	}
	if v.Numbers != nil {
		clone.Numbers = append([]float64(nil), v.Numbers...)
	}
	if v.Bools != nil {
		clone.Bools = append([]bool(nil), v.Bools...)
	}
	return clone
}

// MarshalJSON writes the JSON shape of the attribute value.
func (v AttributeValue) MarshalJSON() ([]byte, error) {
	switch v.Type {
	case AttributeTypeString:
		if v.String == nil {
			return []byte("null"), nil
		}
		return json.Marshal(*v.String)
	case AttributeTypeNumber:
		if v.Number == nil {
			return []byte("null"), nil
		}
		return json.Marshal(*v.Number)
	case AttributeTypeBoolean:
		if v.Bool == nil {
			return []byte("null"), nil
		}
		return json.Marshal(*v.Bool)
	case AttributeTypeStringArr:
		if v.Strings == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(v.Strings)
	case AttributeTypeNumberArr:
		if v.Numbers == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(v.Numbers)
	case AttributeTypeBooleanArr:
		if v.Bools == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(v.Bools)
	default:
		return []byte("null"), nil
	}
}

// UnmarshalJSON reads any of the supported attribute value shapes.
func (v *AttributeValue) UnmarshalJSON(data []byte) error {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch typed := raw.(type) {
	case string:
		*v = StringAttribute(typed)
	case float64:
		*v = NumberAttribute(typed)
	case bool:
		*v = BoolAttribute(typed)
	case []any:
		if strings, ok := toStringSlice(typed); ok {
			*v = StringsAttribute(strings)
			return nil
		}
		if numbers, ok := toNumberSlice(typed); ok {
			*v = NumbersAttribute(numbers)
			return nil
		}
		if booleans, ok := toBoolSlice(typed); ok {
			*v = BoolsAttribute(booleans)
			return nil
		}
		*v = StringsAttribute(nil)
	case nil:
		*v = AttributeValue{}
	default:
		*v = AttributeValue{}
	}
	return nil
}

func toStringSlice(values []any) ([]string, bool) {
	out := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

func toNumberSlice(values []any) ([]float64, bool) {
	out := make([]float64, 0, len(values))
	for _, value := range values {
		number, ok := value.(float64)
		if !ok {
			return nil, false
		}
		out = append(out, number)
	}
	return out, true
}

func toBoolSlice(values []any) ([]bool, bool) {
	out := make([]bool, 0, len(values))
	for _, value := range values {
		boolean, ok := value.(bool)
		if !ok {
			return nil, false
		}
		out = append(out, boolean)
	}
	return out, true
}

// SpanAttributes are the attributes attached to a span or span event.
//
// A present key with a nil value means the attribute is undefined: it must not
// be recorded and must not overwrite an existing value.
type SpanAttributes map[string]AttributeValue

// HasKey reports whether the key was supplied.
func (a SpanAttributes) HasKey(name string) bool {
	_, ok := a[name]
	return ok
}

// SpanOptions are the options for starting a span.
type SpanOptions struct {
	Name       string         `json:"name"`
	Attributes SpanAttributes `json:"attributes,omitempty"`
}

// SpanError describes a failed span.
type SpanError struct {
	Name    string `json:"name"`
	Message string `json:"message"`
}

// SpanStatus is the settled status of a span: either ok, or an error with an
// optional provider-neutral error name and message.
type SpanStatus struct {
	Status string     `json:"status"`
	Error  *SpanError `json:"error,omitempty"`
}

// StatusOK returns the ok span status.
func StatusOK() SpanStatus { return SpanStatus{Status: "ok"} }

// StatusError builds an error span status carrying name and message.
func StatusError(name, message string) SpanStatus {
	return SpanStatus{Status: "error", Error: &SpanError{Name: name, Message: message}}
}

// StatusErrorBare builds an error span status without details.
func StatusErrorBare() SpanStatus { return SpanStatus{Status: "error"} }

// IsOK reports whether the status is ok.
func (s SpanStatus) IsOK() bool { return s.Status == "ok" }

// IsError reports whether the status is an error.
func (s SpanStatus) IsError() bool { return s.Status == "error" }

// TelemetryContext admits callbacks into a telemetry span.
type TelemetryContext interface {
	StartSpan(ctx context.Context, options SpanOptions, callback SpanCallback) error
}

// TelemetrySpan is a started span that can itself start child spans.
type TelemetrySpan interface {
	TelemetryContext
	AddEvent(name string, attributes SpanAttributes)
	SetAttributes(attributes SpanAttributes)
	SetStatus(status SpanStatus)
}

// SpanCallback is the body of a span. It is invoked once with the started span.
type SpanCallback func(ctx context.Context, span TelemetrySpan) error

// TelemetryAttributeMetadata is the documentation metadata of an attribute.
type TelemetryAttributeMetadata struct {
	Description string  `json:"description"`
	Sensitive   *bool   `json:"sensitive,omitempty"`
	Cardinality *string `json:"cardinality,omitempty"`
}

// TelemetryAttributeDefinition describes one schema attribute: its metadata plus
// the union member selected by Type.
type TelemetryAttributeDefinition struct {
	TelemetryAttributeMetadata
	// Type is one of the AttributeValueType values.
	Type AttributeValueType `json:"type"`
	// Values enumerates the accepted values for scalar definitions.
	Values []AttributeValue `json:"values,omitempty"`
	// ElementValues enumerates the accepted element values for array definitions.
	ElementValues []AttributeValue `json:"elementValues,omitempty"`
	// Examples are illustrative values; array definitions use nested arrays.
	Examples []ExampleValue `json:"examples,omitempty"`
}

// ExampleValue is one example for an attribute definition. It holds a scalar or
// an array of values.
type ExampleValue struct {
	Scalar *AttributeValue
	Array  []AttributeValue
}

// MarshalJSON writes an example as a scalar or an array.
func (e ExampleValue) MarshalJSON() ([]byte, error) {
	if e.Array != nil {
		return json.Marshal(e.Array)
	}
	if e.Scalar != nil {
		return json.Marshal(*e.Scalar)
	}
	return []byte("null"), nil
}

// UnmarshalJSON reads a scalar or array example.
func (e *ExampleValue) UnmarshalJSON(data []byte) error {
	var probe any
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if _, ok := probe.([]any); ok {
		var values []AttributeValue
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		e.Array = values
		e.Scalar = nil
		return nil
	}
	var value AttributeValue
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	e.Scalar = &value
	e.Array = nil
	return nil
}

// TelemetryStartAttributeDefinition is a required- or optional-marked start
// attribute definition.
type TelemetryStartAttributeDefinition struct {
	TelemetryAttributeDefinition
	Required bool `json:"required"`
}

// TelemetryEventAttributeDefinition is a required- or optional-marked event
// attribute definition.
type TelemetryEventAttributeDefinition struct {
	TelemetryAttributeDefinition
	Required bool `json:"required"`
}

// TelemetryEventDefinition describes one span event.
type TelemetryEventDefinition struct {
	Description string                                       `json:"description"`
	Attributes  map[string]TelemetryEventAttributeDefinition `json:"attributes"`
}

// TelemetryParentDefinition describes which spans may be a span's parent.
//
// Kind is "any", "root_or_external", or "spans"; Spans is set for the last form.
type TelemetryParentDefinition struct {
	Kind  string   `json:"kind"`
	Spans []string `json:"spans,omitempty"`
}

// ParentAny is a parent definition that accepts any parent.
func ParentAny() TelemetryParentDefinition {
	return TelemetryParentDefinition{Kind: "any"}
}

// ParentRootOrExternal is a parent definition that accepts only a root or
// externally supplied parent.
func ParentRootOrExternal() TelemetryParentDefinition {
	return TelemetryParentDefinition{Kind: "root_or_external"}
}

// ParentSpans is a parent definition restricted to the named spans.
func ParentSpans(spans []string) TelemetryParentDefinition {
	return TelemetryParentDefinition{Kind: "spans", Spans: spans}
}

// TelemetrySpanDefinition describes one span of a telemetry schema.
type TelemetrySpanDefinition struct {
	Description     string                                       `json:"description"`
	Parents         TelemetryParentDefinition                    `json:"parents"`
	StartAttributes map[string]TelemetryStartAttributeDefinition `json:"startAttributes"`
	EndAttributes   map[string]TelemetryAttributeDefinition      `json:"endAttributes"`
	Events          map[string]TelemetryEventDefinition          `json:"events,omitempty"`
	// Status declares the default status and the condition that turns it into
	// an error.
	Status TelemetrySpanStatusDefinition `json:"status"`
}

// TelemetrySpanStatusDefinition is the status contract of a span definition.
type TelemetrySpanStatusDefinition struct {
	Default   string `json:"default"`
	ErrorWhen string `json:"errorWhen"`
}

// TelemetrySchemaDefinition is a versioned telemetry schema.
type TelemetrySchemaDefinition struct {
	Version int                                `json:"version"`
	Spans   map[string]TelemetrySpanDefinition `json:"spans"`
}

// DefineTelemetrySchema is the typed identity helper for serializable telemetry
// schema data. It returns the schema unchanged.
func DefineTelemetrySchema(schema TelemetrySchemaDefinition) TelemetrySchemaDefinition {
	return schema
}

// TelemetrySchemaSpanName is the name of a span with the given name.
type TelemetrySchemaSpanName = string

// InferRequiredAndOptionalAttributes returns the attribute map of a definition
// set: required attributes are always present, optional ones are omitted when
// absent.
type InferRequiredAndOptionalAttributes = SpanAttributes

// InferStartAttributes is the inferred attributes of a start definition set.
type InferStartAttributes = SpanAttributes

// InferOptionalAttributes is the inferred attributes of an optional-only
// definition set.
type InferOptionalAttributes = SpanAttributes

// ExactTelemetryAttributes documents that attribute maps must not carry keys
// outside their schema.
type ExactTelemetryAttributes = SpanAttributes

// InferEventAttributes is the inferred attributes of an event definition set.
type InferEventAttributes = SpanAttributes

// TelemetrySchemaSpanStartAttributes is the inferred start attributes of a
// named span in a schema.
func TelemetrySchemaSpanStartAttributes(schema TelemetrySchemaDefinition, name string) SpanAttributes {
	span, ok := schema.Spans[name]
	if !ok {
		return nil
	}
	return requiredAttributes(span.StartAttributes, startAttributeRequired)
}

// TelemetrySchemaSpanEndAttributes is the inferred end attributes of a named
// span in a schema.
func TelemetrySchemaSpanEndAttributes(schema TelemetrySchemaDefinition, name string) SpanAttributes {
	span, ok := schema.Spans[name]
	if !ok {
		return nil
	}
	return optionalAttributes(span.EndAttributes)
}

// TelemetrySchemaSpanEventName is the name of an event of a named span.
type TelemetrySchemaSpanEventName = string

// TelemetrySchemaSpanEventAttributes is the inferred attributes of a named
// event of a named span.
func TelemetrySchemaSpanEventAttributes(schema TelemetrySchemaDefinition, spanName, eventName string) SpanAttributes {
	span, ok := schema.Spans[spanName]
	if !ok || span.Events == nil {
		return nil
	}
	event, ok := span.Events[eventName]
	if !ok {
		return nil
	}
	return requiredAttributes(event.Attributes, eventAttributeRequired)
}

// startAttributeRequired reports whether a start attribute is required.
func startAttributeRequired(definition TelemetryStartAttributeDefinition) bool {
	return definition.Required
}

// eventAttributeRequired reports whether an event attribute is required.
func eventAttributeRequired(definition TelemetryEventAttributeDefinition) bool {
	return definition.Required
}

func requiredAttributes[Definition any](definitions map[string]Definition, isRequired func(Definition) bool) SpanAttributes {
	out := SpanAttributes{}
	for name, definition := range definitions {
		if !isRequired(definition) {
			continue
		}
		out[name] = AttributeValue{}
	}
	return out
}

func optionalAttributes(definitions map[string]TelemetryAttributeDefinition) SpanAttributes {
	out := SpanAttributes{}
	for name := range definitions {
		out[name] = AttributeValue{}
	}
	return out
}

// SchemaTelemetrySpan is a span bound to the events and end attributes declared
// by one schema span. The generic form is expressed through AddSchemaEvent.
type SchemaTelemetrySpan struct {
	TelemetrySpan
}

// AddSchemaEvent records an event declared by the schema.
func (s SchemaTelemetrySpan) AddSchemaEvent(name string, attributes SpanAttributes) {
	s.AddEvent(name, attributes)
}

// SetSchemaAttributes records the schema's end attributes.
func (s SchemaTelemetrySpan) SetSchemaAttributes(attributes SpanAttributes) {
	s.SetAttributes(attributes)
}

// TelemetrySchemaSpanUnion is one member of the schema's span vocabulary.
type TelemetrySchemaSpanUnion struct {
	Name            string                    `json:"name"`
	StartAttributes SpanAttributes            `json:"startAttributes"`
	EndAttributes   SpanAttributes            `json:"endAttributes"`
	Events          map[string]SpanAttributes `json:"events"`
}

// TypedSpanStarter starts schema-typed spans: each call names one span of the
// bound schemas and receives the started span plus a starter for child spans.
type TypedSpanStarter func(ctx context.Context, name string, attributes SpanAttributes, callback TypedSpanCallback) error

// TypedSpanCallback is the body of a typed span.
type TypedSpanCallback func(ctx context.Context, span TelemetrySpan, startChildSpan TypedSpanStarter) error

// CreateTypedSpanStarter binds an explicit parent context to the combined span
// vocabulary of the given schemas. It is the upstream createTypedSpanStarter.
//
// Schema values are used only for validation bookkeeping; no runtime schema
// validation is performed, matching upstream.
func CreateTypedSpanStarter(ctx TelemetryContext, schemas []TelemetrySchemaDefinition) TypedSpanStarter {
	return BindTypedSpanStarter(ctx, schemas)
}

// BindTypedSpanStarter binds the span vocabulary of the supplied schemas to a
// starter whose child spans keep the schema binding.
//
// The schemas argument is accepted for call-site parity with the upstream
// createTypedSpanStarter signature; schema data is used only for type inference
// upstream and no runtime schema validation is performed.
func BindTypedSpanStarter(ctx TelemetryContext, schemas []TelemetrySchemaDefinition) TypedSpanStarter {
	return func(callCtx context.Context, name string, attributes SpanAttributes, callback TypedSpanCallback) error {
		return ctx.StartSpan(callCtx, SpanOptions{Name: name, Attributes: attributes},
			func(spanCtx context.Context, span TelemetrySpan) error {
				return callback(spanCtx, span, BindTypedSpanStarter(span, schemas))
			})
	}
}
