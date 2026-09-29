// Package types holds the shared AI message, model, protocol and image DTOs.
//
// This is a Go port of packages/ai/src/types.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Value semantics: absent and present-zero are distinct for every optional
// field, which is why optionals use pointers, json.RawMessage or explicit
// presence flags instead of Go zero values. JSON objects that must preserve
// their exact literal form (tool call arguments, deferred data) are kept as
// json.RawMessage so numbers and signatures are not silently rewritten.
package types

import (
	"bytes"
	"encoding/json"

	"github.com/minifish-org/pith/packages/ai/utils/eventstream"
)

// newBytesReader is a small local helper that keeps JSON decoding call sites
// readable. It is a Go-only support function, not an upstream symbol.
func newBytesReader(data []byte) *bytes.Reader {
	return bytes.NewReader(data)
}

// KnownApi is the set of API identifiers the SDK implements natively.
// Custom API strings are represented by the plain Api type.
type KnownApi string

// Known API identifiers.
//
// The constants are intentionally untyped so that they are usable both as a
// KnownApi and as the open Api type (upstream `KnownApi | (string & {})`), the
// same way an untyped string literal is. The KnownApi type still names the
// closed vocabulary for callers that want it.
const (
	ApiOpenAICompletions     = "openai-completions"
	ApiMistralConversations  = "mistral-conversations"
	ApiOpenAIResponses       = "openai-responses"
	ApiAzureOpenAIResponses  = "azure-openai-responses"
	ApiOpenAICodexResponses  = "openai-codex-responses"
	ApiAnthropicMessages     = "anthropic-messages"
	ApiBedrockConverseStream = "bedrock-converse-stream"
	ApiGoogleGenerativeAI    = "google-generative-ai"
	ApiGoogleVertex          = "google-vertex"
	ApiPiMessages            = "pi-messages"
)

// apiKnown lists the known API ids in upstream declaration order.
var apiKnown = []KnownApi{
	ApiOpenAICompletions,
	ApiMistralConversations,
	ApiOpenAIResponses,
	ApiAzureOpenAIResponses,
	ApiOpenAICodexResponses,
	ApiAnthropicMessages,
	ApiBedrockConverseStream,
	ApiGoogleGenerativeAI,
	ApiGoogleVertex,
	ApiPiMessages,
}

// KnownApis returns the known API identifiers in declaration order.
func KnownApis() []KnownApi {
	out := make([]KnownApi, len(apiKnown))
	copy(out, apiKnown)
	return out
}

// Api identifies a streaming API. It is a KnownApi or a custom provider API
// identifier (upstream `KnownApi | (string & {})`).
type Api string

// Known reports whether the API identifier is one the SDK implements natively.
func (a Api) Known() bool {
	for _, known := range apiKnown {
		if KnownApi(a) == known {
			return true
		}
	}
	return false
}

// KnownImagesApi is the set of image-generation API identifiers.
type KnownImagesApi string

// ApiOpenRouterImages is the only known image API. It is an untyped constant so
// it is usable both as a KnownImagesApi and as the open ImagesApi.
const ApiOpenRouterImages = "openrouter-images"

// ImagesApi identifies an image-generation API.
type ImagesApi string

// KnownProvider is the set of provider identifiers shipped with the SDK.
type KnownProvider string

// Known provider identifiers.
//
// As with the API identifiers, these are untyped constants so they satisfy both
// the closed KnownProvider vocabulary and the open ProviderId type.
const (
	ProviderAmazonBedrock           = "amazon-bedrock"
	ProviderAntLing                 = "ant-ling"
	ProviderAnthropic               = "anthropic"
	ProviderGoogle                  = "google"
	ProviderGoogleVertex            = "google-vertex"
	ProviderOpenAI                  = "openai"
	ProviderAzureOpenAIResponses    = "azure-openai-responses"
	ProviderOpenAICodex             = "openai-codex"
	ProviderRadius                  = "radius"
	ProviderNvidia                  = "nvidia"
	ProviderDeepSeek                = "deepseek"
	ProviderGitHubCopilot           = "github-copilot"
	ProviderXAI                     = "xai"
	ProviderGroq                    = "groq"
	ProviderCerebras                = "cerebras"
	ProviderOpenRouter              = "openrouter"
	ProviderVercelAIGateway         = "vercel-ai-gateway"
	ProviderZai                     = "zai"
	ProviderZaiCodingCN             = "zai-coding-cn"
	ProviderMistral                 = "mistral"
	ProviderMinimax                 = "minimax"
	ProviderMinimaxCN               = "minimax-cn"
	ProviderMoonshotAI              = "moonshotai"
	ProviderMoonshotAICN            = "moonshotai-cn"
	ProviderHuggingFace             = "huggingface"
	ProviderFireworks               = "fireworks"
	ProviderTogether                = "together"
	ProviderBaseten                 = "baseten"
	ProviderOpencode                = "opencode"
	ProviderOpencodeGo              = "opencode-go"
	ProviderKimiCoding              = "kimi-coding"
	ProviderMeta                    = "meta"
	ProviderCloudflareWorkersAI     = "cloudflare-workers-ai"
	ProviderCloudflareAIGateway     = "cloudflare-ai-gateway"
	ProviderQwenTokenPlan           = "qwen-token-plan"
	ProviderQwenTokenPlanCN         = "qwen-token-plan-cn"
	ProviderQwenTokenPlanIndividual = "qwen-token-plan-individual"
	ProviderXiaomi                  = "xiaomi"
	ProviderXiaomiTokenPlanCN       = "xiaomi-token-plan-cn"
	ProviderXiaomiTokenPlanAMS      = "xiaomi-token-plan-ams"
	ProviderXiaomiTokenPlanSGP      = "xiaomi-token-plan-sgp"
)

// providerKnown lists the known providers in upstream declaration order.
var providerKnown = []KnownProvider{
	ProviderAmazonBedrock,
	ProviderAntLing,
	ProviderAnthropic,
	ProviderGoogle,
	ProviderGoogleVertex,
	ProviderOpenAI,
	ProviderAzureOpenAIResponses,
	ProviderOpenAICodex,
	ProviderRadius,
	ProviderNvidia,
	ProviderDeepSeek,
	ProviderGitHubCopilot,
	ProviderXAI,
	ProviderGroq,
	ProviderCerebras,
	ProviderOpenRouter,
	ProviderVercelAIGateway,
	ProviderZai,
	ProviderZaiCodingCN,
	ProviderMistral,
	ProviderMinimax,
	ProviderMinimaxCN,
	ProviderMoonshotAI,
	ProviderMoonshotAICN,
	ProviderHuggingFace,
	ProviderFireworks,
	ProviderTogether,
	ProviderBaseten,
	ProviderOpencode,
	ProviderOpencodeGo,
	ProviderKimiCoding,
	ProviderMeta,
	ProviderCloudflareWorkersAI,
	ProviderCloudflareAIGateway,
	ProviderQwenTokenPlan,
	ProviderQwenTokenPlanCN,
	ProviderQwenTokenPlanIndividual,
	ProviderXiaomi,
	ProviderXiaomiTokenPlanCN,
	ProviderXiaomiTokenPlanAMS,
	ProviderXiaomiTokenPlanSGP,
}

// KnownProviders returns the known provider identifiers in declaration order.
func KnownProviders() []KnownProvider {
	out := make([]KnownProvider, len(providerKnown))
	copy(out, providerKnown)
	return out
}

// ProviderId identifies a model provider.
type ProviderId string

// Known reports whether the provider identifier is shipped with the SDK.
func (p ProviderId) Known() bool {
	for _, known := range providerKnown {
		if KnownProvider(p) == known {
			return true
		}
	}
	return false
}

// KnownImagesProvider is the set of image provider identifiers.
type KnownImagesProvider string

// ProviderOpenRouterImages is the only known image provider. It is an untyped
// constant so it is usable both as a KnownImagesProvider and as the open
// ImagesProviderId.
const ProviderOpenRouterImages = "openrouter"

// ImagesProviderId identifies an image provider.
type ImagesProviderId string

// ToolChoice is the provider-neutral tool selection hint used by simple requests.
type ToolChoice string

// Tool choice values.
const (
	ToolChoiceAuto ToolChoice = "auto"
	ToolChoiceNone ToolChoice = "none"
)

// ThinkingLevel is the provider-neutral reasoning effort level.
type ThinkingLevel string

