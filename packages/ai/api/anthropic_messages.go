// This file is a Go port of packages/ai/src/api/anthropic-messages.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The upstream module talks to the @anthropic-ai/sdk Beta Messages endpoint.
// The Go port keeps the observable request/response contract (the beta
// `/v1/messages?beta=true` endpoint, streaming SSE frames, thinking,
// adaptive thinking, tool search, eager tool input, cache control and native
// tool changes) and speaks HTTP directly. The injectable `client` becomes the
// AnthropicClient interface so callers and tests can substitute a transport
// without importing the TypeScript SDK.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// AnthropicEffort is the adaptive-thinking effort level.
type AnthropicEffort string

// Adaptive-thinking effort levels.
const (
	AnthropicEffortLow    AnthropicEffort = "low"
	AnthropicEffortMedium AnthropicEffort = "medium"
	AnthropicEffortHigh   AnthropicEffort = "high"
	AnthropicEffortXHigh  AnthropicEffort = "xhigh"
	AnthropicEffortMax    AnthropicEffort = "max"
)

// AnthropicThinkingDisplay controls how thinking content is returned.
type AnthropicThinkingDisplay string

// Thinking display modes.
const (
	AnthropicThinkingDisplaySummarized AnthropicThinkingDisplay = "summarized"
	AnthropicThinkingDisplayOmitted    AnthropicThinkingDisplay = "omitted"
)

// AnthropicToolChoice is the forced-tool form of the Anthropic tool choice.
type AnthropicToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// AnthropicClient is the injectable Anthropic transport. When set on
// AnthropicOptions it replaces internal client construction entirely, mirroring
// the upstream `client?: Anthropic` option. Implementations receive the
// already-built request body and return the raw HTTP response.
type AnthropicClient interface {
	CreateMessage(ctx context.Context, params map[string]any, headers map[string]string) (*http.Response, error)
}

// AnthropicOptions are the Anthropic Messages-specific stream options.
type AnthropicOptions struct {
	types.StreamOptions

	// ThinkingEnabled enables extended thinking. For adaptive-thinking models
	// the model decides when/how much to think; older models use a token
	// budget.
	ThinkingEnabled *bool
	// ThinkingBudgetTokens is the token budget for older thinking models.
	ThinkingBudgetTokens *int
	// Effort is the adaptive-thinking effort level.
	Effort *AnthropicEffort
	// ThinkingDisplay controls how thinking content is returned.
	ThinkingDisplay *AnthropicThinkingDisplay
	// InterleavedThinking requests the interleaved-thinking beta for
	// non-adaptive thinking models.
	InterleavedThinking *bool
	// ToolChoice is the Anthropic tool-choice behavior: a string ("auto",
	// "any", "none") or an AnthropicToolChoice.
	ToolChoice any
	// Client is a pre-built transport. When set, internal client construction is
	// skipped.
	Client AnthropicClient
}

// =============================================================================
// Compatibility
// =============================================================================

type resolvedAnthropicCompat struct {
	supportsEagerToolInputStreaming bool
	supportsLongCacheRetention      bool
	sendSessionAffinityHeaders      bool
	sessionAffinityFormat           string
	supportsCacheControlOnTools     bool
	supportsTemperature             bool
	allowEmptySignature             bool
	supportsStrictTools             bool
	supportsMidConvoSystemMessages  bool
	supportsMidConvoToolChanges     bool
	forceAdaptiveThinking           bool
	supportsMidConvoEffort          bool
	allowedFallbackModels           []types.AnthropicAllowedFallbackModel
}

func boolOr(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func getAnthropicCompat(model *types.Model) resolvedAnthropicCompat {
	isOpenRouter := model.Provider == types.ProviderOpenRouter || strings.Contains(model.BaseUrl, "openrouter.ai")
	compat := resolvedAnthropicCompat{
		supportsEagerToolInputStreaming: true,
		supportsLongCacheRetention:      true,
		sendSessionAffinityHeaders:      isOpenRouter,
		supportsCacheControlOnTools:     true,
		supportsTemperature:             true,
	}
	if isOpenRouter {
		compat.sessionAffinityFormat = "openrouter"
	}
	raw := model.Compat.AnthropicMessages
	if raw == nil {
		return compat
	}
	compat.supportsEagerToolInputStreaming = boolOr(raw.SupportsEagerToolInputStreaming, compat.supportsEagerToolInputStreaming)
	compat.supportsLongCacheRetention = boolOr(raw.SupportsLongCacheRetention, compat.supportsLongCacheRetention)
	compat.sendSessionAffinityHeaders = boolOr(raw.SendSessionAffinityHeaders, compat.sendSessionAffinityHeaders)
	compat.supportsCacheControlOnTools = boolOr(raw.SupportsCacheControlOnTools, compat.supportsCacheControlOnTools)
	compat.supportsTemperature = boolOr(raw.SupportsTemperature, compat.supportsTemperature)
	compat.allowEmptySignature = boolOr(raw.AllowEmptySignature, compat.allowEmptySignature)
	compat.supportsStrictTools = boolOr(raw.SupportsStrictTools, compat.supportsStrictTools)
	compat.supportsMidConvoSystemMessages = boolOr(raw.SupportsMidConvoSystemMessages, compat.supportsMidConvoSystemMessages)
	compat.supportsMidConvoToolChanges = boolOr(raw.SupportsMidConvoToolChanges, compat.supportsMidConvoToolChanges)
	compat.forceAdaptiveThinking = boolOr(raw.ForceAdaptiveThinking, compat.forceAdaptiveThinking)
	compat.supportsMidConvoEffort = boolOr(raw.SupportsMidConvoEffort, compat.supportsMidConvoEffort)
	if raw.SessionAffinityFormat != nil {
		compat.sessionAffinityFormat = *raw.SessionAffinityFormat
	}
	compat.allowedFallbackModels = raw.AllowedFallbackModels
	return compat
}

// getAnthropicCacheControl resolves the cache-control block for a request.
func getAnthropicCacheControl(model *types.Model, cacheRetention *types.CacheRetention, env types.ProviderEnv) (types.CacheRetention, map[string]any) {
	retention := resolveCacheRetention(cacheRetention, env)
	if retention == types.CacheRetentionNone {
		return retention, nil
	}
	cacheControl := map[string]any{"type": "ephemeral"}
	if retention == types.CacheRetentionLong && getAnthropicCompat(model).supportsLongCacheRetention {
		cacheControl["ttl"] = "1h"
	}
	return retention, cacheControl
}

// =============================================================================
// Claude Code tool-name normalization (stealth mode)
// =============================================================================

const anthropicClaudeCodeVersion = "2.1.280"

var anthropicClaudeCodeTools = []string{
	"Read",
	"Write",
	"Edit",
	"Bash",
	"Grep",
	"Glob",
	"AskUserQuestion",
	"EnterPlanMode",
	"ExitPlanMode",
	"KillShell",
	"NotebookEdit",
	"Skill",
	"Task",
	"TaskOutput",
	"TodoWrite",
	"WebFetch",
	"WebSearch",
}

var anthropicClaudeCodeLookup = func() map[string]string {
	lookup := make(map[string]string, len(anthropicClaudeCodeTools))
	for _, name := range anthropicClaudeCodeTools {
		lookup[strings.ToLower(name)] = name
	}
	return lookup
}()

func anthropicToClaudeCodeName(name string) string {
	if canonical, ok := anthropicClaudeCodeLookup[strings.ToLower(name)]; ok {
		return canonical
	}
	return name
}

func anthropicFromClaudeCodeName(name string, tools []types.Tool) string {
	if len(tools) == 0 {
		return name
	}
	lowerName := strings.ToLower(name)
	for _, tool := range tools {
		if strings.ToLower(tool.Name) == lowerName {
			return tool.Name
		}
	}
	return name
}

// =============================================================================
// Content conversion
// =============================================================================

// anthropicConvertContentBlocks converts text/image blocks into either a
// concatenated string or an Anthropic content-block array.
func anthropicConvertContentBlocks(content []types.ContentBlock) any {
	hasImages := false
	for _, block := range content {
		if block.Type == types.ContentTypeImage {
			hasImages = true
			break
		}
	}
	if !hasImages {
		parts := make([]string, 0, len(content))
		for _, block := range content {
			if block.Type == types.ContentTypeText && block.Text != nil {
				parts = append(parts, block.Text.Text)
			}
		}
		return utils.SanitizeSurrogates(strings.Join(parts, "\n"))
	}

	blocks := make([]any, 0, len(content))
	for _, block := range content {
		switch block.Type {
		case types.ContentTypeText:
			if block.Text == nil {
				continue
			}
			blocks = append(blocks, map[string]any{
				"type": "text",
				"text": utils.SanitizeSurrogates(block.Text.Text),
			})
		case types.ContentTypeImage:
			if block.Image == nil {
				continue
			}
			blocks = append(blocks, map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":       "base64",
					"media_type": block.Image.MimeType,
					"data":       block.Image.Data,
				},
			})
		}
	}

	hasText := false
	for _, block := range blocks {
		if object, ok := block.(map[string]any); ok && object["type"] == "text" {
			hasText = true
			break
		}
	}
	if !hasText {
		blocks = append([]any{map[string]any{"type": "text", "text": "(see attached image)"}}, blocks...)
	}
	return blocks
}

