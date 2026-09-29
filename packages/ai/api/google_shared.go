// This file is a Go port of packages/ai/src/api/google-shared.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The upstream file is the shared conversion layer for the Google Generative AI
// and Google Vertex providers: thinking levels, message/tool conversion, stop
// reasons and the shared retry wrapper. This Go port also carries the streaming
// consumer that both providers feed with their provider-specific HTTP response,
// so the wire behavior lives in exactly one place. The exported helpers map
// one-to-one to the upstream source symbols; the unexported stream helpers are
// Go support code shared by google-generative-ai.go and google-vertex.go.
package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// GoogleApiThinkingLevel is the discrete Gemini thinking level wire value.
type GoogleApiThinkingLevel string

// Google API thinking level values.
const (
	GoogleThinkingLevelUnspecified GoogleApiThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	GoogleThinkingLevelMinimal     GoogleApiThinkingLevel = "MINIMAL"
	GoogleThinkingLevelLow         GoogleApiThinkingLevel = "LOW"
	GoogleThinkingLevelMedium      GoogleApiThinkingLevel = "MEDIUM"
	GoogleThinkingLevelHigh        GoogleApiThinkingLevel = "HIGH"
)

// GoogleSdkThinkingLevel is the @google/genai ThinkingLevel enum value. Its
// string form is identical to GoogleApiThinkingLevel; the distinct type keeps
// the two conversion steps explicit, mirroring the SDK enum.
type GoogleSdkThinkingLevel string

// Google SDK thinking level values.
const (
	GoogleSdkThinkingLevelUnspecified GoogleSdkThinkingLevel = "THINKING_LEVEL_UNSPECIFIED"
	GoogleSdkThinkingLevelMinimal     GoogleSdkThinkingLevel = "MINIMAL"
	GoogleSdkThinkingLevelLow         GoogleSdkThinkingLevel = "LOW"
	GoogleSdkThinkingLevelMedium      GoogleSdkThinkingLevel = "MEDIUM"
	GoogleSdkThinkingLevelHigh        GoogleSdkThinkingLevel = "HIGH"
)

// ResolvedGoogleThinkingLevel is a Pi thinking level restricted to the levels
// Google supports (upstream `Exclude<ThinkingLevel, "xhigh" | "max">`).
type ResolvedGoogleThinkingLevel string

var googleSdkThinkingLevelMap = map[GoogleApiThinkingLevel]GoogleSdkThinkingLevel{
	GoogleThinkingLevelUnspecified: GoogleSdkThinkingLevelUnspecified,
	GoogleThinkingLevelMinimal:     GoogleSdkThinkingLevelMinimal,
	GoogleThinkingLevelLow:         GoogleSdkThinkingLevelLow,
	GoogleThinkingLevelMedium:      GoogleSdkThinkingLevelMedium,
	GoogleThinkingLevelHigh:        GoogleSdkThinkingLevelHigh,
}

var googleGemini3LevelPattern = regexp.MustCompile(`gemini-3(\.[0-9]+)?-(pro|flash)`)
var googleGemma4Pattern = regexp.MustCompile(`gemma-?4`)
var googleGeminiMajorPattern = regexp.MustCompile(`^gemini(-live)?-([0-9]+)`)
var googleSignaturePattern = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

// ResolveGoogleThinkingLevel resolves a supported Pi level, or a model-specific
// Google mapping, to a standard Google level. It returns an error for an
// unsupported mapping, matching the upstream thrown message.
func ResolveGoogleThinkingLevel(model *types.Model, level types.ThinkingLevel) (ResolvedGoogleThinkingLevel, error) {
	mappedValue, present, supported := model.ThinkingLevelMap.Lookup(level)
	resolved := string(level)
	mappedDescription := "undefined"
	if present {
		if supported {
			mappedDescription = mappedValue
			resolved = strings.ToLower(mappedValue)
		} else {
			mappedDescription = "null"
		}
	}
	switch resolved {
	case "minimal", "low", "medium", "high":
		return ResolvedGoogleThinkingLevel(resolved), nil
	default:
		return "", fmt.Errorf("Unsupported Google thinking level mapping for %s/%s: %s -> %s",
			model.Provider, model.Id, level, mappedDescription)
	}
}

// UsesGoogleThinkingLevel reports whether a model uses Gemini's discrete
// thinkingLevel control instead of the token-based thinkingBudget control.
func UsesGoogleThinkingLevel(model *types.Model) bool {
	id := strings.ToLower(model.Id)
	if googleGemini3LevelPattern.MatchString(id) {
		return true
	}
	if id == "gemini-flash-latest" || id == "gemini-flash-lite-latest" {
		return true
	}
	return googleGemma4Pattern.MatchString(id)
}

// ToGoogleThinkingLevel maps a resolved Pi level to the Google wire value.
func ToGoogleThinkingLevel(level ResolvedGoogleThinkingLevel) GoogleApiThinkingLevel {
	switch level {
	case "minimal":
		return GoogleThinkingLevelMinimal
	case "low":
		return GoogleThinkingLevelLow
	case "medium":
		return GoogleThinkingLevelMedium
	case "high":
		return GoogleThinkingLevelHigh
	default:
		return GoogleThinkingLevelUnspecified
	}
}