// Thinking levels.
const (
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

// ModelThinkingLevel is a ThinkingLevel or "off". It is an alias so a
// ThinkingLevel value is directly usable where a model thinking level is
// expected, matching the upstream `ThinkingLevel | "off"` union.
type ModelThinkingLevel = ThinkingLevel

// ThinkingOff disables thinking for a model.
const ThinkingOff = "off"

// ThinkingLevelMap maps Pi thinking levels to provider/model-specific values.
//
// A missing key means "use provider default". A present key with a nil value
// marks the level as unsupported (JSON null upstream).
type ThinkingLevelMap map[ModelThinkingLevel]*string

// Set records a supported mapping for a level.
func (m ThinkingLevelMap) Set(level ModelThinkingLevel, value string) {
	if m != nil {
		m[level] = &value
	}
}

// Unsupported marks a level as explicitly unsupported (JSON null).
func (m ThinkingLevelMap) Unsupported(level ModelThinkingLevel) {
	if m != nil {
		m[level] = nil
	}
}

// Lookup returns the mapped value and how it was mapped.
func (m ThinkingLevelMap) Lookup(level ModelThinkingLevel) (value string, mapped, supported bool) {
	mappedValue, ok := m[level]
	if !ok {
		return "", false, false
	}
	if mappedValue == nil {
		return "", true, false
	}
	return *mappedValue, true, true
}

// ChatTemplateVar is the pi-controlled variable a chat template kwarg reads.
type ChatTemplateVar string

// Chat template variables.
const (
	ChatTemplateVarThinkingEnabled ChatTemplateVar = "thinking.enabled"
	ChatTemplateVarThinkingEffort  ChatTemplateVar = "thinking.effort"
	ChatTemplateVarThinkingBudget  ChatTemplateVar = "thinking.budget"
)

// ChatTemplateVarRef is the literal template substitution object used by
// chat_template_kwargs / chat_template_args values.
type ChatTemplateVarRef struct {
	Var         ChatTemplateVar `json:"$var"`
	OmitWhenOff *bool           `json:"omitWhenOff,omitempty"`
}

// marshalChatTemplateValue preserves the upstream literal shape: plain
// scalars stay scalars, `{ "$var": ... }` substitution objects stay objects.
func marshalChatTemplateValue(value any) ([]byte, error) {
	switch v := value.(type) {
	case nil:
		return []byte("null"), nil
	case ChatTemplateVarRef:
		return json.Marshal(struct {
			Var         ChatTemplateVar `json:"$var"`
			OmitWhenOff *bool           `json:"omitWhenOff,omitempty"`
		}{Var: v.Var, OmitWhenOff: v.OmitWhenOff})
	case *ChatTemplateVarRef:
		if v == nil {
			return []byte("null"), nil
		}
		return marshalChatTemplateValue(*v)
	default:
		return json.Marshal(value)
	}
}

// ChatTemplateKwargValue is the JSON value sent for one chat template kwarg:
// string, number, boolean, null, or a ChatTemplateVarRef substitution.
type ChatTemplateKwargValue struct {
	value any
}

// ChatTemplateString builds a string kwarg value.
func ChatTemplateString(value string) ChatTemplateKwargValue {
	return ChatTemplateKwargValue{value: value}
}

// ChatTemplateNumber builds a number kwarg value. num must be a JSON-safe
// number representation such as json.Number or a float64.
func ChatTemplateNumber(num any) ChatTemplateKwargValue {
	return ChatTemplateKwargValue{value: num}
}

// ChatTemplateBool builds a boolean kwarg value.
func ChatTemplateBool(value bool) ChatTemplateKwargValue {
	return ChatTemplateKwargValue{value: value}
}

// ChatTemplateNull builds a null kwarg value.
func ChatTemplateNull() ChatTemplateKwargValue {
	return ChatTemplateKwargValue{value: nil}
}

// ChatTemplateVarValue builds a `{ "$var": ... }` substitution kwarg value.
func ChatTemplateVarValue(ref ChatTemplateVarRef) ChatTemplateKwargValue {
	return ChatTemplateKwargValue{value: ref}
}

// Value returns the underlying JSON value.
func (v ChatTemplateKwargValue) Value() any { return v.value }

// IsZero reports whether the value was never set.
func (v ChatTemplateKwargValue) IsZero() bool { return v.value == nil }

// MarshalJSON writes the literal JSON shape for the kwarg.
func (v ChatTemplateKwargValue) MarshalJSON() ([]byte, error) {
	return marshalChatTemplateValue(v.value)
}

// UnmarshalJSON reads any of the accepted kwarg shapes.
func (v *ChatTemplateKwargValue) UnmarshalJSON(data []byte) error {
	var raw any
	dec := json.NewDecoder(newBytesReader(data))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if obj, ok := raw.(map[string]any); ok {
		if varName, ok := obj["$var"].(string); ok {
			ref := ChatTemplateVarRef{Var: ChatTemplateVar(varName)}
			if omit, ok := obj["omitWhenOff"].(bool); ok {
				ref.OmitWhenOff = &omit
			}
			v.value = ref
			return nil
		}
	}
	v.value = raw
	return nil
}

// ThinkingTokenBudgetField names the top-level request field used to cap
// reasoning tokens on OpenAI-compatible servers.
type ThinkingTokenBudgetField string

// Thinking token budget fields.
const (
	ThinkingTokenBudgetVLLM     ThinkingTokenBudgetField = "thinking_token_budget"
	ThinkingTokenBudgetQwen     ThinkingTokenBudgetField = "thinking_budget"
	ThinkingTokenBudgetLlamaCpp ThinkingTokenBudgetField = "thinking_budget_tokens"
)

// ThinkingBudgets holds token budgets for each thinking level.
type ThinkingBudgets struct {
	Minimal *int `json:"minimal,omitempty"`
	Low     *int `json:"low,omitempty"`
	Medium  *int `json:"medium,omitempty"`
	High    *int `json:"high,omitempty"`
}

// CacheRetention is the prompt cache retention preference.
type CacheRetention string

// Cache retention values.
const (
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

// ModelPromptCache is the best-effort prompt cache lifetime in seconds for
// each retention tier a request can ask for. A missing tier means the lifetime
// is unknown; Pi does not warm such caches.
type ModelPromptCache struct {
	Short *float64 `json:"short,omitempty"`
	Long  *float64 `json:"long,omitempty"`
}

// Get returns the lifetime for a retention tier together with its presence.
func (c ModelPromptCache) Get(retention CacheRetention) (float64, bool) {
	switch retention {
	case CacheRetentionShort:
		if c.Short != nil {
			return *c.Short, true
		}
	case CacheRetentionLong:
		if c.Long != nil {
			return *c.Long, true
		}
	}
	return 0, false
}

// Transport is the preferred provider transport.
type Transport string

// Transport values.
const (
	TransportSSE             Transport = "sse"
	TransportWebsocket       Transport = "websocket"
	TransportWebsocketCached Transport = "websocket-cached"
	TransportAuto            Transport = "auto"
)

// ProviderEnv holds provider-scoped environment overrides. Values take
// precedence over the process environment. A present key with a nil value
// means the variable is unset rather than inherited.
type ProviderEnv map[string]*string

// ProviderHeaders holds custom request headers. A nil value suppresses a
// provider/API default header with the same name.
type ProviderHeaders map[string]*string

// FetchFunction performs a provider HTTP request. It mirrors the injected
// `fetch` hook; providers that cannot inject a custom implementation reject it.
type FetchFunction func(req *HTTPRequest) (*HTTPResponse, error)

// HTTPRequest is a transport-neutral HTTP request description.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
}

// HTTPResponse is a transport-neutral HTTP response description.
type HTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

// SessionAffinityFormat selects the session-affinity header convention.
type SessionAffinityFormat string

// Session affinity formats.
const (
	SessionAffinityOpenAI          SessionAffinityFormat = "openai"
	SessionAffinityOpenAINoSession SessionAffinityFormat = "openai-nosession"
	SessionAffinityOpenRouter      SessionAffinityFormat = "openrouter"
)

// ProviderResponse describes a received HTTP response for the onResponse hook.
type ProviderResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

// ProviderRequestOptions holds the authentication, HTTP transport and lifecycle
// callbacks shared by provider requests.
type ProviderRequestOptions struct {
	// Signal is the explicit cancellation signal, when the caller is not using
	// a context.Context.
	Signal <-chan struct{}
	// TelemetryContext is the explicit parent context for telemetry produced by
	// this logical request. It is `any` here to keep the AI types package free
	// of a hard telemetry dependency; callers pass a telemetry context value.
	TelemetryContext any
	APIKey           *string
	// Fetch is the optional fetch implementation for provider HTTP requests.
	// It defaults to the process HTTP client.
	Fetch FetchFunction
	// Env holds provider-scoped environment values.
	Env ProviderEnv
	// OnPayload inspects or replaces a provider payload before sending. A nil
	// return keeps the payload unchanged.
	OnPayload func(payload any, model *Model) (any, error)
	// OnResponse is invoked after an HTTP response is received.
	OnResponse func(response ProviderResponse, model *Model)
	// Headers holds custom HTTP headers merged over provider defaults.
	Headers ProviderHeaders
	// TimeoutMs is the HTTP request timeout in milliseconds.
	TimeoutMs *int
	// MaxRetries is the maximum number of client-side retry attempts.
	MaxRetries *int
	// MaxRetryDelayMs caps how long a retry may wait. Zero disables the cap.
	MaxRetryDelayMs *int
}

// DefaultMaxRetryDelayMs is the upstream default retry delay cap.
const DefaultMaxRetryDelayMs = 60000

// StreamOptions are the options shared by every streaming provider.
type StreamOptions struct {
	ProviderRequestOptions
	Temperature *float64
	// SamplingParams are merged into the request body as-is, after the named
	// request fields, so keys here override them.
	SamplingParams map[string]any
	MaxTokens      *int
	Transport      *Transport
	CacheRetention *CacheRetention
	SessionId      *string
	// WebsocketConnectTimeoutMs covers the WebSocket connect handshake only.
	WebsocketConnectTimeoutMs *int
	Metadata                  map[string]any
}

// ProviderStreamOptions are StreamOptions plus arbitrary extra request fields.
type ProviderStreamOptions struct {
	StreamOptions
	Extra map[string]any
}

// DeferredFetchOptions are the options for fetching a deferred response.
type DeferredFetchOptions struct {
	ProviderRequestOptions
	// Wait is the maximum provider long-poll duration in milliseconds. Zero
	// performs one status check.
	Wait *int
}

// DeferredCancelOptions are the options for best-effort deferred cancellation.
type DeferredCancelOptions struct {
	ProviderRequestOptions
}

// ApiOptionsMap maps known APIs to their full provider-specific stream options.
//
// This is a concrete projection of the upstream type-level map: the option
// structs themselves live in their provider packages and are surfaced as
// aliases there, so this map exists to make the API-to-options relationship
// representable and testable without introducing an import cycle.
type ApiOptionsMap map[KnownApi]any

// ApiStreamOptions returns the full stream options type for an API. Known APIs
// resolve to their concrete option type; custom API strings fall back to the
// generic shape.
type ApiStreamOptions struct {
	Api  Api
	Base StreamOptions
	// Extra carries keys not modeled by Base.
	Extra map[string]any
}

// KnownStreamOptions returns true when the API has a concrete options type.
func (o ApiStreamOptions) KnownStreamOptions() bool { return o.Api.Known() }

// ProviderStreams is the uniform stream contract of an API implementation
// module: every module under the API directory exports stream and streamSimple;
// capable modules also export the deferred-response methods.
type ProviderStreams interface {
	Stream(model *Model, context *TranscriptContext, options *StreamOptions) *AssistantMessageEventStream
	StreamSimple(model *Model, context *TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream
	FetchDeferred(model *Model, handle DeferredHandle, options *DeferredFetchOptions) (*AssistantMessageEventStream, error)
	CancelDeferred(model *Model, handle DeferredHandle, options *DeferredCancelOptions) error
}

// ProviderImages is the uniform contract of an image-generation API module.
type ProviderImages interface {
	GenerateImages(model *ImagesModel, context *ImagesContext, options *ImagesOptions) (*AssistantImages, error)
}

// ImagesOptions are the options for image-generation requests.
type ImagesOptions struct {
	ProviderRequestOptions
	Metadata map[string]any
}

// ProviderImagesOptions are ImagesOptions plus arbitrary extra request fields.
type ProviderImagesOptions struct {
	ImagesOptions
	Extra map[string]any
}

// AnthropicAllowedFallbackModel is a model Anthropic accepts in `fallbacks`.
type AnthropicAllowedFallbackModel struct {
	Provider ProviderId `json:"provider"`
	Model    string     `json:"model"`
	Cost     ModelCost  `json:"cost"`
}

// DeferredWindow is the requested deferred-response window.
type DeferredWindow string

// Deferred windows.
const (
	DeferredWindow15m DeferredWindow = "15m"
	DeferredWindow1h  DeferredWindow = "1h"
	DeferredWindow24h DeferredWindow = "24h"
)

// DeferredRequest asks a capable provider to return a durable handle.
//
// Upstream is `boolean | { window?: "15m" | "1h" | "24h" }`; the boolean form
// is preserved because `true` and `{ window: ... }` are not interchangeable on
// the wire.
type DeferredRequest struct {
	// Flag is the boolean form. Nil means the object form was used.
	Flag   *bool
	Window *DeferredWindow
}

// SimpleStreamOptions are the unified reasoning-enabled options used by the
// simple stream entry points.
type SimpleStreamOptions struct {
	StreamOptions
	// ToolChoice is the provider-neutral tool selection hint.
	ToolChoice *ToolChoice
	Reasoning  *ThinkingLevel
	// Deferred requests a durable handle and asynchronous continuation.
	Deferred *DeferredRequest
	// ThinkingBudgets overrides token budgets per thinking level.
	ThinkingBudgets *ThinkingBudgets
}

// StreamFunction is the generic typed stream function used by providers.
//
// It receives a normalized transcript: the system prompt and tools live in the
// leading system message, never on the context itself. Direct StreamSimple
// calls may report missing request auth before a stream is returned; once a
// stream is returned, request/model/runtime failures are encoded in it, and
// error termination must produce an AssistantMessage with stop reason "error"
// or "aborted" plus an error message via the stream protocol.
type StreamFunction func(model *Model, context *TranscriptContext, options *StreamOptions) *AssistantMessageEventStream

// ImagesFunction is the generic typed image-generation function.
type ImagesFunction func(model *ImagesModel, context *ImagesContext, options *ImagesOptions) (*AssistantImages, error)

// TextSignaturePhase is the phase of a TextSignatureV1.
type TextSignaturePhase string

// Text signature phases.
const (
	TextSignaturePhaseCommentary  TextSignaturePhase = "commentary"
	TextSignaturePhaseFinalAnswer TextSignaturePhase = "final_answer"
)

// TextSignatureV1 is the structured text signature carried by TextContent.
type TextSignatureV1 struct {
	V     int                 `json:"v"`
	Id    string              `json:"id"`
	Phase *TextSignaturePhase `json:"phase,omitempty"`
}

// AsTextSignatureV1 returns the signature itself, so a caller holding a value
// does not need to branch between a parsed signature and a legacy id string.
func (s TextSignatureV1) AsTextSignatureV1() *TextSignatureV1 { return &s }

// ParseTextSignatureV1 parses a serialized TextSignatureV1. It returns nil when
// the payload is not a v1 signature object.
func ParseTextSignatureV1(raw string) *TextSignatureV1 {
	var parsed struct {
		V     *int   `json:"v"`
		Id    string `json:"id"`
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	if parsed.V == nil || *parsed.V != 1 {
		return nil
	}
	sig := &TextSignatureV1{V: 1, Id: parsed.Id}
	if parsed.Phase != "" {
		phase := TextSignaturePhase(parsed.Phase)
		sig.Phase = &phase
	}
	return sig
}

// ContentBlockType discriminates a content block.
type ContentBlockType string

// Content block type discriminators.
const (
	ContentTypeText       ContentBlockType = "text"
	ContentTypeThinking   ContentBlockType = "thinking"
	ContentTypeImage      ContentBlockType = "image"
	ContentTypeToolCall   ContentBlockType = "toolCall"
	ContentTypeToolResult ContentBlockType = "toolResult"
)

// TextContent is a text block. TextSignature carries provider message metadata
// (a legacy id string or a serialized TextSignatureV1).
type TextContent struct {
	Type          ContentBlockType `json:"type"`
	Text          string           `json:"text"`
	TextSignature *string          `json:"textSignature,omitempty"`
}

// NewTextContent builds a text content block.
func NewTextContent(text string) TextContent {
	return TextContent{Type: ContentTypeText, Text: text}
}

// ThinkingContent is a reasoning block. ThinkingSignature holds provider-specific
// opaque or serialized reasoning replay data; when Redacted is true the thinking
// content was redacted by safety filters and the opaque encrypted payload lives
// in ThinkingSignature so it can be passed back for multi-turn continuity.
type ThinkingContent struct {
	Type              ContentBlockType `json:"type"`
	Thinking          string           `json:"thinking"`
	ThinkingSignature *string          `json:"thinkingSignature,omitempty"`
	Redacted          *bool            `json:"redacted,omitempty"`
}

// NewThinkingContent builds a thinking content block.
func NewThinkingContent(thinking string) ThinkingContent {
	return ThinkingContent{Type: ContentTypeThinking, Thinking: thinking}
}

// ImageContent is a base64-encoded image block.
type ImageContent struct {
	Type     ContentBlockType `json:"type"`
	Data     string           `json:"data"`
	MimeType string           `json:"mimeType"`
}

// NewImageContent builds an image content block.
func NewImageContent(data, mimeType string) ImageContent {
	return ImageContent{Type: ContentTypeImage, Data: data, MimeType: mimeType}
}

// ToolCall is a model-requested tool invocation.
//
// Arguments stays raw so the exact JSON number and signature semantics survive a
// round trip; callers validate it before execution rather than coercing it here.
type ToolCall struct {
	Type             ContentBlockType `json:"type"`
	Id               string           `json:"id"`
	Name             string           `json:"name"`
	Arguments        json.RawMessage  `json:"arguments"`
	ThoughtSignature *string          `json:"thoughtSignature,omitempty"`
	Namespace        *string          `json:"namespace,omitempty"`
}

// NewToolCall builds a tool call from a pre-encoded JSON argument object.
func NewToolCall(id, name string, arguments json.RawMessage) ToolCall {
	return ToolCall{Type: ContentTypeToolCall, Id: id, Name: name, Arguments: arguments}
}

// ContentBlock is one element of an assistant or user message.
//
// Exactly one of the payloads is populated, selected by Type. Keeping the
// variants in a single struct preserves the ordered, mixed content array that
// the upstream `(A | B | C)[]` union models.
type ContentBlock struct {
	Type     ContentBlockType `json:"type"`
	Text     *TextContent     `json:"-"`
	Thinking *ThinkingContent `json:"-"`
	Image    *ImageContent    `json:"-"`
	ToolCall *ToolCall        `json:"-"`
}

// TextBlock builds a text content block.
func TextBlock(text string) ContentBlock {
	block := NewTextContent(text)
	return ContentBlock{Type: ContentTypeText, Text: &block}
}

// TextBlockSigned builds a text content block carrying a text signature.
func TextBlockSigned(text, signature string) ContentBlock {
	block := NewTextContent(text)
	block.TextSignature = &signature
	return ContentBlock{Type: ContentTypeText, Text: &block}
}

// ThinkingBlock builds a thinking content block.
func ThinkingBlock(thinking string) ContentBlock {
	block := NewThinkingContent(thinking)
	return ContentBlock{Type: ContentTypeThinking, Thinking: &block}
}

// ThinkingBlockSigned builds a thinking content block carrying a signature.
func ThinkingBlockSigned(thinking, signature string) ContentBlock {
	block := NewThinkingContent(thinking)
	block.ThinkingSignature = &signature
	return ContentBlock{Type: ContentTypeThinking, Thinking: &block}
}

// RedactedThinkingBlock builds a redacted thinking content block whose opaque
// payload is stored in the signature field.
func RedactedThinkingBlock(signature string) ContentBlock {
	block := NewThinkingContent("")
	block.ThinkingSignature = &signature
	redacted := true
	block.Redacted = &redacted
	return ContentBlock{Type: ContentTypeThinking, Thinking: &block}
}

// ImageBlock builds an image content block.
func ImageBlock(data, mimeType string) ContentBlock {
	block := NewImageContent(data, mimeType)
	return ContentBlock{Type: ContentTypeImage, Image: &block}
}

// ToolCallBlock builds a tool call content block.
func ToolCallBlock(call ToolCall) ContentBlock {
	call.Type = ContentTypeToolCall
	return ContentBlock{Type: ContentTypeToolCall, ToolCall: &call}
}

// IsText reports whether the block is a text block.
func (b ContentBlock) IsText() bool { return b.Type == ContentTypeText }

// IsThinking reports whether the block is a thinking block.
func (b ContentBlock) IsThinking() bool { return b.Type == ContentTypeThinking }

// IsImage reports whether the block is an image block.
func (b ContentBlock) IsImage() bool { return b.Type == ContentTypeImage }

// IsToolCall reports whether the block is a tool call block.
func (b ContentBlock) IsToolCall() bool { return b.Type == ContentTypeToolCall }

// MarshalJSON writes the discriminated union member selected by Type.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	switch b.Type {
	case ContentTypeText:
		if b.Text != nil {
			return json.Marshal(b.Text)
		}
		return json.Marshal(NewTextContent(""))
	case ContentTypeThinking:
		if b.Thinking != nil {
			return json.Marshal(b.Thinking)
		}
		return json.Marshal(NewThinkingContent(""))
	case ContentTypeImage:
		if b.Image != nil {
			return json.Marshal(b.Image)
		}
		return json.Marshal(NewImageContent("", ""))
	case ContentTypeToolCall:
		if b.ToolCall != nil {
			return json.Marshal(b.ToolCall)
		}
		return json.Marshal(NewToolCall("", "", json.RawMessage("{}")))
	default:
		if b.Text != nil {
			return json.Marshal(b.Text)
		}
		return []byte("null"), nil
	}
}

// UnmarshalJSON reads any of the content variants.
func (b *ContentBlock) UnmarshalJSON(data []byte) error {
	var probe struct {
		Type ContentBlockType `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	b.Type = probe.Type
	b.Text, b.Thinking, b.Image, b.ToolCall = nil, nil, nil, nil
	switch probe.Type {
	case ContentTypeText:
		var block TextContent
		if err := json.Unmarshal(data, &block); err != nil {
			return err
		}
		b.Text = &block
	case ContentTypeThinking:
		var block ThinkingContent
		if err := json.Unmarshal(data, &block); err != nil {
			return err
		}
		b.Thinking = &block
	case ContentTypeImage:
		var block ImageContent
		if err := json.Unmarshal(data, &block); err != nil {
			return err
		}
		b.Image = &block
	case ContentTypeToolCall:
		var block ToolCall
		if err := json.Unmarshal(data, &block); err != nil {
			return err
		}
		b.ToolCall = &block
	}
	return nil
}

// Usage is the token and cost accounting for one request.
//
// Reasoning, when present, is a subset of Output: Output already includes these
// tokens. Providers that expose a reasoning breakdown set it (possibly 0);
// providers that do not leave it absent. CacheWrite1h is the subset of
// CacheWrite written with 1h retention and is only reported by Anthropic.
type Usage struct {
	Input        float64   `json:"input"`
	Output       float64   `json:"output"`
	CacheRead    float64   `json:"cacheRead"`
	CacheWrite   float64   `json:"cacheWrite"`
	CacheWrite1h *float64  `json:"cacheWrite1h,omitempty"`
	Reasoning    *float64  `json:"reasoning,omitempty"`
	TotalTokens  float64   `json:"totalTokens"`
	Cost         UsageCost `json:"cost"`
}

// UsageCost is the monetary cost accounting for one request.
type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

// StopReason is why a model turn stopped.
type StopReason string

// Stop reasons.
const (
	StopReasonPending  StopReason = "pending"
	StopReasonStop     StopReason = "stop"
	StopReasonLength   StopReason = "length"
	StopReasonToolUse  StopReason = "toolUse"
	StopReasonError    StopReason = "error"
	StopReasonAborted  StopReason = "aborted"
	StopReasonDeferred StopReason = "deferred"
)

// DoneStopReasons are the reasons a successful stream may report when it ends.
func DoneStopReasons() []StopReason {
	return []StopReason{StopReasonStop, StopReasonLength, StopReasonToolUse, StopReasonDeferred}
}

// IsDoneReason reports whether reason can end a successful stream.
func (r StopReason) IsDoneReason() bool {
	switch r {
	case StopReasonStop, StopReasonLength, StopReasonToolUse, StopReasonDeferred:
		return true
	default:
		return false
	}
}

// IsErrorReason reports whether reason terminates a failed stream.
func (r StopReason) IsErrorReason() bool {
	return r == StopReasonError || r == StopReasonAborted
}

// JsonValue is any value that survives a JSON round trip.
type JsonValue = any

// JsonObject is a JSON object. Values are JsonValue.
type JsonObject = map[string]JsonValue

// JsonRepresentation is the JSON representation of a typed in-memory value.
// In Go this is the same as JsonValue; the upstream conditional type exists to
// reject non-JSON detail types at compile time.
type JsonRepresentation = JsonValue

// DeferredHandle identifies a provider-side deferred response.
type DeferredHandle struct {
	Provider ProviderId `json:"provider"`
	ModelId  string     `json:"modelId"`
	Api      Api        `json:"api"`
	// Id is the provider token: a response id, or a batch id plus row id.
	Id string `json:"id"`
	// ExpiresAt is the expiry as a Unix millisecond timestamp.
	ExpiresAt   *int64 `json:"expiresAt,omitempty"`
	PollAfterMs *int64 `json:"pollAfterMs,omitempty"`
	// Data carries provider conversion data required to reconstruct the final
	// assistant message.
	Data json.RawMessage `json:"data,omitempty"`
}

// SystemMessageRole is the role discriminator for system messages.
const SystemMessageRole = "system"

// SystemSections is the named prompt-section map of a system message
// (upstream `Record<string, string | null>`). A present name with a nil value
// removes that section; an absent name leaves it untouched.
type SystemSections map[string]*string

// SetSection records a section value.
func (s SystemSections) SetSection(name, content string) {
	if s != nil {
		s[name] = &content
	}
}

// RemoveSection marks a section as removed with an explicit null.
func (s SystemSections) RemoveSection(name string) {
	if s != nil {
		s[name] = nil
	}
}

// SystemMessage carries system instructions and tool declarations at one point
// in the transcript.
//
// The leading system message is the system prompt. Later system messages change
// it: Content adds instructions from that point on, Sections replace or remove
// named prompt sections, and ToolsAdded/ToolsRemoved change the tool set.
// Replaying every system message in order yields the current prompt and tools.
type SystemMessage struct {
	Role string `json:"role"`
	// Content is instruction text. On the leading message this is the base
	// prompt; later it is additional instructions.
	Content SystemContent `json:"content"`
	// Sections are named prompt sections rendered verbatim after Content. A
	// present name with a nil value removes that section.
	Sections SystemSections `json:"sections,omitempty"`
	// ToolsAdded lists complete definitions of tools that become available at
	// this point.
	ToolsAdded []Tool `json:"toolsAdded,omitempty"`
	// ToolsRemoved lists tools that stop being available at this point.
	ToolsRemoved []ToolReference `json:"toolsRemoved,omitempty"`
	// Timestamp is a Unix timestamp in milliseconds.
	Timestamp float64 `json:"timestamp"`
}

// NewSystemMessage builds a system message with a plain string prompt.
func NewSystemMessage(content string, timestamp float64) SystemMessage {
	return SystemMessage{Role: SystemMessageRole, Content: SystemContent{Text: content}, Timestamp: timestamp}
}

// SystemContent is `string | TextContent[]`.
type SystemContent struct {
	// Text holds the plain string form. It is used when Blocks is empty.
	Text string
	// Blocks holds the structured form.
	Blocks []TextContent
	// Structured records which form was supplied, so an empty string and an
	// empty block array stay distinguishable.
	Structured bool
}

// SystemContentText builds the string form.
func SystemContentText(text string) SystemContent { return SystemContent{Text: text} }

// SystemContentBlocks builds the structured form.
func SystemContentBlocks(blocks []TextContent) SystemContent {
	return SystemContent{Blocks: blocks, Structured: true}
}

// MarshalJSON writes the string or array form that was supplied.
func (c SystemContent) MarshalJSON() ([]byte, error) {
	if c.Structured {
		if c.Blocks == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(c.Blocks)
	}
	return json.Marshal(c.Text)
}

// UnmarshalJSON reads either accepted form.
func (c *SystemContent) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		c.Text = asString
		c.Blocks = nil
		c.Structured = false
		return nil
	}
	var blocks []TextContent
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	c.Blocks = blocks
	c.Text = ""
	c.Structured = true
	return nil
}

// UserMessageRole is the role discriminator for user messages.
const UserMessageRole = "user"

// UserMessage carries user input.
//
// Content is `string | (TextContent | ImageContent)[]`; the same presence
// distinction as SystemContent applies.
type UserMessage struct {
	Role    string      `json:"role"`
	Content UserContent `json:"content"`
	// Timestamp is a Unix timestamp in milliseconds.
	Timestamp float64 `json:"timestamp"`
}

// NewUserMessage builds a user message with a plain string prompt.
func NewUserMessage(content string, timestamp float64) UserMessage {
	return UserMessage{Role: UserMessageRole, Content: UserContent{Text: content}, Timestamp: timestamp}
}

// NewUserMessageBlocks builds a user message from ordered content blocks.
func NewUserMessageBlocks(blocks []ContentBlock, timestamp float64) UserMessage {
	return UserMessage{Role: UserMessageRole, Content: UserContent{Blocks: blocks, Structured: true}, Timestamp: timestamp}
}

// UserContent is `string | (TextContent | ImageContent)[]`.
type UserContent struct {
	Text       string
	Blocks     []ContentBlock
	Structured bool
}

// UserContentText builds the string form.
func UserContentText(text string) UserContent { return UserContent{Text: text} }

// UserContentBlocks builds the structured form.
func UserContentBlocks(blocks []ContentBlock) UserContent {
	return UserContent{Blocks: blocks, Structured: true}
}

// MarshalJSON writes the string or array form that was supplied.
func (c UserContent) MarshalJSON() ([]byte, error) {
	if c.Structured {
		if c.Blocks == nil {
			return []byte("[]"), nil
		}
		return json.Marshal(c.Blocks)
	}
	return json.Marshal(c.Text)
}

// UnmarshalJSON reads either accepted form.
func (c *UserContent) UnmarshalJSON(data []byte) error {
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		c.Text = asString
		c.Blocks = nil
		c.Structured = false
		return nil
	}
	var blocks []ContentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	c.Blocks = blocks
	c.Text = ""
	c.Structured = true
	return nil
}

// AssistantMessageRole is the role discriminator for assistant messages.
const AssistantMessageRole = "assistant"

// AssistantMessageDiagnostic is a redacted provider/runtime diagnostic.
//
// The JSON shape matches packages/ai/src/utils/diagnostics.ts ("type",
// "timestamp", optional "error" and "details"). Error stays as `any` so this
// package does not import the utils diagnostics type, which would create an
// import cycle; the two only meet over the wire shape.
type AssistantMessageDiagnostic struct {
	Type      string         `json:"type"`
	Timestamp float64        `json:"timestamp"`
	Error     any            `json:"error,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// AssistantMessage is a complete or partial assistant turn.
type AssistantMessage struct {
	Role     string         `json:"role"`
	Content  []ContentBlock `json:"content"`
	Api      Api            `json:"api"`
	Provider ProviderId     `json:"provider"`
	Model    string         `json:"model"`
	// ResponseModel is the concrete model reported by the provider when it
	// differs from the requested Model.
	ResponseModel *string `json:"responseModel,omitempty"`
	// ResponseId is the provider-specific response/message identifier.
	ResponseId *string `json:"responseId,omitempty"`
	// ProviderThinkingLevel is the exact provider-native effort level used for
	// this response. Absent for legacy or unmanaged responses.
	ProviderThinkingLevel *string                      `json:"providerThinkingLevel,omitempty"`
	Diagnostics           []AssistantMessageDiagnostic `json:"diagnostics,omitempty"`
	Usage                 Usage                        `json:"usage"`
	StopReason            StopReason                   `json:"stopReason"`
	Deferred              *DeferredHandle              `json:"deferred,omitempty"`
	ErrorMessage          *string                      `json:"errorMessage,omitempty"`
	RawStopReason         *string                      `json:"rawStopReason,omitempty"`
	// EndTurn is the provider indication of whether the model explicitly ended
	// its turn. Preserved for debugging only.
	EndTurn *bool `json:"endTurn,omitempty"`
	// Timestamp is a Unix timestamp in milliseconds.
	Timestamp float64 `json:"timestamp"`
}

// NewAssistantMessage builds an empty assistant message for a model.
func NewAssistantMessage(api Api, provider ProviderId, model string, timestamp float64) AssistantMessage {
	return AssistantMessage{
		Role:       AssistantMessageRole,
		Api:        api,
		Provider:   provider,
		Model:      model,
		StopReason: StopReasonPending,
		Timestamp:  timestamp,
	}
}

// ToolResultMessageRole is the role discriminator for tool result messages.
const ToolResultMessageRole = "toolResult"

// ToolResultMessage carries a tool execution result back to the model.
type ToolResultMessage struct {
	Role       string `json:"role"`
	ToolCallId string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	// Content supports text and images.
	Content []ContentBlock `json:"content"`
	// Details is tool-specific JSON metadata.
	Details json.RawMessage `json:"details,omitempty"`
	// Usage is the usage from the tool execution itself, if available. It is not
	// part of main LLM context accounting.
	Usage   *Usage `json:"usage,omitempty"`
	IsError bool   `json:"isError"`
	// Timestamp is a Unix timestamp in milliseconds.
	Timestamp float64 `json:"timestamp"`
}

// NewToolResultMessage builds a tool result message.
func NewToolResultMessage(toolCallId, toolName string, content []ContentBlock, isError bool, timestamp float64) ToolResultMessage {
	return ToolResultMessage{
		Role:       ToolResultMessageRole,
		ToolCallId: toolCallId,
		ToolName:   toolName,
		Content:    content,
		IsError:    isError,
		Timestamp:  timestamp,
	}
}

// Message is one element of a transcript.
//
// Exactly one payload is set, selected by Role. The union is modeled as a struct
// rather than an interface so that the ordered message slice, JSON round trips
// and absent/null/zero distinctions stay under the caller's control.
type Message struct {
	Role       string             `json:"role"`
	System     *SystemMessage     `json:"-"`
	User       *UserMessage       `json:"-"`
	Assistant  *AssistantMessage  `json:"-"`
	ToolResult *ToolResultMessage `json:"-"`
}

// NewSystemMessageVariant builds a system message variant.
func NewSystemMessageVariant(message SystemMessage) Message {
	message.Role = SystemMessageRole
	return Message{Role: SystemMessageRole, System: &message}
}

// NewUserMessageVariant builds a user message variant.
func NewUserMessageVariant(message UserMessage) Message {
	message.Role = UserMessageRole
	return Message{Role: UserMessageRole, User: &message}
}

// NewAssistantMessageVariant builds an assistant message variant.
func NewAssistantMessageVariant(message AssistantMessage) Message {
	message.Role = AssistantMessageRole
	return Message{Role: AssistantMessageRole, Assistant: &message}
}

// NewToolResultMessageVariant builds a tool result message variant.
func NewToolResultMessageVariant(message ToolResultMessage) Message {
	message.Role = ToolResultMessageRole
	return Message{Role: ToolResultMessageRole, ToolResult: &message}
}

// MarshalJSON writes the union member selected by Role.
func (m Message) MarshalJSON() ([]byte, error) {
	switch m.Role {
	case SystemMessageRole:
		if m.System != nil {
			return json.Marshal(m.System)
		}
		return json.Marshal(SystemMessage{Role: SystemMessageRole})
	case UserMessageRole:
		if m.User != nil {
			return json.Marshal(m.User)
		}
		return json.Marshal(UserMessage{Role: UserMessageRole})
	case AssistantMessageRole:
		if m.Assistant != nil {
			return json.Marshal(m.Assistant)
		}
		return json.Marshal(AssistantMessage{Role: AssistantMessageRole})
	case ToolResultMessageRole:
		if m.ToolResult != nil {
			return json.Marshal(m.ToolResult)
		}
		return json.Marshal(ToolResultMessage{Role: ToolResultMessageRole})
	default:
		return []byte("null"), nil
	}
}

// UnmarshalJSON reads a message of any role.
func (m *Message) UnmarshalJSON(data []byte) error {
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	m.Role = probe.Role
	m.System, m.User, m.Assistant, m.ToolResult = nil, nil, nil, nil
	switch probe.Role {
	case SystemMessageRole:
		var message SystemMessage
		if err := json.Unmarshal(data, &message); err != nil {
			return err
		}
		m.System = &message
	case UserMessageRole:
		var message UserMessage
		if err := json.Unmarshal(data, &message); err != nil {
			return err
		}
		m.User = &message
	case AssistantMessageRole:
		var message AssistantMessage
		if err := json.Unmarshal(data, &message); err != nil {
			return err
		}
		m.Assistant = &message
	case ToolResultMessageRole:
		var message ToolResultMessage
		if err := json.Unmarshal(data, &message); err != nil {
			return err
		}
		m.ToolResult = &message
	}
	return nil
}

// ImagesInputContent is one element of an image request.
type ImagesInputContent = ContentBlock

// ImagesOutputContent is one element of an image response.
type ImagesOutputContent = ContentBlock

// ImagesContext is the input of an image-generation request.
type ImagesContext struct {
	Input []ImagesInputContent `json:"input"`
}

// ImagesStopReason is why an image request stopped.
type ImagesStopReason string

// Images stop reasons.
const (
	ImagesStopReasonStop    ImagesStopReason = "stop"
	ImagesStopReasonError   ImagesStopReason = "error"
	ImagesStopReasonAborted ImagesStopReason = "aborted"
)

// AssistantImages is the result of an image-generation request.
type AssistantImages struct {
	Api          ImagesApi             `json:"api"`
	Provider     ImagesProviderId      `json:"provider"`
	Model        string                `json:"model"`
	Output       []ImagesOutputContent `json:"output"`
	ResponseId   *string               `json:"responseId,omitempty"`
	Usage        *Usage                `json:"usage,omitempty"`
	StopReason   ImagesStopReason      `json:"stopReason"`
	ErrorMessage *string               `json:"errorMessage,omitempty"`
	// Timestamp is a Unix timestamp in milliseconds.
	Timestamp float64 `json:"timestamp"`
}

// GrammarFormat names an OpenAI grammar variant for constrained sampling.
type GrammarFormat string

// Grammar formats.
const (
	GrammarFormatOpenAILark  GrammarFormat = "openai_lark"
	GrammarFormatOpenAIRegex GrammarFormat = "openai_regex"
)

// GrammarVariants holds the provider-specific encodings of the same language.
type GrammarVariants map[GrammarFormat]string

// ConstrainedSamplingConfig is the optional provider-side constrained sampling
// config for a tool.
//
// The `json_schema` form roughly maps to the `strict` concept in APIs that
// implement it as json-schema constrained sampling. Grammar variants let callers
// provide provider-specific encodings of the same intended language.
type ConstrainedSamplingConfig struct {
	// Type is "json_schema" or "grammar".
	Type string `json:"type"`
	// Strict is the strictness preference for the json_schema form.
	Strict *ConstrainedStrict `json:"strict,omitempty"`
	// Variants holds the grammar encodings for the grammar form.
	Variants GrammarVariants `json:"variants,omitempty"`
}

// ConstrainedStrict is the strictness preference for json_schema sampling.
type ConstrainedStrict string

// Constrained sampling strictness values.
const (
	ConstrainedStrictPrefer  ConstrainedStrict = "prefer"
	ConstrainedStrictRequire ConstrainedStrict = "require"
)

// NewJSONSchemaSampling builds a json_schema constrained sampling config.
func NewJSONSchemaSampling(strict ConstrainedStrict) ConstrainedSamplingConfig {
	return ConstrainedSamplingConfig{Type: "json_schema", Strict: &strict}
}

// NewGrammarSampling builds a grammar constrained sampling config.
func NewGrammarSampling(variants GrammarVariants) ConstrainedSamplingConfig {
	return ConstrainedSamplingConfig{Type: "grammar", Variants: variants}
}

// ToolInputType discriminates the two supported tool input descriptors.
type ToolInputType string

// Tool input types.
const (
	ToolInputTypeJSONSchema ToolInputType = "json_schema"
	ToolInputTypeGrammar    ToolInputType = "grammar"
)

// ToolInput describes the input contract of a tool.
type ToolInput struct {
	Type ToolInputType `json:"type"`
	// Schema is the JSON Schema document for the json_schema form. It is kept
	// raw so number and keyword semantics survive unchanged.
	Schema json.RawMessage `json:"schema,omitempty"`
	// Grammar is the grammar source for the grammar form.
	Grammar *string `json:"grammar,omitempty"`
}

// JSONSchemaToolInput builds a json_schema tool input.
func JSONSchemaToolInput(schema json.RawMessage) ToolInput {
	return ToolInput{Type: ToolInputTypeJSONSchema, Schema: schema}
}

// GrammarToolInput builds a grammar tool input.
func GrammarToolInput(grammar string) ToolInput {
	return ToolInput{Type: ToolInputTypeGrammar, Grammar: &grammar}
}

// Tool is a tool declaration visible to a model.
type Tool struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Input       ToolInput `json:"input"`
	// ConstrainedSampling is the optional constrained-sampling config. The
	// boolean false upstream means "explicitly disabled" and is represented by
	// the Disabled flag so it stays distinct from an absent config.
	ConstrainedSampling *ConstrainedSamplingConfig `json:"constrainedSampling,omitempty"`
	ConstrainedDisabled bool                       `json:"-"`
}

// toolJSON is the upstream `Tool` wire shape. Upstream exposes the model-facing
// schema as `parameters`; the Go model keeps it in Input.Schema (or a Pith-only
// grammar descriptor), so the two are bridged by Tool's JSON methods.
type toolJSON struct {
	Name                string          `json:"name"`
	Description         string          `json:"description"`
	Parameters          json.RawMessage `json:"parameters"`
	Input               *ToolInput      `json:"input,omitempty"`
	ConstrainedSampling json.RawMessage `json:"constrainedSampling,omitempty"`
}

// MarshalJSON writes the upstream tool shape: the model-facing schema as
// `parameters`. A Pith-only grammar input is preserved in an additional `input`
// field so it round-trips, and `constrainedSampling: false` stays distinct from
// an absent config.
func (t Tool) MarshalJSON() ([]byte, error) {
	wire := toolJSON{Name: t.Name, Description: t.Description}
	if t.Input.Type == ToolInputTypeGrammar {
		input := t.Input
		wire.Input = &input
		wire.Parameters = json.RawMessage("{}")
	} else {
		wire.Parameters = t.Input.Schema
		if len(wire.Parameters) == 0 {
			wire.Parameters = json.RawMessage("{}")
		}
	}
	switch {
	case t.ConstrainedSampling != nil:
		encoded, err := json.Marshal(t.ConstrainedSampling)
		if err != nil {
			return nil, err
		}
		wire.ConstrainedSampling = encoded
	case t.ConstrainedDisabled:
		wire.ConstrainedSampling = json.RawMessage("false")
	}
	return json.Marshal(wire)
}

// UnmarshalJSON accepts the upstream `parameters` field as well as the Pith
// `input` descriptor and the `constrainedSampling: false` sentinel.
func (t *Tool) UnmarshalJSON(data []byte) error {
	var wire toolJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	t.Name = wire.Name
	t.Description = wire.Description
	t.Input = ToolInput{}
	t.ConstrainedSampling = nil
	t.ConstrainedDisabled = false
	if wire.Input != nil {
		t.Input = *wire.Input
	} else if len(wire.Parameters) > 0 {
		t.Input = JSONSchemaToolInput(wire.Parameters)
	}
	if len(wire.ConstrainedSampling) > 0 {
		if string(wire.ConstrainedSampling) == "false" {
			t.ConstrainedDisabled = true
		} else {
			var config ConstrainedSamplingConfig
			if err := json.Unmarshal(wire.ConstrainedSampling, &config); err != nil {
				return err
			}
			t.ConstrainedSampling = &config
		}
	}
	return nil
}

// NewTool builds a tool declaration with a JSON Schema input.
func NewTool(name, description string, schema json.RawMessage) Tool {
	return Tool{Name: name, Description: description, Input: JSONSchemaToolInput(schema)}
}

// ArgsObject decodes the tool call arguments as a JSON object. It returns nil
// when the arguments are absent or not an object, leaving validation to the
// caller.
func (t Tool) ArgsObject(args json.RawMessage) map[string]any {
	if len(args) == 0 {
		return nil
	}
	var object map[string]any
	dec := json.NewDecoder(newBytesReader(args))
	dec.UseNumber()
	if err := dec.Decode(&object); err != nil {
		return nil
	}
	return object
}

// ToolReference is a reference to a tool by name.
type ToolReference struct {
	Name string `json:"name"`
}

// UnmarshalJSON accepts either the documented `{"name": "..."}` form or a
// bare string. In the upstream runtime a bare string is an element without a
// `.name` property, so it names no tool; it is represented here with an empty
// name, which therefore matches no correctly-named tool. This mirrors the
// upstream replay behavior where such an entry deletes nothing.
func (r *ToolReference) UnmarshalJSON(data []byte) error {
	var bare string
	if err := json.Unmarshal(data, &bare); err == nil {
		r.Name = ""
		return nil
	}
	type plain ToolReference
	var object plain
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	*r = ToolReference(object)
	return nil
}

// Context is the request input accepted by the public stream entry points.
//
// SystemPrompt and Tools are shorthand for a leading system message;
// NormalizeContext folds them into one before the request reaches a provider.
type Context struct {
	SystemPrompt *string   `json:"systemPrompt,omitempty"`
	Messages     []Message `json:"messages"`
	Tools        []Tool    `json:"tools,omitempty"`
}

// TranscriptContext is the normalized request context passed to providers and
// API implementations. The prompt and tool declarations are carried by the
// transcript's system messages, so only NormalizeContext produces this type and
// a raw Context cannot reach provider code by accident.
type TranscriptContext struct {
	Messages []Message
}

// NewTranscriptContext builds a transcript context.
func NewTranscriptContext(messages []Message) *TranscriptContext {
	if messages == nil {
		messages = []Message{}
	}
	return &TranscriptContext{Messages: messages}
}

// NormalizeContext folds the systemPrompt/tools shorthand of a raw Context into
// a leading system message, producing the normalized transcript context.
func NormalizeContext(context Context) *TranscriptContext {
	messages := make([]Message, 0, len(context.Messages)+1)
	needsLeading := context.SystemPrompt != nil || len(context.Tools) > 0
	if needsLeading {
		var leading SystemMessage
		if context.SystemPrompt != nil {
			leading = NewSystemMessage(*context.SystemPrompt, 0)
		} else {
			leading = SystemMessage{Role: SystemMessageRole, Content: SystemContentText("")}
		}
		if len(context.Tools) > 0 {
			leading.ToolsAdded = append([]Tool(nil), context.Tools...)
		}
		messages = append(messages, NewSystemMessageVariant(leading))
	}
	messages = append(messages, context.Messages...)
	return NewTranscriptContext(messages)
}

// AssistantMessageEventType discriminates an assistant stream event.
type AssistantMessageEventType string

// Assistant message event types.
const (
	AssistantEventStart         AssistantMessageEventType = "start"
	AssistantEventTextStart     AssistantMessageEventType = "text_start"
	AssistantEventTextDelta     AssistantMessageEventType = "text_delta"
	AssistantEventTextEnd       AssistantMessageEventType = "text_end"
	AssistantEventThinkingStart AssistantMessageEventType = "thinking_start"
	AssistantEventThinkingDelta AssistantMessageEventType = "thinking_delta"
	AssistantEventThinkingEnd   AssistantMessageEventType = "thinking_end"
	AssistantEventToolCallStart AssistantMessageEventType = "toolcall_start"
	AssistantEventToolCallDelta AssistantMessageEventType = "toolcall_delta"
	AssistantEventToolCallEnd   AssistantMessageEventType = "toolcall_end"
	AssistantEventDone          AssistantMessageEventType = "done"
	AssistantEventError         AssistantMessageEventType = "error"
)

// AssistantMessageEvent is one event of the AssistantMessageEventStream
// protocol.
//
// Successful streams emit Start before partial updates and terminate with Done.
// A stream may terminate directly with Error when request setup fails before
// generation starts; after Start, failures also terminate with Error. Updates
// and Done must never appear before Start.
//
// Partial is the shared live response-so-far helper, not an event-time snapshot.
// Text and thinking blocks are empty when their start event is emitted and grow
// only through their delta events until the authoritative end. Redacted thinking
// may be complete at start and emit no deltas. Tool-call arguments at
// toolcall_start are provider-specific; toolcall_delta carries subsequent JSON
// updates.
//
// The variant payloads are flattened into optional fields so a single value type
// can carry the whole protocol without losing ordering or presence.
type AssistantMessageEvent struct {
	Type AssistantMessageEventType `json:"type"`
	// ContentIndex indexes into Partial.Content for the block-scoped events.
	ContentIndex *int `json:"contentIndex,omitempty"`
	// Delta is the incremental text for the delta events.
	Delta *string `json:"delta,omitempty"`
	// ContentBlock is the authoritative block for the end events.
	ContentBlock *string `json:"content,omitempty"`
	// ToolCall is the completed tool call for toolcall_end.
	ToolCall *ToolCall `json:"toolCall,omitempty"`
	// Partial is the live response-so-far.
	Partial *AssistantMessage `json:"partial,omitempty"`
	// Message is the final message for the done event.
	Message *AssistantMessage `json:"message,omitempty"`
	// Reason is the terminal reason for the done and error events.
	Reason *StopReason `json:"reason,omitempty"`
	// Error is the failed message for the error event.
	Error *AssistantMessage `json:"error,omitempty"`
}

// NewStartEvent builds a start event.
func NewStartEvent(partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventStart, Partial: &partial}
}

// NewTextStartEvent builds a text_start event.
func NewTextStartEvent(contentIndex int, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventTextStart, ContentIndex: &contentIndex, Partial: &partial}
}