// =============================================================================
// Beta features
// =============================================================================

const (
	anthropicFineGrainedToolStreamingBeta    = "fine-grained-tool-streaming-2025-05-14"
	anthropicInterleavedThinkingBeta         = "interleaved-thinking-2025-05-14"
	anthropicServerSideFallbackBeta          = "server-side-fallback-2026-07-01"
	anthropicMidConversationOutputConfigBeta = "mid-conversation-output-config-2026-07-01"
	anthropicThinkingBindingControlsBeta     = "thinking-binding-controls-2026-08-01"
	anthropicMidConversationToolChangesBeta  = "mid-conversation-tool-changes-2026-07-01"
	anthropicClaudeCodeBeta                  = "claude-code-20250219"
	anthropicOAuthBeta                       = "oauth-2025-04-20"
	anthropicDeferredPlaceholderToolName     = "__pi_deferred_placeholder__"
	anthropicDeferredPlaceholderToolDesc     = "Reserved placeholder. Never available. Never call this."
	anthropicClaudeCodeIdentitySystem        = "You are Claude Code, Anthropic's official CLI for Claude."
)

func anthropicHasConfigurableBetaHeader(model *types.Model, optionsHeaders types.ProviderHeaders) (configured *string, suppressed bool, present bool) {
	for _, headers := range []types.ProviderHeaders{headersFromModel(model), optionsHeaders} {
		for name, value := range headers {
			if strings.ToLower(name) != "anthropic-beta" {
				continue
			}
			if value == nil {
				return nil, true, true
			}
			copied := *value
			configured = &copied
			present = true
		}
	}
	return configured, false, present
}

func headersFromModel(model *types.Model) types.ProviderHeaders {
	if model == nil || len(model.Headers) == 0 {
		return nil
	}
	headers := make(types.ProviderHeaders, len(model.Headers))
	for key, value := range model.Headers {
		copied := value
		headers[key] = &copied
	}
	return headers
}

func anthropicShouldUseFineGrainedToolStreamingBeta(model *types.Model, context *types.TranscriptContext) bool {
	return len(utils.GetCurrentTools(context.Messages)) > 0 && !getAnthropicCompat(model).supportsEagerToolInputStreaming
}

func anthropicShouldUseServerSideFallbackBeta(compat resolvedAnthropicCompat) bool {
	return len(compat.allowedFallbackModels) > 0
}