// ToGoogleSdkThinkingLevel maps a Google wire value to the SDK enum value.
func ToGoogleSdkThinkingLevel(level GoogleApiThinkingLevel) GoogleSdkThinkingLevel {
	if mapped, ok := googleSdkThinkingLevelMap[level]; ok {
		return mapped
	}
	return GoogleSdkThinkingLevelUnspecified
}

// GoogleThinkingConfig is the thinking control sent to the Google API.
type GoogleThinkingConfig struct {
	IncludeThoughts *bool                   `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int                    `json:"thinkingBudget,omitempty"`
	ThinkingLevel   *GoogleSdkThinkingLevel `json:"thinkingLevel,omitempty"`
}

// GetDisabledGoogleThinkingConfig builds the thinking config that disables
// thinking for a model, choosing the token-budget or discrete-level form.
func GetDisabledGoogleThinkingConfig(model *types.Model) (GoogleThinkingConfig, error) {
	if !UsesGoogleThinkingLevel(model) {
		return GoogleThinkingConfig{ThinkingBudget: intPtr(0)}, nil
	}

	fallback := clampThinkingLevel(model, types.ThinkingOff)
	if fallback == types.ThinkingOff {
		return GoogleThinkingConfig{ThinkingBudget: intPtr(0)}, nil
	}

	resolvedLevel, err := ResolveGoogleThinkingLevel(model, fallback)
	if err != nil {
		return GoogleThinkingConfig{}, err
	}
	apiLevel := ToGoogleThinkingLevel(resolvedLevel)
	sdkLevel := ToGoogleSdkThinkingLevel(apiLevel)
	return GoogleThinkingConfig{ThinkingLevel: &sdkLevel}, nil
}

// GoogleInlineData is a base64 inline binary payload (image or other media).
type GoogleInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// GoogleFunctionCall is a model function call part.
//
// Args stays raw so the exact JSON number semantics survive conversion.
type GoogleFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	Id   *string         `json:"id,omitempty"`
}

// GoogleFunctionResponse is a tool result part.
type GoogleFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	Parts    []GooglePart   `json:"parts,omitempty"`
	Id       *string        `json:"id,omitempty"`
}

// GooglePart is one element of a Gemini content part.
type GooglePart struct {
	Text             *string                 `json:"text,omitempty"`
	Thought          *bool                   `json:"thought,omitempty"`
	ThoughtSignature *string                 `json:"thoughtSignature,omitempty"`
	InlineData       *GoogleInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *GoogleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GoogleFunctionResponse `json:"functionResponse,omitempty"`
}

// GoogleContent is a Gemini conversation turn.
type GoogleContent struct {
	Role  string       `json:"role"`
	Parts []GooglePart `json:"parts"`
}

// IsThinkingPart reports whether a streamed part is thinking content.
//
// `thought === true` is the definitive marker. `thoughtSignature` can appear on
// any part type and does not by itself indicate thinking.
func IsThinkingPart(part GooglePart) bool {
	return part.Thought != nil && *part.Thought
}

// RetainThoughtSignature keeps the last non-empty signature for the current
// streamed block. Some backends only send `thoughtSignature` on the first
// delta; later deltas omit it.
func RetainThoughtSignature(existing, incoming *string) *string {
	if incoming != nil && len(*incoming) > 0 {
		return incoming
	}
	return existing
}

func isValidThoughtSignature(signature *string) bool {
	if signature == nil || *signature == "" {
		return false
	}
	if len(*signature)%4 != 0 {
		return false
	}
	return googleSignaturePattern.MatchString(*signature)
}

// resolveThoughtSignature keeps signatures only from the same provider/model
// that are valid base64, matching the Google TYPE_BYTES requirement.
func resolveThoughtSignature(isSameProviderAndModel bool, signature *string) *string {
	if isSameProviderAndModel && isValidThoughtSignature(signature) {
		return signature
	}
	return nil
}

// RequiresToolCallID reports whether a model needs explicit tool call ids in
// function calls and responses.
func RequiresToolCallID(modelId string) bool {
	major := getGeminiMajorVersion(modelId)
	return strings.HasPrefix(modelId, "claude-") ||
		strings.HasPrefix(modelId, "gpt-oss-") ||
		(major != nil && *major >= 3)
}

func getGeminiMajorVersion(modelId string) *int {
	match := googleGeminiMajorPattern.FindStringSubmatch(strings.ToLower(modelId))
	if match == nil {
		return nil
	}
	value := 0
	for _, r := range match[2] {
		value = value*10 + int(r-'0')
	}
	return &value
}

func supportsMultimodalFunctionResponse(modelId string) bool {
	major := getGeminiMajorVersion(modelId)
	if major != nil {
		return *major >= 3
	}
	return true
}

func googleNormalizeToolCallID(id string, model *types.Model, _ types.AssistantMessage) string {
	if model == nil || !RequiresToolCallID(model.Id) {
		return id
	}
	var builder strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	normalized := builder.String()
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	return normalized
}

// GoogleConvertMessages converts internal messages to the Gemini Content[]
// format. Gemini has no mid-conversation system messages, so the leading prompt
// is sent as systemInstruction.
func GoogleConvertMessages(model *types.Model, context *types.TranscriptContext) []GoogleContent {
	if context == nil {
		context = types.NewTranscriptContext(nil)
	}
	collapsed := utils.CollapseSystemMessages(*context)
	conversation := utils.WithoutInitialSystemMessage(collapsed.Messages)

	transformedMessages := TransformMessages(conversation, model, googleNormalizeToolCallID)
	contents := []GoogleContent{}

	for _, msg := range transformedMessages {
		switch msg.Role {
		case types.UserMessageRole:
			if msg.User == nil {
				continue
			}
			if !msg.User.Content.Structured {
				contents = append(contents, GoogleContent{
					Role:  "user",
					Parts: []GooglePart{{Text: stringPointer(utils.SanitizeSurrogates(msg.User.Content.Text))}},
				})
				continue
			}
			parts := []GooglePart{}
			for _, item := range msg.User.Content.Blocks {
				switch item.Type {
				case types.ContentTypeText:
					if item.Text != nil {
						parts = append(parts, GooglePart{Text: stringPointer(utils.SanitizeSurrogates(item.Text.Text))})
					}
				case types.ContentTypeImage:
					if item.Image != nil {
						parts = append(parts, GooglePart{InlineData: &GoogleInlineData{
							MimeType: item.Image.MimeType,
							Data:     item.Image.Data,
						}})
					}
				}
			}
			if len(parts) == 0 {
				continue
			}
			contents = append(contents, GoogleContent{Role: "user", Parts: parts})

		case types.AssistantMessageRole:
			if msg.Assistant == nil {
				continue
			}
			assistant := msg.Assistant
			isSameProviderAndModel := assistant.Provider == model.Provider && assistant.Model == model.Id
			parts := []GooglePart{}

			for _, block := range assistant.Content {
				switch block.Type {
				case types.ContentTypeText:
					text := ""
					if block.Text != nil {
						text = block.Text.Text
					}
					var rawSignature *string
					if block.Text != nil {
						rawSignature = block.Text.TextSignature
					}
					thoughtSignature := resolveThoughtSignature(isSameProviderAndModel, rawSignature)
					// Skip empty text blocks unless they carry a thought
					// signature: Gemini can attach the signature to a part whose
					// visible text is empty and requires it echoed back.
					if (text == "" || isBlank(text)) && thoughtSignature == nil {
						continue
					}
					part := GooglePart{Text: stringPointer(utils.SanitizeSurrogates(text))}
					if thoughtSignature != nil {
						part.ThoughtSignature = thoughtSignature
					}
					parts = append(parts, part)

				case types.ContentTypeThinking:
					thinking := ""
					var rawSignature *string
					if block.Thinking != nil {
						thinking = block.Thinking.Thinking
						rawSignature = block.Thinking.ThinkingSignature
					}
					if isSameProviderAndModel {
						thoughtSignature := resolveThoughtSignature(true, rawSignature)
						if (thinking == "" || isBlank(thinking)) && thoughtSignature == nil {
							continue
						}
						part := GooglePart{
							Thought: boolPointer(true),
							Text:    stringPointer(utils.SanitizeSurrogates(thinking)),
						}
						if thoughtSignature != nil {
							part.ThoughtSignature = thoughtSignature
						}
						parts = append(parts, part)
					} else {
						if thinking == "" || isBlank(thinking) {
							continue
						}
						parts = append(parts, GooglePart{Text: stringPointer(utils.SanitizeSurrogates(thinking))})
					}

				case types.ContentTypeToolCall:
					if block.ToolCall == nil {
						continue
					}
					call := block.ToolCall
					thoughtSignature := resolveThoughtSignature(isSameProviderAndModel, call.ThoughtSignature)
					args := call.Arguments
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					part := GooglePart{FunctionCall: &GoogleFunctionCall{Name: call.Name, Args: args}}
					if RequiresToolCallID(model.Id) {
						id := call.Id
						part.FunctionCall.Id = &id
					}
					if thoughtSignature != nil {
						part.ThoughtSignature = thoughtSignature
					}
					parts = append(parts, part)
				}
			}

			if len(parts) == 0 {
				continue
			}
			contents = append(contents, GoogleContent{Role: "model", Parts: parts})

		case types.ToolResultMessageRole:
			if msg.ToolResult == nil {
				continue
			}
			result := msg.ToolResult
			textParts := []string{}
			imageParts := []GooglePart{}
			for _, block := range result.Content {
				switch block.Type {
				case types.ContentTypeText:
					if block.Text != nil {
						textParts = append(textParts, block.Text.Text)
					}
				case types.ContentTypeImage:
					if block.Image != nil && modelSupportsImages(model) {
						imageParts = append(imageParts, GooglePart{InlineData: &GoogleInlineData{
							MimeType: block.Image.MimeType,
							Data:     block.Image.Data,
						}})
					}
				}
			}
			textResult := strings.Join(textParts, "\n")
			hasText := len(textResult) > 0
			hasImages := len(imageParts) > 0

			responseValue := ""
			switch {
			case hasText:
				responseValue = utils.SanitizeSurrogates(textResult)
			case hasImages:
				responseValue = "(see attached image)"
			}

			response := map[string]any{"output": responseValue}
			if result.IsError {
				response = map[string]any{"error": responseValue}
			}

			functionResponse := &GoogleFunctionResponse{Name: result.ToolName, Response: response}
			if hasImages && supportsMultimodalFunctionResponse(model.Id) {
				functionResponse.Parts = imageParts
			}
			if RequiresToolCallID(model.Id) {
				id := result.ToolCallId
				functionResponse.Id = &id
			}
			functionResponsePart := GooglePart{FunctionResponse: functionResponse}

			// Cloud Code Assist requires all function responses in a single
			// user turn; merge into the previous user turn when it already holds
			// function responses.
			merged := false
			if len(contents) > 0 {
				last := &contents[len(contents)-1]
				if last.Role == "user" {
					for _, part := range last.Parts {
						if part.FunctionResponse != nil {
							last.Parts = append(last.Parts, functionResponsePart)
							merged = true
							break
						}
					}
				}
			}
			if !merged {
				contents = append(contents, GoogleContent{Role: "user", Parts: []GooglePart{functionResponsePart}})
			}

			// Gemini < 3 does not support multimodal function responses, so
			// images go in a separate user message.
			if hasImages && !supportsMultimodalFunctionResponse(model.Id) {
				parts := []GooglePart{{Text: stringPointer("Tool result image:")}}
				parts = append(parts, imageParts...)
				contents = append(contents, GoogleContent{Role: "user", Parts: parts})
			}
		}
	}

	return contents
}

var googleJSONSchemaMetaDeclarations = map[string]bool{
	"$schema":        true,
	"$id":            true,
	"$anchor":        true,
	"$dynamicAnchor": true,
	"$vocabulary":    true,
	"$comment":       true,
	"$defs":          true,
	"definitions":    true,
}

// sanitizeForOpenAPI strips JSON Schema meta-declarations from a schema.
func sanitizeForOpenAPI(schema any) any {
	switch value := schema.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, item := range value {
			if googleJSONSchemaMetaDeclarations[key] {
				continue
			}
			result[key] = sanitizeForOpenAPI(item)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i := range value {
			result[i] = sanitizeForOpenAPI(value[i])
		}
		return result
	default:
		return schema
	}
}

// GoogleToolDeclaration is one function declaration in a Gemini tool group.
type GoogleToolDeclaration struct {
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Parameters           json.RawMessage `json:"parameters,omitempty"`
	ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

// GoogleToolGroup is a Gemini tools entry holding function declarations.
type GoogleToolGroup struct {
	FunctionDeclarations []GoogleToolDeclaration `json:"functionDeclarations"`
}

// GoogleConvertTools converts tools to the Gemini function declarations format.
//
// By default it uses `parametersJsonSchema`, which supports full JSON Schema.
// When useParameters is true it uses the legacy `parameters` field (OpenAPI
// 3.03 Schema) with JSON Schema meta-declarations stripped.
func GoogleConvertTools(tools []types.Tool, useParameters bool, supportsStrictMode bool) ([]GoogleToolGroup, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	declarations := make([]GoogleToolDeclaration, 0, len(tools))
	for _, tool := range tools {
		strict, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		parameters, err := GetJSONSchemaToolParameters(tool, strict)
		if err != nil {
			return nil, err
		}
		declaration := GoogleToolDeclaration{Name: tool.Name, Description: tool.Description}
		if useParameters {
			sanitized, err := sanitizeParametersForOpenAPI(parameters)
			if err != nil {
				return nil, err
			}
			declaration.Parameters = sanitized
		} else {
			declaration.ParametersJSONSchema = parameters
		}
		declarations = append(declarations, declaration)
	}
	return []GoogleToolGroup{{FunctionDeclarations: declarations}}, nil
}

func sanitizeParametersForOpenAPI(parameters json.RawMessage) (json.RawMessage, error) {
	if len(parameters) == 0 {
		return json.RawMessage("{}"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(parameters))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(sanitizeForOpenAPI(decoded))
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// SupportsGoogleStrictToolSampling reports whether a model enforces required
// function parameters in validated tool-calling modes.
func SupportsGoogleStrictToolSampling(modelId string) bool {
	major := getGeminiMajorVersion(modelId)
	return major != nil && *major >= 3
}

// GoogleFunctionCallingConfigMode is the Gemini function-calling mode.
type GoogleFunctionCallingConfigMode string

// Gemini function-calling modes.
const (
	GoogleFunctionCallingAuto      GoogleFunctionCallingConfigMode = "AUTO"
	GoogleFunctionCallingNone      GoogleFunctionCallingConfigMode = "NONE"
	GoogleFunctionCallingAny       GoogleFunctionCallingConfigMode = "ANY"
	GoogleFunctionCallingValidated GoogleFunctionCallingConfigMode = "VALIDATED"
)

// MapGoogleToolChoice maps a tool choice string to a Gemini mode.
func MapGoogleToolChoice(choice string) GoogleFunctionCallingConfigMode {
	switch choice {
	case "auto":
		return GoogleFunctionCallingAuto
	case "none":
		return GoogleFunctionCallingNone
	case "any":
		return GoogleFunctionCallingAny
	default:
		return GoogleFunctionCallingAuto
	}
}

// ResolveGoogleFunctionCallingMode resolves the function-calling mode from the
// tool set and tool choice, using VALIDATED for strict tools.
func ResolveGoogleFunctionCallingMode(tools []types.Tool, toolChoice *string, supportsStrictMode bool) (*GoogleFunctionCallingConfigMode, error) {
	useStrictMode := false
	for _, tool := range tools {
		strict, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		if strict != nil && *strict {
			useStrictMode = true
			break
		}
	}
	if toolChoice != nil && (*toolChoice == "none" || *toolChoice == "any") {
		mode := MapGoogleToolChoice(*toolChoice)
		return &mode, nil
	}
	if useStrictMode {
		mode := GoogleFunctionCallingValidated
		return &mode, nil
	}
	if toolChoice != nil {
		mode := MapGoogleToolChoice(*toolChoice)
		return &mode, nil
	}
	return nil, nil
}

// GoogleFinishReason is a Gemini finish reason wire value.
type GoogleFinishReason string

// Gemini finish reason values.
const (
	GoogleFinishReasonStop             GoogleFinishReason = "STOP"
	GoogleFinishReasonMaxTokens        GoogleFinishReason = "MAX_TOKENS"
	GoogleFinishReasonBlocklist        GoogleFinishReason = "BLOCKLIST"
	GoogleFinishReasonProhibited       GoogleFinishReason = "PROHIBITED_CONTENT"
	GoogleFinishReasonSpii             GoogleFinishReason = "SPII"
	GoogleFinishReasonSafety           GoogleFinishReason = "SAFETY"
	GoogleFinishReasonImageSafety      GoogleFinishReason = "IMAGE_SAFETY"
	GoogleFinishReasonImageProhibited  GoogleFinishReason = "IMAGE_PROHIBITED_CONTENT"
	GoogleFinishReasonImageRecitation  GoogleFinishReason = "IMAGE_RECITATION"
	GoogleFinishReasonImageOther       GoogleFinishReason = "IMAGE_OTHER"
	GoogleFinishReasonRecitation       GoogleFinishReason = "RECITATION"
	GoogleFinishReasonUnspecified      GoogleFinishReason = "FINISH_REASON_UNSPECIFIED"
	GoogleFinishReasonOther            GoogleFinishReason = "OTHER"
	GoogleFinishReasonLanguage         GoogleFinishReason = "LANGUAGE"
	GoogleFinishReasonMalformed        GoogleFinishReason = "MALFORMED_FUNCTION_CALL"
	GoogleFinishReasonUnexpectedCall   GoogleFinishReason = "UNEXPECTED_TOOL_CALL"
	GoogleFinishReasonTooManyToolCalls GoogleFinishReason = "TOO_MANY_TOOL_CALLS"
	GoogleFinishReasonNoImage          GoogleFinishReason = "NO_IMAGE"
)

// MapGoogleStopReason maps a Gemini finish reason to a Pi stop reason.
func MapGoogleStopReason(reason GoogleFinishReason) (types.StopReason, error) {
	switch reason {
	case GoogleFinishReasonStop:
		return types.StopReasonStop, nil
	case GoogleFinishReasonMaxTokens:
		return types.StopReasonLength, nil
	case GoogleFinishReasonBlocklist,
		GoogleFinishReasonProhibited,
		GoogleFinishReasonSpii,
		GoogleFinishReasonSafety,
		GoogleFinishReasonImageSafety,
		GoogleFinishReasonImageProhibited,
		GoogleFinishReasonImageRecitation,
		GoogleFinishReasonImageOther,
		GoogleFinishReasonRecitation,
		GoogleFinishReasonUnspecified,
		GoogleFinishReasonOther,
		GoogleFinishReasonLanguage,
		GoogleFinishReasonMalformed,
		GoogleFinishReasonUnexpectedCall,
		GoogleFinishReasonTooManyToolCalls,
		GoogleFinishReasonNoImage:
		return types.StopReasonError, nil
	default:
		return "", fmt.Errorf("Unhandled stop reason: %s", reason)
	}
}

// MapGoogleStopReasonString maps a raw finish-reason string to a Pi stop reason.
func MapGoogleStopReasonString(reason string) types.StopReason {
	switch reason {
	case "STOP":
		return types.StopReasonStop
	case "MAX_TOKENS":
		return types.StopReasonLength
	default:
		return types.StopReasonError
	}
}

// RetryGoogleRequest runs a Google request with the shared provider retry
// policy. The SDK's ApiError carries a status but no headers; this wrapper
// leaves the headers absent so retry classification falls back to the status.
func RetryGoogleRequest[T any](ctx context.Context, request func() (T, error), options *types.StreamOptions) (T, error) {
	var maxRetries *int
	var maxRetryDelayMs *float64
	if options != nil {
		maxRetries = options.MaxRetries
		if options.MaxRetryDelayMs != nil {
			value := float64(*options.MaxRetryDelayMs)
			maxRetryDelayMs = &value
		}
	}
	return utils.RetryProviderRequest(ctx, request, utils.ProviderRetryOptions{
		MaxRetries:      maxRetries,
		MaxRetryDelayMs: maxRetryDelayMs,
		Signal:          ctx,
	})
}

// =============================================================================
// Go-only support helpers
// =============================================================================

var googleToolCallCounter int64

func stringPointer(value string) *string { return &value }

func boolPointer(value bool) *bool { return &value }

func googleFloatOrZero(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func hasGoogleToolCallWithID(output *types.AssistantMessage, id string) bool {
	for _, block := range output.Content {
		if block.Type == types.ContentTypeToolCall && block.ToolCall != nil && block.ToolCall.Id == id {
			return true
		}
	}
	return false
}

func compactGoogleArguments(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

// GoogleSystemInstruction is the leading system prompt sent to Gemini.
type GoogleSystemInstruction struct {
	Role  string       `json:"role"`
	Parts []GooglePart `json:"parts"`
}

// GoogleGenerationConfig is the Gemini generationConfig payload.
type GoogleGenerationConfig struct {
	Temperature     *float64              `json:"temperature,omitempty"`
	MaxOutputTokens *int                  `json:"maxOutputTokens,omitempty"`
	ThinkingConfig  *GoogleThinkingConfig `json:"thinkingConfig,omitempty"`
}

// GoogleToolConfig is the Gemini toolConfig payload.
type GoogleToolConfig struct {
	FunctionCallingConfig *GoogleFunctionCallingConfig `json:"functionCallingConfig"`
}

// GoogleFunctionCallingConfig selects the function-calling mode.
type GoogleFunctionCallingConfig struct {
	Mode GoogleFunctionCallingConfigMode `json:"mode"`
}

// GoogleGenerateContentRequest is the REST body sent to the Google endpoint.
type GoogleGenerateContentRequest struct {
	Contents          []GoogleContent          `json:"contents"`
	SystemInstruction *GoogleSystemInstruction `json:"systemInstruction,omitempty"`
	Tools             []GoogleToolGroup        `json:"tools,omitempty"`
	ToolConfig        *GoogleToolConfig        `json:"toolConfig,omitempty"`
	GenerationConfig  *GoogleGenerationConfig  `json:"generationConfig,omitempty"`
}

// googleBuildInput carries the provider-neutral options both Google providers
// project into the REST body.
type googleBuildInput struct {
	Temperature *float64
	MaxTokens   *int
	ToolChoice  *string
	Thinking    *GoogleThinkingOptions
	Signal      <-chan struct{}
}

// buildGoogleGenerateRequest builds the Gemini REST request body shared by both
// providers. It returns an error when the request is already aborted.
func buildGoogleGenerateRequest(model *types.Model, context *types.TranscriptContext, input googleBuildInput) (*GoogleGenerateContentRequest, error) {
	contents := GoogleConvertMessages(model, context)
	initialSystemMessage := utils.GetInitialSystemMessage(context.Messages)
	currentTools := utils.GetCurrentTools(context.Messages)

	supportsStrictMode := SupportsGoogleStrictToolSampling(model.Id)
	var functionCallingMode *GoogleFunctionCallingConfigMode
	if len(currentTools) > 0 {
		mode, err := ResolveGoogleFunctionCallingMode(currentTools, input.ToolChoice, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		functionCallingMode = mode
	}

	request := &GoogleGenerateContentRequest{Contents: contents}

	systemInstruction := ""
	if initialSystemMessage != nil {
		systemInstruction = utils.GetSystemMessageText(*initialSystemMessage)
	}
	if systemInstruction != "" {
		request.SystemInstruction = &GoogleSystemInstruction{
			Role:  "user",
			Parts: []GooglePart{{Text: stringPointer(utils.SanitizeSurrogates(systemInstruction))}},
		}
	}

	if len(currentTools) > 0 {
		tools, err := GoogleConvertTools(currentTools, false, supportsStrictMode)
		if err != nil {
			return nil, err
		}
		request.Tools = tools
	}
	if functionCallingMode != nil {
		request.ToolConfig = &GoogleToolConfig{
			FunctionCallingConfig: &GoogleFunctionCallingConfig{Mode: *functionCallingMode},
		}
	}

	generationConfig := GoogleGenerationConfig{}
	if input.Temperature != nil {
		generationConfig.Temperature = input.Temperature
	}
	if input.MaxTokens != nil {
		generationConfig.MaxOutputTokens = input.MaxTokens
	}

	if input.Thinking != nil && input.Thinking.Enabled && model.Reasoning {
		thinkingConfig := GoogleThinkingConfig{IncludeThoughts: boolPointer(true)}
		if input.Thinking.Level != nil {
			level := ToGoogleSdkThinkingLevel(*input.Thinking.Level)
			thinkingConfig.ThinkingLevel = &level
		} else if input.Thinking.BudgetTokens != nil {
			thinkingConfig.ThinkingBudget = input.Thinking.BudgetTokens
		}
		generationConfig.ThinkingConfig = &thinkingConfig
	} else if model.Reasoning && input.Thinking != nil && !input.Thinking.Enabled {
		disabled, err := GetDisabledGoogleThinkingConfig(model)
		if err != nil {
			return nil, err
		}
		generationConfig.ThinkingConfig = &disabled
	}

	if generationConfig.Temperature != nil || generationConfig.MaxOutputTokens != nil || generationConfig.ThinkingConfig != nil {
		request.GenerationConfig = &generationConfig
	}

	if input.Signal != nil && aborted(input.Signal) {
		return nil, fmt.Errorf("Request aborted")
	}

	return request, nil
}

// googleGenerateContentResponse is one decoded SSE chunk.
type googleGenerateContentResponse struct {
	ResponseId    *string              `json:"responseId"`
	Candidates    []googleCandidate    `json:"candidates"`
	UsageMetadata *googleUsageMetadata `json:"usageMetadata"`
}

type googleCandidate struct {
	Content      *googleCandidateContent `json:"content"`
	FinishReason *string                 `json:"finishReason"`
}

type googleCandidateContent struct {
	Role  string       `json:"role"`
	Parts []GooglePart `json:"parts"`
}

type googleUsageMetadata struct {
	PromptTokenCount        *float64 `json:"promptTokenCount"`
	CachedContentTokenCount *float64 `json:"cachedContentTokenCount"`
	CandidatesTokenCount    *float64 `json:"candidatesTokenCount"`
	ThoughtsTokenCount      *float64 `json:"thoughtsTokenCount"`
	TotalTokenCount         *float64 `json:"totalTokenCount"`
}

// iterateGoogleSSE parses a server-sent event stream and invokes yield with the
// raw `data:` payload of every frame. `[DONE]` is ignored.
func iterateGoogleSSE(ctx context.Context, body io.Reader, signal <-chan struct{}, yield func([]byte) error) error {
	reader := bufio.NewReaderSize(body, 64*1024)
	var data strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if aborted(signal) {
			return fmt.Errorf("Request was aborted")
		}
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				if data.Len() > 0 {
					payload := strings.TrimSpace(data.String())
					data.Reset()
					if payload != "" && payload != "[DONE]" {
						if yieldErr := yield([]byte(payload)); yieldErr != nil {
							return yieldErr
						}
					}
				}
			case strings.HasPrefix(trimmed, "data:"):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			if data.Len() > 0 {
				payload := strings.TrimSpace(data.String())
				if payload != "" && payload != "[DONE]" {
					if yieldErr := yield([]byte(payload)); yieldErr != nil {
						return yieldErr
					}
				}
			}
			return nil
		}
	}
}

// consumeGoogleStream processes the SSE response, emitting the assistant stream
// events and mutating output. It closes the trailing open block at EOF.
func consumeGoogleStream(ctx context.Context, model *types.Model, stream *types.AssistantMessageEventStream, output *types.AssistantMessage, body io.Reader, signal <-chan struct{}) error {
	currentKind := ""
	var textBlock *types.TextContent
	var thinkingBlock *types.ThinkingContent

	closeCurrent := func() {
		switch currentKind {
		case "text":
			content := ""
			if textBlock != nil {
				content = textBlock.Text
			}
			index := len(output.Content) - 1
			stream.Push(types.NewTextEndEvent(index, content, *output))
		case "thinking":
			content := ""
			if thinkingBlock != nil {
				content = thinkingBlock.Thinking
			}
			index := len(output.Content) - 1
			stream.Push(types.NewThinkingEndEvent(index, content, *output))
		}
		currentKind = ""
	}

	err := iterateGoogleSSE(ctx, body, signal, func(payload []byte) error {
		var chunk googleGenerateContentResponse
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return fmt.Errorf("Invalid Google SSE JSON: %w", err)
		}

		// responseId is output-only; keep the first non-empty value.
		if (output.ResponseId == nil || *output.ResponseId == "") && chunk.ResponseId != nil {
			output.ResponseId = chunk.ResponseId
		}

		if len(chunk.Candidates) > 0 {
			candidate := chunk.Candidates[0]
			if candidate.Content != nil {
				for _, part := range candidate.Content.Parts {
					if part.Text != nil {
						isThinking := IsThinkingPart(part)
						if currentKind == "" ||
							(isThinking && currentKind != "thinking") ||
							(!isThinking && currentKind != "text") {
							if currentKind != "" {
								closeCurrent()
							}
							if isThinking {
								block := &types.ThinkingContent{Type: types.ContentTypeThinking, Thinking: ""}
								output.Content = append(output.Content, types.ContentBlock{Type: types.ContentTypeThinking, Thinking: block})
								thinkingBlock = block
								currentKind = "thinking"
								stream.Push(types.NewThinkingStartEvent(len(output.Content)-1, *output))
							} else {
								block := &types.TextContent{Type: types.ContentTypeText, Text: ""}
								output.Content = append(output.Content, types.ContentBlock{Type: types.ContentTypeText, Text: block})
								textBlock = block
								currentKind = "text"
								stream.Push(types.NewTextStartEvent(len(output.Content)-1, *output))
							}
						}
						if currentKind == "thinking" {
							thinkingBlock.Thinking += *part.Text
							thinkingBlock.ThinkingSignature = RetainThoughtSignature(thinkingBlock.ThinkingSignature, part.ThoughtSignature)
							stream.Push(types.NewThinkingDeltaEvent(len(output.Content)-1, *part.Text, *output))
						} else {
							textBlock.Text += *part.Text
							textBlock.TextSignature = RetainThoughtSignature(textBlock.TextSignature, part.ThoughtSignature)
							stream.Push(types.NewTextDeltaEvent(len(output.Content)-1, *part.Text, *output))
						}
					}

					if part.FunctionCall != nil {
						if currentKind != "" {
							closeCurrent()
						}
						providedID := part.FunctionCall.Id
						needsNewID := providedID == nil || hasGoogleToolCallWithID(output, *providedID)
						toolCallID := ""
						if needsNewID {
							counter := atomic.AddInt64(&googleToolCallCounter, 1)
							toolCallID = fmt.Sprintf("%s_%d_%d", part.FunctionCall.Name, int64(nowMillis()), counter)
						} else {
							toolCallID = *providedID
						}
						args := part.FunctionCall.Args
						if len(args) == 0 {
							args = json.RawMessage("{}")
						}
						toolCall := types.ToolCall{
							Type:      types.ContentTypeToolCall,
							Id:        toolCallID,
							Name:      part.FunctionCall.Name,
							Arguments: args,
						}
						if part.ThoughtSignature != nil {
							toolCall.ThoughtSignature = part.ThoughtSignature
						}
						output.Content = append(output.Content, types.ToolCallBlock(toolCall))
						index := len(output.Content) - 1
						stream.Push(types.NewToolCallStartEvent(index, *output))
						stream.Push(types.NewToolCallDeltaEvent(index, compactGoogleArguments(args), *output))
						stream.Push(types.NewToolCallEndEvent(index, toolCall, *output))
					}
				}
			}

			if candidate.FinishReason != nil {
				output.RawStopReason = candidate.FinishReason
				reason, err := MapGoogleStopReason(GoogleFinishReason(*candidate.FinishReason))
				if err != nil {
					return err
				}
				output.StopReason = reason
				hasToolCall := false
				for _, block := range output.Content {
					if block.Type == types.ContentTypeToolCall {
						hasToolCall = true
						break
					}
				}
				if hasToolCall && output.StopReason == types.StopReasonStop {
					output.StopReason = types.StopReasonToolUse
				}
			}
		}

		if chunk.UsageMetadata != nil {
			prompt := googleFloatOrZero(chunk.UsageMetadata.PromptTokenCount)
			cached := googleFloatOrZero(chunk.UsageMetadata.CachedContentTokenCount)
			candidates := googleFloatOrZero(chunk.UsageMetadata.CandidatesTokenCount)
			thoughts := googleFloatOrZero(chunk.UsageMetadata.ThoughtsTokenCount)
			output.Usage = types.Usage{
				Input:       prompt - cached,
				Output:      candidates + thoughts,
				CacheRead:   cached,
				CacheWrite:  0,
				Reasoning:   &thoughts,
				TotalTokens: googleFloatOrZero(chunk.UsageMetadata.TotalTokenCount),
			}
			calculateCost(model, &output.Usage)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if currentKind != "" {
		closeCurrent()
	}
	return nil
}

// failGoogle terminates the stream with an error message.
func failGoogle(stream *types.AssistantMessageEventStream, output *types.AssistantMessage, err error, isAborted bool) {
	if isAborted {
		output.StopReason = types.StopReasonAborted
	} else {
		output.StopReason = types.StopReasonError
	}
	message := fmtProviderError(err, nil)
	output.ErrorMessage = &message
	stream.Push(types.NewErrorEvent(output.StopReason, *output))
	stream.End(output)
}

// finalizeGoogleStream performs the post-response checks and terminates the
// stream with a done event, or an error when the stop reason is not successful.
func finalizeGoogleStream(stream *types.AssistantMessageEventStream, output *types.AssistantMessage, signal <-chan struct{}, pendingMessage string) {
	if aborted(signal) {
		failGoogle(stream, output, fmt.Errorf("Request was aborted"), true)
		return
	}
	if output.StopReason == types.StopReasonPending {
		failGoogle(stream, output, fmt.Errorf("%s", pendingMessage), false)
		return
	}
	if output.StopReason == types.StopReasonAborted || output.StopReason == types.StopReasonError {
		message := "An unknown error occurred"
		if output.RawStopReason != nil {
			message = "Provider stopped with: " + *output.RawStopReason
		}
		failGoogle(stream, output, fmt.Errorf("%s", message), false)
		return
	}
	stream.Push(types.NewDoneEvent(output.StopReason, *output))
	stream.End(output)
}

// googlePerformRequest performs an HTTP request and classifies non-2xx
// responses as provider errors so the retry policy can act on them.
func googlePerformRequest(ctx context.Context, options *types.ProviderRequestOptions, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	request := func() (*http.Response, error) {
		response, err := doProviderRequest(ctx, options, method, url, headers, body)
		if err != nil {
			return nil, err
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return response, nil
		}
		raw, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		status := response.StatusCode
		return nil, &utils.ProviderError{
			Err:    fmt.Errorf("%d: %s", status, strings.TrimSpace(string(raw))),
			Status: &status,
		}
	}

	var retryOptions *types.StreamOptions
	if options != nil {
		retryOptions = &types.StreamOptions{ProviderRequestOptions: *options}
	}
	return RetryGoogleRequest(ctx, request, retryOptions)
}