// NewTextDeltaEvent builds a text_delta event.
func NewTextDeltaEvent(contentIndex int, delta string, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventTextDelta, ContentIndex: &contentIndex, Delta: &delta, Partial: &partial}
}

// NewTextEndEvent builds a text_end event.
func NewTextEndEvent(contentIndex int, content string, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventTextEnd, ContentIndex: &contentIndex, ContentBlock: &content, Partial: &partial}
}

// NewThinkingStartEvent builds a thinking_start event.
func NewThinkingStartEvent(contentIndex int, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventThinkingStart, ContentIndex: &contentIndex, Partial: &partial}
}

// NewThinkingDeltaEvent builds a thinking_delta event.
func NewThinkingDeltaEvent(contentIndex int, delta string, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventThinkingDelta, ContentIndex: &contentIndex, Delta: &delta, Partial: &partial}
}

// NewThinkingEndEvent builds a thinking_end event.
func NewThinkingEndEvent(contentIndex int, content string, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventThinkingEnd, ContentIndex: &contentIndex, ContentBlock: &content, Partial: &partial}
}

// NewToolCallStartEvent builds a toolcall_start event.
func NewToolCallStartEvent(contentIndex int, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventToolCallStart, ContentIndex: &contentIndex, Partial: &partial}
}