func getAnthropicBetaFeatures(model *types.Model, context *types.TranscriptContext, isOAuthToken bool, nativeToolChanges bool, options *AnthropicOptions) []string {
	configured, suppressed, present := anthropicHasConfigurableBetaHeader(model, anthropicOptionsHeaders(options))
	if suppressed {
		return []string{}
	}
	if present && configured != nil {
		features := []string{}
		seen := map[string]bool{}
		for _, feature := range strings.Split(*configured, ",") {
			trimmed := strings.TrimSpace(feature)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			seen[trimmed] = true
			features = append(features, trimmed)
		}
		return features
	}

	compat := getAnthropicCompat(model)
	features := []string{}
	if isOAuthToken {
		features = append(features, anthropicClaudeCodeBeta, anthropicOAuthBeta)
	}
	if anthropicShouldUseFineGrainedToolStreamingBeta(model, context) {
		features = append(features, anthropicFineGrainedToolStreamingBeta)
	}
	thinkingEnabled := options != nil && options.ThinkingEnabled != nil && *options.ThinkingEnabled
	if model.Reasoning && thinkingEnabled && boolOr(anthropicOptionInterleaved(options), true) && !compat.forceAdaptiveThinking {
		features = append(features, anthropicInterleavedThinkingBeta)
	}
	if anthropicShouldUseServerSideFallbackBeta(compat) {
		features = append(features, anthropicServerSideFallbackBeta)
	}
	if compat.supportsMidConvoEffort {
		features = append(features, anthropicMidConversationOutputConfigBeta, anthropicThinkingBindingControlsBeta)
	}
	if nativeToolChanges {
		features = append(features, anthropicMidConversationToolChangesBeta)
	}
	return uniqueStrings(features)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func anthropicOptionsHeaders(options *AnthropicOptions) types.ProviderHeaders {
	if options == nil {
		return nil
	}
	return options.Headers
}

func anthropicOptionInterleaved(options *AnthropicOptions) *bool {
	if options == nil {
		return nil
	}
	return options.InterleavedThinking
}

// =============================================================================
// Parameter building
// =============================================================================

func anthropicIsMidConvoEffort(model *types.Model) bool {
	return rawCompat(model) != nil && boolOr(rawCompat(model).SupportsMidConvoEffort, false)
}

func rawCompat(model *types.Model) *types.AnthropicMessagesCompat {
	if model == nil {
		return nil
	}
	return model.Compat.AnthropicMessages
}

func anthropicNormalizeToolCallID(id string, _ *types.Model, _ types.AssistantMessage) string {
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

func anthropicIsEffort(value *string) bool {
	if value == nil {
		return false
	}
	switch AnthropicEffort(*value) {
	case AnthropicEffortLow, AnthropicEffortMedium, AnthropicEffortHigh, AnthropicEffortXHigh, AnthropicEffortMax:
		return true
	default:
		return false
	}
}

func anthropicConvertToolResult(message types.ToolResultMessage) map[string]any {
	return map[string]any{
		"type":        "tool_result",
		"tool_use_id": message.ToolCallId,
		"content":     anthropicConvertContentBlocks(message.Content),
		"is_error":    message.IsError,
	}
}

type convertedAnthropicMessages struct {
	messages        []map[string]any
	assistantLevels map[int]AnthropicEffort
}

func sanitizeText(text string) string { return utils.SanitizeSurrogates(text) }

func convertAnthropicMessages(
	transformedMessages []types.Message,
	isOAuthToken bool,
	cacheControl map[string]any,
	allowEmptySignature bool,
	managedProvider string,
	nativeToolChanges bool,
) convertedAnthropicMessages {
	params := []map[string]any{}
	assistantLevels := map[int]AnthropicEffort{}
	pendingSystemMessages := []map[string]any{}
	flushPendingSystemMessages := func() {
		params = append(params, pendingSystemMessages...)
		pendingSystemMessages = []map[string]any{}
	}

	for i := 0; i < len(transformedMessages); i++ {
		message := transformedMessages[i]

		switch message.Role {
		case types.SystemMessageRole:
			if message.System == nil {
				continue
			}
			text := utils.RenderSystemMessageUpdate(*message.System)
			blocks := []any{}
			if len(text) > 0 {
				blocks = append(blocks, map[string]any{"type": "text", "text": sanitizeText(text)})
			}
			if nativeToolChanges {
				for _, tool := range message.System.ToolsRemoved {
					name := tool.Name
					if isOAuthToken {
						name = anthropicToClaudeCodeName(tool.Name)
					}
					blocks = append(blocks, map[string]any{
						"type": "tool_removal",
						"tool": map[string]any{"type": "tool_reference", "name": name},
					})
				}
				for _, tool := range message.System.ToolsAdded {
					name := tool.Name
					if isOAuthToken {
						name = anthropicToClaudeCodeName(tool.Name)
					}
					blocks = append(blocks, map[string]any{
						"type": "tool_addition",
						"tool": map[string]any{"type": "tool_reference", "name": name},
					})
				}
			}
			if len(blocks) > 0 {
				pendingSystemMessages = append(pendingSystemMessages, map[string]any{"role": "system", "content": blocks})
			}

		case types.UserMessageRole:
			if message.User == nil {
				continue
			}
			if !message.User.Content.Structured {
				if strings.TrimSpace(message.User.Content.Text) != "" {
					params = append(params, map[string]any{"role": "user", "content": sanitizeText(message.User.Content.Text)})
				}
				continue
			}
			blocks := []any{}
			for _, item := range message.User.Content.Blocks {
				switch item.Type {
				case types.ContentTypeText:
					if item.Text == nil {
						continue
					}
					blocks = append(blocks, map[string]any{"type": "text", "text": sanitizeText(item.Text.Text)})
				case types.ContentTypeImage:
					if item.Image == nil {
						continue
					}
					blocks = append(blocks, map[string]any{
						"type": "image",
						"source": map[string]any{
							"type":       "base64",
							"media_type": item.Image.MimeType,
							"data":       item.Image.Data,
						},
					})
				}
			}
			filtered := make([]any, 0, len(blocks))
			for _, block := range blocks {
				object, ok := block.(map[string]any)
				if !ok {
					continue
				}
				if object["type"] == "text" {
					if text, ok := object["text"].(string); ok && strings.TrimSpace(text) == "" {
						continue
					}
				}
				filtered = append(filtered, block)
			}
			if len(filtered) == 0 {
				continue
			}
			params = append(params, map[string]any{"role": "user", "content": filtered})

		case types.AssistantMessageRole:
			if message.Assistant == nil {
				continue
			}
			flushPendingSystemMessages()
			blocks := []any{}
			for _, block := range message.Assistant.Content {
				switch block.Type {
				case types.ContentTypeText:
					if block.Text == nil || strings.TrimSpace(block.Text.Text) == "" {
						continue
					}
					blocks = append(blocks, map[string]any{"type": "text", "text": sanitizeText(block.Text.Text)})
				case types.ContentTypeThinking:
					if block.Thinking == nil {
						continue
					}
					thinking := block.Thinking
					if thinking.Redacted != nil && *thinking.Redacted {
						data := ""
						if thinking.ThinkingSignature != nil {
							data = *thinking.ThinkingSignature
						}
						blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": data})
						continue
					}
					signature := thinking.ThinkingSignature
					hasSignature := signature != nil && strings.TrimSpace(*signature) != ""
					if strings.TrimSpace(thinking.Thinking) == "" && !hasSignature {
						continue
					}
					if !hasSignature {
						if allowEmptySignature {
							blocks = append(blocks, map[string]any{
								"type":      "thinking",
								"thinking":  sanitizeText(thinking.Thinking),
								"signature": "",
							})
						} else {
							blocks = append(blocks, map[string]any{
								"type": "text",
								"text": sanitizeText(thinking.Thinking),
							})
						}
						continue
					}
					blocks = append(blocks, map[string]any{
						"type":      "thinking",
						"thinking":  sanitizeText(thinking.Thinking),
						"signature": *signature,
					})
				case types.ContentTypeToolCall:
					if block.ToolCall == nil {
						continue
					}
					name := block.ToolCall.Name
					if isOAuthToken {
						name = anthropicToClaudeCodeName(block.ToolCall.Name)
					}
					arguments := block.ToolCall.Arguments
					if len(arguments) == 0 {
						arguments = json.RawMessage("{}")
					}
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    block.ToolCall.Id,
						"name":  name,
						"input": arguments,
					})
				}
			}
			if len(blocks) == 0 {
				continue
			}
			messageIndex := len(params)
			params = append(params, map[string]any{"role": "assistant", "content": blocks})
			if managedProvider != "" &&
				message.Assistant.Api == types.ApiAnthropicMessages &&
				string(message.Assistant.Provider) == managedProvider &&
				anthropicIsEffort(message.Assistant.ProviderThinkingLevel) {
				assistantLevels[messageIndex] = AnthropicEffort(*message.Assistant.ProviderThinkingLevel)
			}

		case types.ToolResultMessageRole:
			toolResults := []any{}
			j := i
			for j < len(transformedMessages) && transformedMessages[j].Role == types.ToolResultMessageRole {
				if transformedMessages[j].ToolResult != nil {
					toolResults = append(toolResults, anthropicConvertToolResult(*transformedMessages[j].ToolResult))
				}
				j++
			}
			i = j - 1
			params = append(params, map[string]any{"role": "user", "content": toolResults})
		}
	}

	flushPendingSystemMessages()

	if cacheControl != nil && len(params) > 0 {
		last := params[len(params)-1]
		if role, ok := last["role"].(string); ok && (role == "user" || role == "system") {
			switch content := last["content"].(type) {
			case []any:
				if len(content) > 0 {
					if block, ok := content[len(content)-1].(map[string]any); ok {
						switch block["type"] {
						case "text", "image", "tool_result", "tool_addition", "tool_removal":
							block["cache_control"] = cacheControl
						}
					}
				}
			case string:
				last["content"] = []any{map[string]any{"type": "text", "text": content, "cache_control": cacheControl}}
			}
		}
	}

	return convertedAnthropicMessages{messages: params, assistantLevels: assistantLevels}
}

func insertThinkingLevelMessages(converted convertedAnthropicMessages, activeEffort AnthropicEffort) []map[string]any {
	messages := make([]map[string]any, 0, len(converted.messages)+1)
	for index, message := range converted.messages {
		if historical, ok := converted.assistantLevels[index]; ok {
			messages = append(messages, map[string]any{
				"role":          "system",
				"content":       []any{},
				"output_config": map[string]any{"effort": string(historical)},
			})
		}
		messages = append(messages, message)
	}
	messages = append(messages, map[string]any{
		"role":          "system",
		"content":       []any{},
		"output_config": map[string]any{"effort": string(activeEffort)},
	})
	return messages
}

func anthropicDeferredPlaceholderTool() map[string]any {
	return map[string]any{
		"name":          anthropicDeferredPlaceholderToolName,
		"description":   anthropicDeferredPlaceholderToolDesc,
		"input_schema":  map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}},
		"defer_loading": true,
	}
}

