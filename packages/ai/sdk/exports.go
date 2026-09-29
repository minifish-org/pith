// This file is a Go port of packages/ai/src/index.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// index.ts is the core, side-effect-free root entry point: it re-exports the
// TypeBox schema vocabulary, the per-API option DTOs, the public auth DTOs and
// a handful of text/uuid helpers, while deliberately excluding generated
// catalogs, provider factories, the api registry, OAuth implementations and
// compat. Go has no barrel re-export, so the SDK entry point exposes typed
// aliases and function values that resolve to the real declarations.
package sdk

import (
	"encoding/json"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// TSchema is the JSON Schema document type behind the TypeBox re-export. Go
// carries schemas as their JSON wire document, which is exactly what provider
// request bodies consume, so the root entry point exposes that representation.
type TSchema = json.RawMessage

// Static is the schema-to-value projection exposed by the root entry point.
// TypeScript extracts a value type from a schema at the type level; Go has no
// type-level schema extraction, so the projection is the runtime identity over
// the Go value that a caller pairs with the schema it is building.
func Static[T any](value T) T { return value }

// SchemaBuilder builds the JSON Schema documents that providers and tool
// declarations consume. It is the functional Go equivalent of the TypeBox `Type`
// namespace re-exported by the root entry point; every constructor returns the
// wire document without wrapping it in a type-level kind.
type SchemaBuilder struct{}

// NewSchemaBuilder creates a schema builder.
func NewSchemaBuilder() *SchemaBuilder { return &SchemaBuilder{} }

// Type is the schema builder namespace exposed by the root entry point.
var Type = NewSchemaBuilder()

func marshalSchema(value any) TSchema {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return encoded
}

// Unsafe returns a schema document unchanged. It mirrors `Type.Unsafe`.
func (b *SchemaBuilder) Unsafe(schema TSchema) TSchema { return schema }

// String builds `{ "type": "string" }`.
func (b *SchemaBuilder) String() TSchema { return marshalSchema(map[string]any{"type": "string"}) }

// Number builds `{ "type": "number" }`.
func (b *SchemaBuilder) Number() TSchema { return marshalSchema(map[string]any{"type": "number"}) }

// Integer builds `{ "type": "integer" }`.
func (b *SchemaBuilder) Integer() TSchema { return marshalSchema(map[string]any{"type": "integer"}) }

// Boolean builds `{ "type": "boolean" }`.
func (b *SchemaBuilder) Boolean() TSchema { return marshalSchema(map[string]any{"type": "boolean"}) }

// Null builds `{ "type": "null" }`.
func (b *SchemaBuilder) Null() TSchema { return marshalSchema(map[string]any{"type": "null"}) }

// Array builds `{ "type": "array", "items": <items> }`.
func (b *SchemaBuilder) Array(items TSchema) TSchema {
	document := map[string]any{"type": "array"}
	if len(items) > 0 {
		document["items"] = items
	}
	return marshalSchema(document)
}

// Object builds an object schema. The required list is only emitted when it is
// non-empty, preserving the absence of an empty required array.
func (b *SchemaBuilder) Object(properties map[string]TSchema, required ...string) TSchema {
	document := map[string]any{"type": "object"}
	if properties != nil {
		document["properties"] = properties
	}
	if len(required) > 0 {
		document["required"] = required
	}
	return marshalSchema(document)
}

// Enum builds a string enum, the shape upstream uses for provider-independent
// enumerations.
func (b *SchemaBuilder) Enum(values ...string) TSchema {
	return marshalSchema(map[string]any{"type": "string", "enum": values})
}

// Literal builds `{ "const": <value> }`.
func (b *SchemaBuilder) Literal(value any) TSchema {
	return marshalSchema(map[string]any{"const": value})
}

// Union builds `{ "anyOf": [...] }`.
func (b *SchemaBuilder) Union(schemas ...TSchema) TSchema {
	members := make([]json.RawMessage, 0, len(schemas))
	for _, schema := range schemas {
		if len(schema) > 0 {
			members = append(members, schema)
		}
	}
	return marshalSchema(map[string]any{"anyOf": members})
}

// AnthropicEffort is the adaptive-thinking effort level.
type AnthropicEffort = api.AnthropicEffort

// AnthropicOptions are the Anthropic Messages-specific stream options.
type AnthropicOptions = api.AnthropicOptions

// AnthropicThinkingDisplay controls how thinking content is returned.
type AnthropicThinkingDisplay = api.AnthropicThinkingDisplay

// AzureOpenAIResponsesOptions are the Azure OpenAI Responses stream options.
type AzureOpenAIResponsesOptions = api.AzureOpenAIResponsesOptions

// BedrockOptions are the Bedrock ConverseStream-specific stream options.
type BedrockOptions = api.BedrockOptions

// BedrockThinkingDisplay controls how Claude thinking content is returned.
type BedrockThinkingDisplay = api.BedrockThinkingDisplay

// GoogleOptions are the Google Generative AI stream options.
type GoogleOptions = api.GoogleOptions

// GoogleApiThinkingLevel is the discrete Gemini thinking level wire value.
type GoogleApiThinkingLevel = api.GoogleApiThinkingLevel

// ResolvedGoogleThinkingLevel is a thinking level restricted to the levels a
// model supports.
type ResolvedGoogleThinkingLevel = api.ResolvedGoogleThinkingLevel

// GoogleVertexOptions are the Google Vertex stream options.
type GoogleVertexOptions = api.GoogleVertexOptions

// MistralOptions are the provider-specific options for the Mistral API.
type MistralOptions = api.MistralOptions

// OpenAICodexResponsesOptions are the Codex Responses stream options.
type OpenAICodexResponsesOptions = api.OpenAICodexResponsesOptions

// OpenAICodexWebSocketDebugStats is the per-session WebSocket debug snapshot.
type OpenAICodexWebSocketDebugStats = api.OpenAICodexWebSocketDebugStats

// OpenAICompletionsOptions are the OpenAI Chat Completions stream options.
type OpenAICompletionsOptions = api.OpenAICompletionsOptions

// OpenAIResponsesOptions are the OpenAI Responses-specific stream options.
type OpenAIResponsesOptions = api.OpenAIResponsesOptions

// PiMessagesEvent is a serialized assistant-message event sent by a
// pi-messages server.
type PiMessagesEvent = api.PiMessagesEvent

// PiMessagesOptions are the provider-specific options for the pi-messages API.
type PiMessagesOptions = api.PiMessagesOptions

// PiMessagesRewriteImpact is the impact summary of a server-side rewrite.
type PiMessagesRewriteImpact = api.PiMessagesRewriteImpact

// contentText, getSystemMessageText, renderSystemMessageUpdate and uuidv7 are
// exposed as function values so the root entry point re-exports the real
// implementations without an import cycle.
var (
	// ContentText renders message content to text.
	ContentText = utils.ContentText
	// GetSystemMessageText renders the system message text.
	GetSystemMessageText = utils.GetSystemMessageText
	// RenderSystemMessageUpdate renders a system-message update line.
	RenderSystemMessageUpdate = utils.RenderSystemMessageUpdate
	// UUIDv7 generates a time-ordered UUIDv7 string.
	UUIDv7 = utils.UUIDv7
)