// NewToolCallDeltaEvent builds a toolcall_delta event.
func NewToolCallDeltaEvent(contentIndex int, delta string, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventToolCallDelta, ContentIndex: &contentIndex, Delta: &delta, Partial: &partial}
}

// NewToolCallEndEvent builds a toolcall_end event.
func NewToolCallEndEvent(contentIndex int, toolCall ToolCall, partial AssistantMessage) AssistantMessageEvent {
	return AssistantMessageEvent{Type: AssistantEventToolCallEnd, ContentIndex: &contentIndex, ToolCall: &toolCall, Partial: &partial}
}

// NewDoneEvent builds a done event. It panics when reason cannot end a
// successful stream, matching the upstream narrowing of the reason type.
func NewDoneEvent(reason StopReason, message AssistantMessage) AssistantMessageEvent {
	if !reason.IsDoneReason() {
		panic("types: done event reason must be stop, length, toolUse or deferred")
	}
	return AssistantMessageEvent{Type: AssistantEventDone, Reason: &reason, Message: &message}
}

// NewErrorEvent builds an error event. It panics when reason is not an error
// or aborted reason, matching the upstream narrowing of the reason type.
func NewErrorEvent(reason StopReason, err AssistantMessage) AssistantMessageEvent {
	if !reason.IsErrorReason() {
		panic("types: error event reason must be error or aborted")
	}
	return AssistantMessageEvent{Type: AssistantEventError, Reason: &reason, Error: &err}
}