func convertAnthropicTools(tools []types.Tool, isOAuthToken, supportsEagerToolInputStreaming, supportsStrictTools bool, cacheControl map[string]any) ([]any, error) {
	if tools == nil {
		return []any{}, nil
	}
	converted := make([]any, 0, len(tools))
	for index, tool := range tools {
		strict, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictTools)
		if err != nil {
			return nil, err
		}
		parameters, err := GetJSONSchemaToolParameters(tool, strict)
		if err != nil {
			return nil, err
		}
		parametersObject := decodeSchemaObject(parameters)
		properties := map[string]any{}
		required := []any{}
		if parametersObject != nil {
			if schemaProperties, ok := parametersObject["properties"].(map[string]any); ok {
				properties = schemaProperties
			}
			if schemaRequired, ok := parametersObject["required"].([]any); ok {
				required = schemaRequired
			}
		}
		legacyInputSchema := map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		}
		var inputSchema map[string]any
		if strict != nil && *strict {
			inputSchema = map[string]any{}
			for key, value := range parametersObject {
				inputSchema[key] = value
			}
			inputSchema["type"] = "object"
			inputSchema["properties"] = properties
			inputSchema["required"] = required
		} else {
			inputSchema = legacyInputSchema
		}

		name := tool.Name
		if isOAuthToken {
			name = anthropicToClaudeCodeName(tool.Name)
		}
		entry := map[string]any{
			"name":         name,
			"description":  tool.Description,
			"input_schema": inputSchema,
		}
		if supportsEagerToolInputStreaming {
			entry["eager_input_streaming"] = true
		}
		if strict != nil && *strict {
			entry["strict"] = true
		}
		if cacheControl != nil && index == len(tools)-1 {
			entry["cache_control"] = cacheControl
		}
		converted = append(converted, entry)
	}
	return converted, nil
}

func buildAnthropicParams(model *types.Model, context *types.TranscriptContext, isOAuthToken bool, options *AnthropicOptions) (map[string]any, error) {
	_, cacheControl := getAnthropicCacheControl(model, anthropicOptionCacheRetention(options), anthropicOptionEnv(options))
	compat := getAnthropicCompat(model)

	initialSystemMessage := utils.GetInitialSystemMessage(context.Messages)
	initialSystemText := ""
	if initialSystemMessage != nil {
		initialSystemText = utils.GetSystemMessageText(*initialSystemMessage)
	}
	transformedMessages := TransformMessages(context.Messages, model, anthropicNormalizeToolCallID)
	var conversationMessages []types.Message
	if initialSystemMessage != nil {
		conversationMessages = transformedMessages[1:]
	} else {
		conversationMessages = transformedMessages
	}

	initialTools := []types.Tool{}
	if initialSystemMessage != nil {
		initialTools = initialSystemMessage.ToolsAdded
	}
	nativeToolChanges := compat.supportsMidConvoSystemMessages &&
		compat.supportsMidConvoToolChanges &&
		len(initialTools) > 0 &&
		!utils.HasToolRedefinitions(context.Messages)

	managedProvider := ""
	if anthropicIsMidConvoEffort(model) {
		managedProvider = string(model.Provider)
	}
	converted := convertAnthropicMessages(
		conversationMessages,
		isOAuthToken,
		cacheControl,
		compat.allowEmptySignature,
		managedProvider,
		nativeToolChanges,
	)

	activeEffort := AnthropicEffortHigh
	if options != nil && options.Effort != nil {
		activeEffort = *options.Effort
	}
	betaFeatures := getAnthropicBetaFeatures(model, context, isOAuthToken, nativeToolChanges, options)

	messages := converted.messages
	if anthropicIsMidConvoEffort(model) {
		messages = insertThinkingLevelMessages(converted, activeEffort)
	}

	maxTokens := int(model.MaxTokens)
	if options != nil && options.MaxTokens != nil {
		maxTokens = *options.MaxTokens
	}

	params := map[string]any{
		"model":      model.Id,
		"messages":   messages,
		"max_tokens": maxTokens,
		"stream":     true,
	}
	if len(betaFeatures) > 0 {
		params["betas"] = betaFeatures
	}

	if isOAuthToken {
		system := []any{map[string]any{
			"type": "text",
			"text": anthropicClaudeCodeIdentitySystem,
		}}
		if cacheControl != nil {
			system[0].(map[string]any)["cache_control"] = cacheControl
		}
		if initialSystemText != "" {
			entry := map[string]any{"type": "text", "text": sanitizeText(initialSystemText)}
			if cacheControl != nil {
				entry["cache_control"] = cacheControl
			}
			system = append(system, entry)
		}
		params["system"] = system
	} else if initialSystemText != "" {
		entry := map[string]any{"type": "text", "text": sanitizeText(initialSystemText)}
		if cacheControl != nil {
			entry["cache_control"] = cacheControl
		}
		params["system"] = []any{entry}
	}

	thinkingEnabled := options != nil && options.ThinkingEnabled != nil && *options.ThinkingEnabled
	if options != nil && options.Temperature != nil &&
		!thinkingEnabled &&
		!compat.supportsMidConvoEffort &&
		compat.supportsTemperature {
		params["temperature"] = *options.Temperature
	}

	toolCacheControl := cacheControl
	if !compat.supportsCacheControlOnTools {
		toolCacheControl = nil
	}
	if nativeToolChanges {
		initialNames := map[string]bool{}
		for _, tool := range initialTools {
			initialNames[tool.Name] = true
		}
		laterTools := []types.Tool{}
		for _, tool := range utils.GetDeclaredTools(context.Messages) {
			if !initialNames[tool.Name] {
				laterTools = append(laterTools, tool)
			}
		}
		initialConverted, err := convertAnthropicTools(initialTools, isOAuthToken, compat.supportsEagerToolInputStreaming, compat.supportsStrictTools, toolCacheControl)
		if err != nil {
			return nil, err
		}
		laterConverted, err := convertAnthropicTools(laterTools, isOAuthToken, compat.supportsEagerToolInputStreaming, compat.supportsStrictTools, nil)
		if err != nil {
			return nil, err
		}
		deferred := make([]any, 0, len(laterConverted))
		for _, tool := range laterConverted {
			if object, ok := tool.(map[string]any); ok {
				object["defer_loading"] = true
			}
			deferred = append(deferred, tool)
		}
		tools := make([]any, 0, len(initialConverted)+1+len(deferred))
		tools = append(tools, initialConverted...)
		tools = append(tools, anthropicDeferredPlaceholderTool())
		tools = append(tools, deferred...)
		params["tools"] = tools
	} else {
		tools := utils.GetCurrentTools(context.Messages)
		if len(tools) > 0 {
			convertedTools, err := convertAnthropicTools(tools, isOAuthToken, compat.supportsEagerToolInputStreaming, compat.supportsStrictTools, toolCacheControl)
			if err != nil {
				return nil, err
			}
			params["tools"] = convertedTools
		}
	}

	if compat.supportsMidConvoEffort {
		display := AnthropicThinkingDisplaySummarized
		if options != nil && options.ThinkingDisplay != nil {
			display = *options.ThinkingDisplay
		}
		params["thinking"] = map[string]any{
			"type":          "adaptive",
			"display":       string(display),
			"block_binding": map[string]any{"prefix_mismatch_behavior": "drop_block"},
		}
		params["output_config"] = map[string]any{"effort": "high"}
	} else if model.Reasoning {
		if thinkingEnabled {
			display := AnthropicThinkingDisplaySummarized
			if options != nil && options.ThinkingDisplay != nil {
				display = *options.ThinkingDisplay
			}
			if compat.forceAdaptiveThinking {
				params["thinking"] = map[string]any{"type": "adaptive", "display": string(display)}
				if options != nil && options.Effort != nil {
					params["output_config"] = map[string]any{"effort": string(*options.Effort)}
				}
			} else {
				budget := 1024
				if options != nil && options.ThinkingBudgetTokens != nil {
					budget = *options.ThinkingBudgetTokens
					if budget == 0 {
						budget = 1024
					}
				}
				params["thinking"] = map[string]any{
					"type":          "enabled",
					"budget_tokens": budget,
					"display":       string(display),
				}
			}
		} else if options != nil && options.ThinkingEnabled != nil && !*options.ThinkingEnabled && anthropicOffLevelNotDisabled(model) {
			params["thinking"] = map[string]any{"type": "disabled"}
		}
	}

	if options != nil && options.Metadata != nil {
		if userId, ok := options.Metadata["user_id"].(string); ok {
			params["metadata"] = map[string]any{"user_id": userId}
		}
	}

	if options != nil && options.ToolChoice != nil {
		switch choice := options.ToolChoice.(type) {
		case string:
			params["tool_choice"] = map[string]any{"type": choice}
		case types.ToolChoice:
			params["tool_choice"] = map[string]any{"type": string(choice)}
		default:
			params["tool_choice"] = choice
		}
	}

	if len(compat.allowedFallbackModels) > 0 {
		fallbacks := make([]any, 0, len(compat.allowedFallbackModels))
		for _, fallback := range compat.allowedFallbackModels {
			fallbacks = append(fallbacks, map[string]any{"model": fallback.Model})
		}
		params["fallbacks"] = fallbacks
	}

	return params, nil
}