// IsTerminal reports whether the event ends the stream.
func (e AssistantMessageEvent) IsTerminal() bool {
	return e.Type == AssistantEventDone || e.Type == AssistantEventError
}

// AssistantMessageEventStream is an EventStream specialized to the assistant
// message protocol: it completes on the first done or error event and resolves
// the final result to that event's message.
//
// The generic FIFO queue and stream live in packages/ai/utils/eventstream; this
// specialization belongs here because upstream types.ts owns the assistant
// stream class, while event-stream.ts owns only the generic stream. The
// packages/ai/utils package re-exports this type and its factory for extension
// use.
//
// isComplete and extractResult run without the stream's internal lock held, so
// they may safely reenter the stream.
type AssistantMessageEventStream = eventstream.EventStream[AssistantMessageEvent, AssistantMessage]

// NewAssistantMessageEventStream creates an assistant message event stream.
func NewAssistantMessageEventStream() *AssistantMessageEventStream {
	return eventstream.NewEventStream(
		func(event AssistantMessageEvent) bool {
			return event.Type == AssistantEventDone || event.Type == AssistantEventError
		},
		func(event AssistantMessageEvent) AssistantMessage {
			switch event.Type {
			case AssistantEventDone:
				if event.Message != nil {
					return *event.Message
				}
				return AssistantMessage{Role: AssistantMessageRole}
			case AssistantEventError:
				if event.Error != nil {
					return *event.Error
				}
				return AssistantMessage{Role: AssistantMessageRole}
			default:
				panic("types: unexpected event type for the final result")
			}
		},
	)
}

// CreateAssistantMessageEventStream is the factory function for
// AssistantMessageEventStream, for use by extensions.
func CreateAssistantMessageEventStream() *AssistantMessageEventStream {
	return NewAssistantMessageEventStream()
}

// ExtraField is one unknown JSON field preserved on a message.
type ExtraField struct {
	Name  string
	Value json.RawMessage
}

// OpenAICompletionsCompat holds compatibility settings for OpenAI-compatible
// completions APIs.
type OpenAICompletionsCompat struct {
	SupportsStore                               *bool   `json:"supportsStore,omitempty"`
	SupportsDeveloperRole                       *bool   `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort                     *bool   `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming                    *bool   `json:"supportsUsageInStreaming,omitempty"`
	SupportsFinishReason                        *bool   `json:"supportsFinishReason,omitempty"`
	MaxTokensField                              *string `json:"maxTokensField,omitempty"`
	RequiresToolResultName                      *bool   `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult            *bool   `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText                      *bool   `json:"requiresThinkingAsText,omitempty"`
	RequiresReasoningContentOnAssistantMessages *bool   `json:"requiresReasoningContentOnAssistantMessages,omitempty"`
	// ThinkingFormat selects how reasoning/thinking parameters are encoded.
	// "openai" uses reasoning_effort, "openrouter" uses reasoning: { effort },
	// "deepseek" uses thinking: { type } plus reasoning_effort when supported,
	// "together" uses reasoning: { enabled } plus reasoning_effort when
	// supported, "baseten" uses configurable chat_template_args plus
	// reasoning_effort when supported, "zai" uses thinking: { type }, "qwen"
	// uses top-level enable_thinking: boolean, "qwen-chat-template" uses
	// chat_template_kwargs.enable_thinking and preserve_thinking,
	// "chat-template" uses configurable chat_template_kwargs,
	// "string-thinking" uses top-level thinking: string, and "ant-ling" uses
	// reasoning: { effort } only when the mapped effort is non-null.
	ThinkingFormat                 *string                           `json:"thinkingFormat,omitempty"`
	ChatTemplateKwargs             map[string]ChatTemplateKwargValue `json:"chatTemplateKwargs,omitempty"`
	ChatTemplateArgs               map[string]ChatTemplateKwargValue `json:"chatTemplateArgs,omitempty"`
	OpenRouterRouting              *OpenRouterRouting                `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting           *VercelGatewayRouting             `json:"vercelGatewayRouting,omitempty"`
	ZaiToolStream                  *bool                             `json:"zaiToolStream,omitempty"`
	ThinkingTokenBudgetField       *ThinkingTokenBudgetField         `json:"thinkingTokenBudgetField,omitempty"`
	SupportsThinkingTokenBudget    *bool                             `json:"supportsThinkingTokenBudget,omitempty"`
	SupportsOpenAIGrammarTools     *bool                             `json:"supportsOpenAIGrammarTools,omitempty"`
	SupportsMidConvoSystemMessages *bool                             `json:"supportsMidConvoSystemMessages,omitempty"`
	SupportsMidConvoToolAdditions  *bool                             `json:"supportsMidConvoToolAdditions,omitempty"`
	SupportsStrictMode             *bool                             `json:"supportsStrictMode,omitempty"`
	CacheControlFormat             *string                           `json:"cacheControlFormat,omitempty"`
	SendSessionAffinityHeaders     *bool                             `json:"sendSessionAffinityHeaders,omitempty"`
	SessionAffinityFormat          *SessionAffinityFormat            `json:"sessionAffinityFormat,omitempty"`
	SupportsLongCacheRetention     *bool                             `json:"supportsLongCacheRetention,omitempty"`
	VLLMPriority                   *int                              `json:"vllmPriority,omitempty"`
}

// OpenAIResponsesCompat holds compatibility settings for OpenAI Responses APIs.
type OpenAIResponsesCompat struct {
	SupportsDeveloperRole           *bool                  `json:"supportsDeveloperRole,omitempty"`
	SupportsMidConvoSystemMessages  *bool                  `json:"supportsMidConvoSystemMessages,omitempty"`
	SessionAffinityFormat           *SessionAffinityFormat `json:"sessionAffinityFormat,omitempty"`
	SupportsLongCacheRetention      *bool                  `json:"supportsLongCacheRetention,omitempty"`
	SupportsStrictMode              *bool                  `json:"supportsStrictMode,omitempty"`
	SupportsOpenAIGrammarTools      *bool                  `json:"supportsOpenAIGrammarTools,omitempty"`
	SupportsAdditionalTools         *bool                  `json:"supportsAdditionalTools,omitempty"`
	SupportsToolSearch              *bool                  `json:"supportsToolSearch,omitempty"`
	SupportsExplicitPromptCacheMode *bool                  `json:"supportsExplicitPromptCacheMode,omitempty"`
	SupportsMaxOutputTokens         *bool                  `json:"supportsMaxOutputTokens,omitempty"`
}

// AnthropicMessagesCompat holds compatibility settings for Anthropic
// Messages-compatible APIs.
type AnthropicMessagesCompat struct {
	SupportsEagerToolInputStreaming *bool                           `json:"supportsEagerToolInputStreaming,omitempty"`
	SupportsLongCacheRetention      *bool                           `json:"supportsLongCacheRetention,omitempty"`
	SendSessionAffinityHeaders      *bool                           `json:"sendSessionAffinityHeaders,omitempty"`
	SessionAffinityFormat           *string                         `json:"sessionAffinityFormat,omitempty"`
	SupportsCacheControlOnTools     *bool                           `json:"supportsCacheControlOnTools,omitempty"`
	SupportsTemperature             *bool                           `json:"supportsTemperature,omitempty"`
	ForceAdaptiveThinking           *bool                           `json:"forceAdaptiveThinking,omitempty"`
	AllowEmptySignature             *bool                           `json:"allowEmptySignature,omitempty"`
	SupportsStrictTools             *bool                           `json:"supportsStrictTools,omitempty"`
	SupportsMidConvoEffort          *bool                           `json:"supportsMidConvoEffort,omitempty"`
	SupportsMidConvoSystemMessages  *bool                           `json:"supportsMidConvoSystemMessages,omitempty"`
	SupportsMidConvoToolChanges     *bool                           `json:"supportsMidConvoToolChanges,omitempty"`
	AllowedFallbackModels           []AnthropicAllowedFallbackModel `json:"allowedFallbackModels,omitempty"`
}

// BedrockCompat holds compatibility settings for Amazon Bedrock models.
type BedrockCompat struct {
	SupportsStrictMode *bool `json:"supportsStrictMode,omitempty"`
}

// MistralConversationsCompat holds compatibility settings for the Mistral chat API.
type MistralConversationsCompat struct {
	SupportsMidConvoSystemMessages *bool `json:"supportsMidConvoSystemMessages,omitempty"`
}

// OpenRouterRouting holds OpenRouter provider routing preferences. It is sent as
// the `provider` field in the OpenRouter request body.
type OpenRouterRouting struct {
	AllowFallbacks         *bool               `json:"allow_fallbacks,omitempty"`
	RequireParameters      *bool               `json:"require_parameters,omitempty"`
	DataCollection         *string             `json:"data_collection,omitempty"`
	Zdr                    *bool               `json:"zdr,omitempty"`
	EnforceDistillableText *bool               `json:"enforce_distillable_text,omitempty"`
	Order                  []string            `json:"order,omitempty"`
	Only                   []string            `json:"only,omitempty"`
	Ignore                 []string            `json:"ignore,omitempty"`
	Quantizations          []string            `json:"quantizations,omitempty"`
	Sort                   any                 `json:"sort,omitempty"`
	MaxPrice               *OpenRouterMaxPrice `json:"max_price,omitempty"`
	PreferredMinThroughput any                 `json:"preferred_min_throughput,omitempty"`
	PreferredMaxLatency    any                 `json:"preferred_max_latency,omitempty"`
}

// OpenRouterMaxPrice is the maximum price per million tokens.
type OpenRouterMaxPrice struct {
	Prompt     any `json:"prompt,omitempty"`
	Completion any `json:"completion,omitempty"`
	Image      any `json:"image,omitempty"`
	Audio      any `json:"audio,omitempty"`
	Request    any `json:"request,omitempty"`
}

// OpenRouterSortByObject is the object form of the OpenRouter sort option.
type OpenRouterSortByObject struct {
	By        *string `json:"by,omitempty"`
	Partition *string `json:"partition,omitempty"`
}

// OpenRouterPercentiles is the object form of a percentile cutoff option.
type OpenRouterPercentiles struct {
	P50 *float64 `json:"p50,omitempty"`
	P75 *float64 `json:"p75,omitempty"`
	P90 *float64 `json:"p90,omitempty"`
	P99 *float64 `json:"p99,omitempty"`
}

// VercelGatewayRouting holds Vercel AI Gateway routing preferences.
type VercelGatewayRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

// ModelCostRates are per-million-token prices in USD.
type ModelCostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// ModelCostTier is a request-wide pricing tier.
type ModelCostTier struct {
	ModelCostRates
	// InputTokensAbove activates the tier for requests whose total input usage
	// exceeds this token count.
	InputTokensAbove float64 `json:"inputTokensAbove"`
}

// ModelCost is the pricing metadata of a model.
type ModelCost struct {
	ModelCostRates
	// Tiers are request-wide pricing tiers. The highest matching input
	// threshold applies to the full request.
	Tiers []ModelCostTier `json:"tiers,omitempty"`
}

// ModelImageResizeOptions is the cache-safe resize profile for images.
type ModelImageResizeOptions struct {
	MaxWidth  *int `json:"maxWidth,omitempty"`
	MaxHeight *int `json:"maxHeight,omitempty"`
	// MaxBytes is the maximum base64-encoded payload size in bytes.
	MaxBytes    *int `json:"maxBytes,omitempty"`
	JpegQuality *int `json:"jpegQuality,omitempty"`
}

// ModelImageInputLimits bounds the images accepted by a model.
type ModelImageInputLimits struct {
	// Resize is applied before a new image enters conversation history.
	Resize *ModelImageResizeOptions `json:"resize,omitempty"`
	// MaxPerMessage is the maximum number of images accepted in one message.
	MaxPerMessage *int `json:"maxPerMessage,omitempty"`
	// MaxPerRequest is the maximum number of images accepted across one request.
	MaxPerRequest *int `json:"maxPerRequest,omitempty"`
}

// ModelInputLimits bounds the input accepted by a model.
type ModelInputLimits struct {
	// MaxRequestBytes is the maximum serialized provider request size in bytes.
	MaxRequestBytes *int                   `json:"maxRequestBytes,omitempty"`
	Images          *ModelImageInputLimits `json:"images,omitempty"`
}

// ModelInputModality is a model input modality.
type ModelInputModality string

// Model input modalities.
const (
	ModelInputText  ModelInputModality = "text"
	ModelInputImage ModelInputModality = "image"
)

// Model is a model in the unified model system.
type Model struct {
	Id        string     `json:"id"`
	Name      string     `json:"name"`
	Api       Api        `json:"api"`
	Provider  ProviderId `json:"provider"`
	BaseUrl   string     `json:"baseUrl"`
	Reasoning bool       `json:"reasoning"`
	// ThinkingLevelMap maps Pi thinking levels to provider/model-specific
	// values. Missing keys use provider defaults; a nil value marks a level as
	// unsupported.
	ThinkingLevelMap ThinkingLevelMap     `json:"thinkingLevelMap,omitempty"`
	Input            []ModelInputModality `json:"input"`
	// InputLimits holds provider input limits and cache-safe preprocessing
	// metadata.
	InputLimits *ModelInputLimits `json:"inputLimits,omitempty"`
	Cost        ModelCost         `json:"cost"`
	// PromptCache holds prompt cache lifetimes per retention tier. Unset when
	// the provider's cache behavior is unknown.
	PromptCache   *ModelPromptCache `json:"promptCache,omitempty"`
	ContextWindow float64           `json:"contextWindow"`
	MaxTokens     float64           `json:"maxTokens"`
	// SamplingParams are the default sampling parameters for this model.
	SamplingParams map[string]any    `json:"samplingParams,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	// Compat holds compatibility overrides. It is kept as its concrete per-API
	// struct rather than the upstream conditional type.
	Compat Compat `json:"-"`
	// CompatRaw preserves the compat payload verbatim so a model that is read
	// and written back is not silently stripped of settings this package does
	// not model. MarshalJSON writes it when Compat holds no concrete struct.
	CompatRaw json.RawMessage `json:"-"`
}