// anthropicOffLevelNotDisabled mirrors `model.thinkingLevelMap?.off !== null`:
// a present JSON null mapping means thinking cannot be explicitly disabled.
func anthropicOffLevelNotDisabled(model *types.Model) bool {
	if model.ThinkingLevelMap == nil {
		return true
	}
	_, present, supported := model.ThinkingLevelMap.Lookup(types.ThinkingOff)
	if present && !supported {
		return false
	}
	return true
}

func anthropicOptionCacheRetention(options *AnthropicOptions) *types.CacheRetention {
	if options == nil {
		return nil
	}
	return options.CacheRetention
}

func anthropicOptionEnv(options *AnthropicOptions) types.ProviderEnv {
	if options == nil {
		return nil
	}
	return options.Env
}

func anthropicOptionMaxRetries(options *AnthropicOptions) *int {
	if options == nil {
		return nil
	}
	return options.MaxRetries
}

func anthropicOptionMaxRetryDelayMs(options *AnthropicOptions) *int {
	if options == nil {
		return nil
	}
	return options.MaxRetryDelayMs
}

// =============================================================================
// Headers and client construction
// =============================================================================

func mergeAnthropicHeaders(sources ...types.ProviderHeaders) types.ProviderHeaders {
	merged := types.ProviderHeaders{}
	for _, headers := range sources {
		for key, value := range headers {
			if value == nil {
				delete(merged, key)
				continue
			}
			copied := *value
			merged[key] = &copied
		}
	}
	return merged
}

func mergeAnthropicClientHeaders(sources ...types.ProviderHeaders) types.ProviderHeaders {
	base := "User-Agent"
	defaults := types.ProviderHeaders{base: stringPtr(utils.GetPiUserAgent())}
	return mergeAnthropicHeaders(append([]types.ProviderHeaders{defaults}, sources...)...)
}

func providerHeadersToMap(headers types.ProviderHeaders) map[string]string {
	out := map[string]string{}
	for key, value := range headers {
		if value != nil {
			out[key] = *value
		}
	}
	return out
}

func stringMapToProviderHeaders(headers map[string]string) types.ProviderHeaders {
	if headers == nil {
		return nil
	}
	out := make(types.ProviderHeaders, len(headers))
	for key, value := range headers {
		copied := value
		out[key] = &copied
	}
	return out
}

func assertAnthropicRequestAuth(provider types.ProviderId, apiKey *string, headers types.ProviderHeaders) error {
	if apiKey != nil && *apiKey != "" {
		return nil
	}
	if hasHeader(headers, "authorization") || hasHeader(headers, "x-api-key") || hasHeader(headers, "cf-aig-authorization") {
		return nil
	}
	return fmt.Errorf("No API key for provider: %s", provider)
}

func anthropicIsOAuthToken(apiKey string) bool {
	return strings.Contains(apiKey, "sk-ant-oat")
}

type anthropicClientSpec struct {
	headers map[string]string
	isOAuth bool
}

func createAnthropicClientSpec(
	model *types.Model,
	apiKey *string,
	optionsHeaders types.ProviderHeaders,
	modelHeaders types.ProviderHeaders,
	dynamicHeaders map[string]string,
	sessionID *string,
) anthropicClientSpec {
	searchHeaders := []types.ProviderHeaders{
		{
			"accept": stringPtr("application/json"),
			"anthropic-dangerous-direct-browser-access": stringPtr("true"),
		},
	}

	if model.Provider == types.ProviderGitHubCopilot {
		headers := mergeAnthropicClientHeaders(
			searchHeaders[0],
			modelHeaders,
			stringMapToProviderHeaders(dynamicHeaders),
			optionsHeaders,
		)
		if apiKey != nil {
			headers["authorization"] = stringPtr("Bearer " + *apiKey)
		}
		return anthropicClientSpec{headers: providerHeadersToMap(headers), isOAuth: false}
	}

	if apiKey != nil && anthropicIsOAuthToken(*apiKey) {
		oauthDefaults := types.ProviderHeaders{
			"accept": stringPtr("application/json"),
			"anthropic-dangerous-direct-browser-access": stringPtr("true"),
			"user-agent": stringPtr("claude-cli/" + anthropicClaudeCodeVersion),
			"x-app":      stringPtr("cli"),
		}
		headers := mergeAnthropicClientHeaders(oauthDefaults, modelHeaders, optionsHeaders)
		headers["authorization"] = stringPtr("Bearer " + *apiKey)
		return anthropicClientSpec{headers: providerHeadersToMap(headers), isOAuth: true}
	}

	compat := getAnthropicCompat(model)
	sessionAffinity := types.ProviderHeaders{}
	if sessionID != nil && compat.sendSessionAffinityHeaders {
		name := "x-session-affinity"
		if compat.sessionAffinityFormat == "openrouter" {
			name = "x-session-id"
		}
		sessionAffinity[name] = sessionID
	}
	headers := mergeAnthropicClientHeaders(searchHeaders[0], sessionAffinity, modelHeaders, optionsHeaders)
	if apiKey != nil && *apiKey != "" {
		headers["x-api-key"] = apiKey
	}
	return anthropicClientSpec{headers: providerHeadersToMap(headers), isOAuth: false}
}

// =============================================================================
// SSE parsing
// =============================================================================

type anthropicServerSentEvent struct {
	event *string
	data  string
	raw   []string
}

type anthropicSSEDecoderState struct {
	event *string
	data  []string
	raw   []string
}

func anthropicFlushSSE(state *anthropicSSEDecoderState) *anthropicServerSentEvent {
	if state.event == nil && len(state.data) == 0 {
		return nil
	}
	event := &anthropicServerSentEvent{
		event: state.event,
		data:  strings.Join(state.data, "\n"),
		raw:   append([]string(nil), state.raw...),
	}
	state.event = nil
	state.data = []string{}
	state.raw = []string{}
	return event
}

func anthropicDecodeSSELine(line string, state *anthropicSSEDecoderState) *anthropicServerSentEvent {
	if line == "" {
		return anthropicFlushSSE(state)
	}
	state.raw = append(state.raw, line)
	if strings.HasPrefix(line, ":") {
		return nil
	}
	delimiterIndex := strings.Index(line, ":")
	fieldName := line
	value := ""
	if delimiterIndex == -1 {
		value = ""
	} else {
		fieldName = line[:delimiterIndex]
		value = line[delimiterIndex+1:]
	}
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	switch fieldName {
	case "event":
		state.event = &value
	case "data":
		state.data = append(state.data, value)
	}
	return nil
}

// anthropicConsumeLine splits the first complete line from text. When the only
// line break is a trailing carriage return that may still be followed by a
// newline in the next chunk it returns ok=false so the caller waits for more.
func anthropicConsumeLine(text string, eof bool) (line, rest string, ok bool) {
	index := strings.IndexAny(text, "\r\n")
	if index == -1 {
		return "", text, false
	}
	if text[index] == '\r' {
		if index+1 == len(text) && !eof {
			return "", text, false
		}
		next := index + 1
		if next < len(text) && text[next] == '\n' {
			next++
		}
		return text[:index], text[next:], true
	}
	return text[:index], text[index+1:], true
}