// Compat is the union of the per-API compatibility settings.
//
// Only the field matching the model's API is meaningful; the rest stay nil, so
// a completions-only override never leaks into an Anthropic request.
type Compat struct {
	OpenAICompletions    *OpenAICompletionsCompat    `json:"-"`
	OpenAIResponses      *OpenAIResponsesCompat      `json:"-"`
	AnthropicMessages    *AnthropicMessagesCompat    `json:"-"`
	Bedrock              *BedrockCompat              `json:"-"`
	MistralConversations *MistralConversationsCompat `json:"-"`
}

// SupportsImageInput reports whether the model accepts image input.
func (m Model) SupportsImageInput() bool {
	for _, modality := range m.Input {
		if modality == ModelInputImage {
			return true
		}
	}
	return false
}

// MarshalJSON flattens the per-API compat struct into the `compat` field, or
// re-emits the verbatim payload when no concrete struct was selected.
func (m Model) MarshalJSON() ([]byte, error) {
	type alias Model
	base := struct {
		alias
		Compat any `json:"compat,omitempty"`
	}{alias: alias(m)}
	switch {
	case m.Compat.OpenAICompletions != nil:
		base.Compat = m.Compat.OpenAICompletions
	case m.Compat.OpenAIResponses != nil:
		base.Compat = m.Compat.OpenAIResponses
	case m.Compat.AnthropicMessages != nil:
		base.Compat = m.Compat.AnthropicMessages
	case m.Compat.Bedrock != nil:
		base.Compat = m.Compat.Bedrock
	case m.Compat.MistralConversations != nil:
		base.Compat = m.Compat.MistralConversations
	case len(m.CompatRaw) > 0:
		base.Compat = m.CompatRaw
	}
	return json.Marshal(base)
}

// UnmarshalJSON restores the per-API compat struct from the `compat` field and
// always keeps the verbatim payload for lossless round trips of unmodeled keys.
func (m *Model) UnmarshalJSON(data []byte) error {
	type alias Model
	aux := struct {
		*alias
		Compat json.RawMessage `json:"compat"`
	}{alias: (*alias)(m)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	m.Compat = Compat{}
	m.CompatRaw = nil
	if len(aux.Compat) == 0 || string(aux.Compat) == "null" {
		return nil
	}
	m.CompatRaw = append(json.RawMessage(nil), aux.Compat...)
	tryUnmarshal := func(target any) bool {
		if err := json.Unmarshal(aux.Compat, target); err != nil {
			return false
		}
		return true
	}
	switch m.Api {
	case ApiOpenAICompletions:
		compat := &OpenAICompletionsCompat{}
		if tryUnmarshal(compat) {
			m.Compat.OpenAICompletions = compat
		}
	case ApiOpenAIResponses, ApiAzureOpenAIResponses, ApiOpenAICodexResponses:
		compat := &OpenAIResponsesCompat{}
		if tryUnmarshal(compat) {
			m.Compat.OpenAIResponses = compat
		}
	case ApiAnthropicMessages:
		compat := &AnthropicMessagesCompat{}
		if tryUnmarshal(compat) {
			m.Compat.AnthropicMessages = compat
		}
	case ApiBedrockConverseStream:
		compat := &BedrockCompat{}
		if tryUnmarshal(compat) {
			m.Compat.Bedrock = compat
		}
	case ApiMistralConversations:
		compat := &MistralConversationsCompat{}
		if tryUnmarshal(compat) {
			m.Compat.MistralConversations = compat
		}
	default:
		compat := &OpenAICompletionsCompat{}
		if tryUnmarshal(compat) {
			m.Compat.OpenAICompletions = compat
		}
	}
	return nil
}

// ImagesModelOutputModality is a model output modality.
type ImagesModelOutputModality string

// Image model output modalities.
const (
	ImagesModelOutputText  ImagesModelOutputModality = "text"
	ImagesModelOutputImage ImagesModelOutputModality = "image"
)

// ImagesModel is the model descriptor used by image-generation providers.
//
// Upstream derives it from Model by omitting api, provider, reasoning,
// contextWindow, maxTokens and compat, then re-declaring api, provider and
// output. The Go form mirrors that: the shared descriptor plus the image fields.
type ImagesModel struct {
	Model
	Output []ImagesModelOutputModality `json:"output"`
}

// MarshalJSON writes the shared model fields plus the image output modalities.
// The embedded Model marshaler is bypassed so Output is not dropped.
func (m ImagesModel) MarshalJSON() ([]byte, error) {
	base, err := m.Model.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(base, &fields); err != nil {
		return nil, err
	}
	output, err := json.Marshal(m.Output)
	if err != nil {
		return nil, err
	}
	fields["output"] = output
	return json.Marshal(fields)
}

// UnmarshalJSON reads the shared model fields and the image output modalities.
func (m *ImagesModel) UnmarshalJSON(data []byte) error {
	if err := m.Model.UnmarshalJSON(data); err != nil {
		return err
	}
	var probe struct {
		Output []ImagesModelOutputModality `json:"output"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	m.Output = probe.Output
	return nil
}