func iterateAnthropicSSE(body io.Reader, signal <-chan struct{}, yield func(event *anthropicServerSentEvent) error) error {
	reader := bufio.NewReader(body)
	state := &anthropicSSEDecoderState{data: []string{}, raw: []string{}}
	buffer := ""
	emit := func(event *anthropicServerSentEvent) error {
		if event != nil {
			return yield(event)
		}
		return nil
	}

	for {
		if aborted(signal) {
			return fmt.Errorf("Request was aborted")
		}
		chunk := make([]byte, 8192)
		n, readErr := reader.Read(chunk)
		if n > 0 {
			buffer += string(chunk[:n])
		}
		eof := readErr == io.EOF
		for {
			line, rest, ok := anthropicConsumeLine(buffer, eof)
			if !ok {
				break
			}
			buffer = rest
			if err := emit(anthropicDecodeSSELine(line, state)); err != nil {
				return err
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				return readErr
			}
			break
		}
	}

	if buffer != "" {
		if err := emit(anthropicDecodeSSELine(buffer, state)); err != nil {
			return err
		}
		buffer = ""
	}
	return emit(anthropicFlushSSE(state))
}

// =============================================================================
// Stop reason mapping
// =============================================================================

func mapAnthropicStopReason(reason string, stopDetails map[string]any) (types.StopReason, *string) {
	switch reason {
	case "end_turn", "pause_turn", "stop_sequence":
		return types.StopReasonStop, nil
	case "max_tokens":
		return types.StopReasonLength, nil
	case "tool_use":
		return types.StopReasonToolUse, nil
	case "refusal":
		message := "The model refused to complete the request"
		if stopDetails != nil {
			if explanation, ok := stopDetails["explanation"].(string); ok && explanation != "" {
				message = explanation
			}
		}
		return types.StopReasonError, &message
	case "sensitive":
		message := "Provider stopped with: sensitive"
		return types.StopReasonError, &message
	default:
		return "", nil
	}
}

// =============================================================================
// Streaming
// =============================================================================

type anthropicStreamBlock struct {
	index       int
	kind        types.ContentBlockType
	text        *types.TextContent
	thinking    *types.ThinkingContent
	tool        *types.ToolCall
	partialJSON string
}

func (b *anthropicStreamBlock) contentBlock() types.ContentBlock {
	switch b.kind {
	case types.ContentTypeText:
		return types.ContentBlock{Type: types.ContentTypeText, Text: b.text}
	case types.ContentTypeThinking:
		return types.ContentBlock{Type: types.ContentTypeThinking, Thinking: b.thinking}
	case types.ContentTypeToolCall:
		return types.ContentBlock{Type: types.ContentTypeToolCall, ToolCall: b.tool}
	default:
		return types.ContentBlock{}
	}
}

func anthropicFindBlock(blocks []*anthropicStreamBlock, index int) int {
	for i, block := range blocks {
		if block.index == index {
			return i
		}
	}
	return -1
}

func rawJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

func terminateAnthropicStream(stream *types.AssistantMessageEventStream, output *types.AssistantMessage, err error, wasAborted bool) {
	output.StopReason = types.StopReasonError
	if wasAborted {
		output.StopReason = types.StopReasonAborted
	}
	message := "An unknown error occurred"
	if err != nil {
		message = err.Error()
	}
	output.ErrorMessage = &message
	stream.Push(types.NewErrorEvent(output.StopReason, *output))
	stream.End(output)
}

func anthropicTransformationsFromValue(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		transformation := map[string]any{}
		for _, key := range []string{"type", "path", "reason"} {
			if text, ok := object[key].(string); ok {
				transformation[key] = text
			}
		}
		out = append(out, transformation)
	}
	return out
}

func anthropicRecordInputTransformations(output *types.AssistantMessage, transformations []map[string]any) {
	if len(transformations) == 0 {
		return
	}
	output.Diagnostics = append(output.Diagnostics, types.AssistantMessageDiagnostic{
		Type:      "anthropic_input_transformations",
		Timestamp: nowMillis(),
		Details:   map[string]any{"transformations": transformations},
	})
}

// AnthropicMessagesStream is the streaming entry point for the Anthropic
// Messages API.
func AnthropicMessagesStream(model *types.Model, transcript *types.TranscriptContext, options *AnthropicOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	supportsMidConvo := false
	if rawCompat(model) != nil {
		supportsMidConvo = boolOr(rawCompat(model).SupportsMidConvoSystemMessages, false)
	}
	normalizedContext := utils.ResolveTranscript(*transcript, &supportsMidConvo)

	go func() {
		compat := getAnthropicCompat(model)
		output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
		output.StopReason = types.StopReasonPending
		if compat.supportsMidConvoEffort {
			level := "high"
			if options != nil && options.Effort != nil {
				level = string(*options.Effort)
			}
			output.ProviderThinkingLevel = &level
		}

		signal := anthropicOptionSignal(options)
		fail := func(err error) {
			terminateAnthropicStream(stream, &output, err, aborted(signal))
		}

		var injectedClient AnthropicClient
		var requestHeaders map[string]string
		isOAuth := false
		if options != nil && options.Client != nil {
			injectedClient = options.Client
		} else {
			var apiKey *string
			var optionsHeaders types.ProviderHeaders
			if options != nil {
				apiKey = options.APIKey
				optionsHeaders = options.Headers
			}
			if err := assertAnthropicRequestAuth(model.Provider, apiKey, optionsHeaders); err != nil {
				fail(err)
				return
			}
			var dynamicHeaders map[string]string
			if model.Provider == types.ProviderGitHubCopilot {
				hasImages := HasCopilotVisionInput(normalizedContext.Messages)
				dynamicHeaders = BuildCopilotDynamicHeaders(normalizedContext.Messages, hasImages)
			}
			retention := resolveCacheRetention(anthropicOptionCacheRetention(options), anthropicOptionEnv(options))
			var sessionID *string
			if retention != types.CacheRetentionNone && options != nil {
				sessionID = options.SessionId
			}
			spec := createAnthropicClientSpec(model, apiKey, optionsHeaders, headersFromModel(model), dynamicHeaders, sessionID)
			requestHeaders = spec.headers
			isOAuth = spec.isOAuth
		}

		params, err := buildAnthropicParams(model, &normalizedContext, isOAuth, options)
		if err != nil {
			fail(err)
			return
		}

		if options != nil && options.OnPayload != nil {
			next, payloadErr := options.OnPayload(params, model)
			if payloadErr != nil {
				fail(payloadErr)
				return
			}
			if next != nil {
				if replacement, ok := next.(map[string]any); ok {
					replacement["stream"] = true
					params = replacement
				}
			}
		}

		var reqOptions *types.ProviderRequestOptions
		if options != nil {
			reqOptions = &options.ProviderRequestOptions
		}
		requestContext, cancel := contextForSignal(contextBackground(), signal)
		defer cancel()

		betaHeaderValue, betaSuppressed, betaConfigured := anthropicHasConfigurableBetaHeader(model, anthropicOptionsHeaders(options))

		create := func(ctx context.Context) (*http.Response, error) {
			if injectedClient != nil {
				return injectedClient.CreateMessage(ctx, params, nil)
			}
			headers := map[string]string{}
			for key, value := range requestHeaders {
				headers[key] = value
			}
			headers["content-type"] = "application/json"
			headers["anthropic-version"] = "2023-06-01"
			if !betaSuppressed {
				if betaConfigured && betaHeaderValue != nil {
					headers["anthropic-beta"] = *betaHeaderValue
				} else if features, ok := params["betas"].([]string); ok && len(features) > 0 {
					headers["anthropic-beta"] = strings.Join(features, ",")
				}
			}
			body, marshalErr := json.Marshal(params)
			if marshalErr != nil {
				return nil, marshalErr
			}
			url := strings.TrimRight(model.BaseUrl, "/") + "/v1/messages?beta=true"
			response, _, requestErr := performRequestWithRetry(ctx, reqOptions, http.MethodPost, url, headers, body)
			return response, requestErr
		}

		response, err := create(requestContext)
		if err != nil {
			fail(err)
			return
		}
		if response == nil {
			fail(fmt.Errorf("Anthropic request returned no response"))
			return
		}
		defer response.Body.Close()

		if options != nil && options.OnResponse != nil {
			options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, model)
		}

		stream.Push(types.NewStartEvent(output))

		blocks := []*anthropicStreamBlock{}
		var inputTransformations []map[string]any
		usageModel := model

		handleEvent := func(event map[string]any) error {
			eventType, _ := event["type"].(string)
			switch eventType {
			case "message_start":
				message, _ := event["message"].(map[string]any)
				if message == nil {
					return nil
				}
				if id, ok := message["id"].(string); ok {
					output.ResponseId = &id
				}
				if transformations, ok := message["input_transformations"].([]any); ok {
					if parsed := anthropicTransformationsFromValue(transformations); parsed != nil {
						inputTransformations = parsed
					}
				}
				if responseModel, ok := message["model"].(string); ok && responseModel != model.Id {
					output.ResponseModel = &responseModel
					var fallbackCost *types.ModelCost
					for i := range compat.allowedFallbackModels {
						fallback := compat.allowedFallbackModels[i]
						if string(fallback.Provider) == string(model.Provider) && fallback.Model == responseModel {
							cost := fallback.Cost
							fallbackCost = &cost
							break
						}
					}
					if fallbackCost != nil {
						fallbackModel := *model
						fallbackModel.Id = responseModel
						fallbackModel.Cost = *fallbackCost
						usageModel = &fallbackModel
					}
				}
				if usage, ok := message["usage"].(map[string]any); ok {
					output.Usage.Input = floatOrZero(usage["input_tokens"])
					output.Usage.Output = floatOrZero(usage["output_tokens"])
					output.Usage.CacheRead = floatOrZero(usage["cache_read_input_tokens"])
					output.Usage.CacheWrite = floatOrZero(usage["cache_creation_input_tokens"])
					if cacheCreation, ok := usage["cache_creation"].(map[string]any); ok {
						value := floatOrZero(cacheCreation["ephemeral_1h_input_tokens"])
						output.Usage.CacheWrite1h = &value
					} else {
						value := 0.0
						output.Usage.CacheWrite1h = &value
					}
					output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
					calculateCost(usageModel, &output.Usage)
				}

			case "content_block_start":
				contentBlock, _ := event["content_block"].(map[string]any)
				if contentBlock == nil {
					return nil
				}
				blockType, _ := contentBlock["type"].(string)
				if blockType == "fallback" {
					if len(output.Content) > 0 {
						return fmt.Errorf("Anthropic performed an unsupported mid-output model fallback")
					}
					return nil
				}
				index, _ := intValue(event["index"])
				switch blockType {
				case "text":
					text, _ := contentBlock["text"].(string)
					block := &anthropicStreamBlock{index: index, kind: types.ContentTypeText, text: &types.TextContent{Type: types.ContentTypeText, Text: text}}
					blocks = append(blocks, block)
					output.Content = append(output.Content, block.contentBlock())
					stream.Push(types.NewTextStartEvent(len(output.Content)-1, output))
				case "thinking":
					thinking, _ := contentBlock["thinking"].(string)
					signature, _ := contentBlock["signature"].(string)
					block := &anthropicStreamBlock{index: index, kind: types.ContentTypeThinking, thinking: &types.ThinkingContent{Type: types.ContentTypeThinking, Thinking: thinking, ThinkingSignature: &signature}}
					blocks = append(blocks, block)
					output.Content = append(output.Content, block.contentBlock())
					stream.Push(types.NewThinkingStartEvent(len(output.Content)-1, output))
				case "redacted_thinking":
					data, _ := contentBlock["data"].(string)
					redacted := true
					block := &anthropicStreamBlock{index: index, kind: types.ContentTypeThinking, thinking: &types.ThinkingContent{Type: types.ContentTypeThinking, Thinking: "[Reasoning redacted]", ThinkingSignature: &data, Redacted: &redacted}}
					blocks = append(blocks, block)
					output.Content = append(output.Content, block.contentBlock())
					stream.Push(types.NewThinkingStartEvent(len(output.Content)-1, output))
				case "tool_use":
					id, _ := contentBlock["id"].(string)
					name, _ := contentBlock["name"].(string)
					if isOAuth {
						name = anthropicFromClaudeCodeName(name, utils.GetCurrentTools(normalizedContext.Messages))
					}
					arguments := contentBlock["input"]
					if arguments == nil {
						arguments = map[string]any{}
					}
					block := &anthropicStreamBlock{index: index, kind: types.ContentTypeToolCall, tool: &types.ToolCall{Type: types.ContentTypeToolCall, Id: id, Name: name, Arguments: rawJSON(arguments)}}
					blocks = append(blocks, block)
					output.Content = append(output.Content, block.contentBlock())
					stream.Push(types.NewToolCallStartEvent(len(output.Content)-1, output))
				}

			case "content_block_delta":
				delta, _ := event["delta"].(map[string]any)
				if delta == nil {
					return nil
				}
				index, _ := intValue(event["index"])
				position := anthropicFindBlock(blocks, index)
				if position == -1 {
					return nil
				}
				block := blocks[position]
				deltaType, _ := delta["type"].(string)
				switch deltaType {
				case "text_delta":
					if block.kind != types.ContentTypeText || block.text == nil {
						return nil
					}
					text, _ := delta["text"].(string)
					block.text.Text += text
					stream.Push(types.NewTextDeltaEvent(position, text, output))
				case "thinking_delta":
					if block.kind != types.ContentTypeThinking || block.thinking == nil {
						return nil
					}
					thinking, _ := delta["thinking"].(string)
					block.thinking.Thinking += thinking
					stream.Push(types.NewThinkingDeltaEvent(position, thinking, output))
				case "input_json_delta":
					if block.kind != types.ContentTypeToolCall || block.tool == nil {
						return nil
					}
					partial, _ := delta["partial_json"].(string)
					block.partialJSON += partial
					block.tool.Arguments = rawJSON(utils.ParseStreamingJSON(block.partialJSON))
					stream.Push(types.NewToolCallDeltaEvent(position, partial, output))
				case "signature_delta":
					if block.kind != types.ContentTypeThinking || block.thinking == nil {
						return nil
					}
					signature, _ := delta["signature"].(string)
					existing := ""
					if block.thinking.ThinkingSignature != nil {
						existing = *block.thinking.ThinkingSignature
					}
					combined := existing + signature
					block.thinking.ThinkingSignature = &combined
				}

			case "content_block_stop":
				index, _ := intValue(event["index"])
				position := anthropicFindBlock(blocks, index)
				if position == -1 {
					return nil
				}
				block := blocks[position]
				switch block.kind {
				case types.ContentTypeText:
					if block.text != nil {
						stream.Push(types.NewTextEndEvent(position, block.text.Text, output))
					}
				case types.ContentTypeThinking:
					if block.thinking != nil {
						stream.Push(types.NewThinkingEndEvent(position, block.thinking.Thinking, output))
					}
				case types.ContentTypeToolCall:
					if block.tool != nil {
						block.tool.Arguments = rawJSON(utils.ParseStreamingJSON(block.partialJSON))
						stream.Push(types.NewToolCallEndEvent(position, *block.tool, output))
					}
				}

			case "message_delta":
				if transformations, ok := event["input_transformations"].([]any); ok {
					if parsed := anthropicTransformationsFromValue(transformations); parsed != nil {
						inputTransformations = parsed
					}
				}
				delta, _ := event["delta"].(map[string]any)
				if delta != nil {
					if stopReason, ok := delta["stop_reason"].(string); ok && stopReason != "" {
						output.RawStopReason = &stopReason
						var stopDetails map[string]any
						if details, ok := delta["stop_details"].(map[string]any); ok {
							stopDetails = details
						}
						mapped, stopErr := mapAnthropicStopReason(stopReason, stopDetails)
						if mapped == "" {
							return fmt.Errorf("Unhandled stop reason: %s", stopReason)
						}
						output.StopReason = mapped
						if stopErr != nil {
							output.ErrorMessage = stopErr
						}
					}
				}
				if usage, ok := event["usage"].(map[string]any); ok {
					if value, present := usage["input_tokens"]; present && value != nil {
						output.Usage.Input = floatOrZero(value)
					}
					if value, present := usage["output_tokens"]; present && value != nil {
						output.Usage.Output = floatOrZero(value)
					}
					if value, present := usage["cache_read_input_tokens"]; present && value != nil {
						output.Usage.CacheRead = floatOrZero(value)
					}
					if value, present := usage["cache_creation_input_tokens"]; present && value != nil {
						output.Usage.CacheWrite = floatOrZero(value)
					}
					if details, ok := usage["output_tokens_details"].(map[string]any); ok {
						if value, present := details["thinking_tokens"]; present && value != nil {
							thinking := floatOrZero(value)
							output.Usage.Reasoning = &thinking
						}
					}
				}
				output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
				calculateCost(usageModel, &output.Usage)
			}
			return nil
		}

		sawMessageStart := false
		sawMessageEnd := false
		readErr := iterateAnthropicSSE(response.Body, signal, func(sse *anthropicServerSentEvent) error {
			if sse.event != nil && *sse.event == "error" {
				return fmt.Errorf("%s", sse.data)
			}
			if sse.event == nil || !anthropicMessageEvent(sse.event) {
				return nil
			}
			value, parseErr := utils.ParseJSONWithRepair(sse.data)
			if parseErr != nil {
				return fmt.Errorf("Could not parse Anthropic SSE event %s: %s; data=%s; raw=%s", anthropicEventName(sse.event), parseErr.Error(), sse.data, strings.Join(sse.raw, "\\n"))
			}
			event, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("Could not parse Anthropic SSE event %s: not an object; data=%s", anthropicEventName(sse.event), sse.data)
			}
			if options != nil && options.OnProviderStreamEvent != nil {
				if observeErr := options.OnProviderStreamEvent(event, model); observeErr != nil {
					return observeErr
				}
			}
			if eventType, _ := event["type"].(string); eventType == "message_start" {
				sawMessageStart = true
			} else if eventType, _ := event["type"].(string); eventType == "message_stop" {
				sawMessageEnd = true
			}
			return handleEvent(event)
		})
		if readErr != nil {
			fail(readErr)
			return
		}
		if sawMessageStart && !sawMessageEnd {
			fail(fmt.Errorf("Anthropic stream ended before message_stop"))
			return
		}
		if aborted(signal) {
			fail(fmt.Errorf("Request was aborted"))
			return
		}
		if output.StopReason == types.StopReasonPending {
			fail(fmt.Errorf("Anthropic stream ended without a stop reason"))
			return
		}
		if output.StopReason == types.StopReasonAborted || output.StopReason == types.StopReasonError {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			fail(fmt.Errorf("%s", message))
			return
		}

		anthropicRecordInputTransformations(&output, inputTransformations)

		stream.Push(types.NewDoneEvent(output.StopReason, output))
		stream.End(&output)
	}()

	return stream
}

func anthropicOptionSignal(options *AnthropicOptions) <-chan struct{} {
	if options == nil {
		return nil
	}
	return options.Signal
}

var anthropicMessageEventTypes = map[string]bool{
	"message_start":       true,
	"message_delta":       true,
	"message_stop":        true,
	"content_block_start": true,
	"content_block_delta": true,
	"content_block_stop":  true,
}

func anthropicMessageEvent(event *string) bool {
	if event == nil {
		return false
	}
	return anthropicMessageEventTypes[*event]
}

func anthropicEventName(event *string) string {
	if event == nil {
		return ""
	}
	return *event
}

func floatOrZero(value any) float64 {
	if value == nil {
		return 0
	}
	if parsed, ok := floatValue(value); ok {
		return parsed
	}
	return 0
}

// =============================================================================
// Simple stream entry point
// =============================================================================

func mapThinkingLevelToEffort(model *types.Model, level *types.SimpleStreamOptions) AnthropicEffort {
	if level != nil && level.Reasoning != nil {
		if value, present, supported := model.ThinkingLevelMap.Lookup(*level.Reasoning); present && supported {
			return AnthropicEffort(value)
		}
	}
	if level == nil || level.Reasoning == nil {
		return AnthropicEffortHigh
	}
	switch *level.Reasoning {
	case types.ThinkingMinimal, types.ThinkingLow:
		return AnthropicEffortLow
	case types.ThinkingMedium:
		return AnthropicEffortMedium
	case types.ThinkingHigh:
		return AnthropicEffortHigh
	default:
		return AnthropicEffortHigh
	}
}

func mapThinkingLevelValue(model *types.Model, value string) AnthropicEffort {
	if mapped, present, supported := model.ThinkingLevelMap.Lookup(types.ModelThinkingLevel(value)); present && supported {
		return AnthropicEffort(mapped)
	}
	switch value {
	case "minimal", "low":
		return AnthropicEffortLow
	case "medium":
		return AnthropicEffortMedium
	case "high":
		return AnthropicEffortHigh
	default:
		return AnthropicEffortHigh
	}
}

// AnthropicMessagesStreamSimple is the unified-reasoning entry point for the
// Anthropic Messages API.
func AnthropicMessagesStreamSimple(model *types.Model, transcript *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	var apiKey *string
	var headers types.ProviderHeaders
	if options != nil {
		apiKey = options.APIKey
		headers = options.Headers
	}
	if err := assertAnthropicRequestAuth(model.Provider, apiKey, headers); err != nil {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
			terminal := terminateAnthropicStream
			terminal(stream, &output, err, false)
		}()
		return stream
	}

	base := BuildBaseOptions(model, transcript, options, apiKey)
	typed := &AnthropicOptions{StreamOptions: base}
	if options != nil && options.ToolChoice != nil {
		typed.ToolChoice = *options.ToolChoice
	}

	if options == nil || options.Reasoning == nil {
		disabled := false
		typed.ThinkingEnabled = &disabled
		return AnthropicMessagesStream(model, transcript, typed)
	}

	compat := getAnthropicCompat(model)
	if compat.forceAdaptiveThinking {
		effort := mapThinkingLevelToEffort(model, options)
		enabled := true
		typed.ThinkingEnabled = &enabled
		typed.Effort = &effort
		return AnthropicMessagesStream(model, transcript, typed)
	}

	var baseMaxTokens *float64
	if base.MaxTokens != nil {
		value := float64(*base.MaxTokens)
		baseMaxTokens = &value
	}
	adjusted := AdjustMaxTokensForThinking(baseMaxTokens, model.MaxTokens, *options.Reasoning, options.ThinkingBudgets)
	maxTokens := ClampMaxTokensToContext(model, transcript, float64(adjusted.MaxTokens))
	room := int(maxTokens) - MinAnswerTokens
	if room < 0 {
		room = 0
	}
	thinkingBudget := adjusted.ThinkingBudget
	if thinkingBudget > room {
		thinkingBudget = room
	}
	maxTokensInt := int(maxTokens)
	enabled := true
	typed.MaxTokens = &maxTokensInt
	typed.ThinkingEnabled = &enabled
	typed.ThinkingBudgetTokens = &thinkingBudget
	return AnthropicMessagesStream(model, transcript, typed)
}
