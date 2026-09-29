// This file is a Go port of packages/ai/src/api/openai-responses-shared.ts from
// Pi at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The OpenAI Responses family (OpenAI, Azure, Codex) shares message/tool
// conversion and the SSE response processor. This file also carries the small
// Go support helpers the api package needs but cannot import from the root
// packages/ai package without creating an import cycle: model cost
// calculation, thinking-level clamping, bounded SSE/HTTP plumbing and JSON
// accessors. Those helpers mirror the upstream functions they replace.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// =============================================================================
// Shared support helpers
// =============================================================================

// calculateCost mirrors packages/ai CalculateCost without importing the root
// package. It applies the highest matching request-wide tier and doubles base
// input for 1h cache writes.
func calculateCost(model *types.Model, usage *types.Usage) {
	if model == nil || usage == nil {
		return
	}
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	rates := model.Cost.ModelCostRates
	matchedThreshold := -1.0
	for _, tier := range model.Cost.Tiers {
		if inputTokens > tier.InputTokensAbove && tier.InputTokensAbove > matchedThreshold {
			rates = tier.ModelCostRates
			matchedThreshold = tier.InputTokensAbove
		}
	}
	longWrite := 0.0
	if usage.CacheWrite1h != nil {
		longWrite = *usage.CacheWrite1h
	}
	shortWrite := usage.CacheWrite - longWrite
	usage.Cost.Input = (rates.Input / 1000000) * usage.Input
	usage.Cost.Output = (rates.Output / 1000000) * usage.Output
	usage.Cost.CacheRead = (rates.CacheRead / 1000000) * usage.CacheRead
	usage.Cost.CacheWrite = (rates.CacheWrite*shortWrite + rates.Input*2*longWrite) / 1000000
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}

var extendedThinkingLevels = []types.ModelThinkingLevel{
	types.ThinkingOff,
	types.ThinkingMinimal,
	types.ThinkingLow,
	types.ThinkingMedium,
	types.ThinkingHigh,
	types.ThinkingXHigh,
	types.ThinkingMax,
}

// clampThinkingLevel mirrors packages/ai ClampThinkingLevel.
func clampThinkingLevel(model *types.Model, level types.ModelThinkingLevel) types.ModelThinkingLevel {
	if model == nil {
		return types.ThinkingOff
	}
	available := supportedThinkingLevels(model)
	for _, candidate := range available {
		if candidate == level {
			return level
		}
	}
	requestedIndex := -1
	for i, candidate := range extendedThinkingLevels {
		if candidate == level {
			requestedIndex = i
			break
		}
	}
	if requestedIndex == -1 {
		if len(available) > 0 {
			return available[0]
		}
		return types.ThinkingOff
	}
	for i := requestedIndex; i < len(extendedThinkingLevels); i++ {
		for _, candidate := range available {
			if candidate == extendedThinkingLevels[i] {
				return candidate
			}
		}
	}
	for i := requestedIndex - 1; i >= 0; i-- {
		for _, candidate := range available {
			if candidate == extendedThinkingLevels[i] {
				return candidate
			}
		}
	}
	if len(available) > 0 {
		return available[0]
	}
	return types.ThinkingOff
}

func supportedThinkingLevels(model *types.Model) []types.ModelThinkingLevel {
	if !model.Reasoning {
		return []types.ModelThinkingLevel{types.ThinkingOff}
	}
	levels := []types.ModelThinkingLevel{}
	for _, level := range extendedThinkingLevels {
		_, present, supported := model.ThinkingLevelMap.Lookup(level)
		if present && !supported {
			continue
		}
		if (level == types.ThinkingXHigh || level == types.ThinkingMax) && !present {
			continue
		}
		levels = append(levels, level)
	}
	return levels
}

// aborted reports whether an explicit cancellation signal has fired.
func aborted(signal <-chan struct{}) bool {
	if signal == nil {
		return false
	}
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

// contextForSignal couples an operation context with the explicit abort signal.
// The returned cancel func must be called to release the watcher.
func contextForSignal(ctx context.Context, signal <-chan struct{}) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if signal == nil {
		return context.WithCancel(ctx)
	}
	child, cancel := context.WithCancel(ctx)
	// Cancel synchronously when the signal already fired so callers observe the
	// abort before any request is attempted.
	select {
	case <-signal:
		cancel()
		return child, func() { cancel() }
	default:
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-signal:
			cancel()
		case <-done:
		case <-child.Done():
		}
	}()
	return child, func() { close(done); cancel() }
}

// providerError carries the HTTP status and body so NormalizeProviderError can
// fold them into a display message.
type providerError struct {
	status  int
	body    string
	message string
}

func (e *providerError) Error() string {
	if e.message != "" {
		return e.message
	}
	return fmt.Sprintf("%d: %s", e.status, e.body)
}

// ProviderErrorFields exposes the SDK-shaped fields probed by
// utils.NormalizeProviderError.
func (e *providerError) ProviderErrorFields() map[string]any {
	return map[string]any{
		"status": e.status,
		"body":   e.body,
	}
}

func fmtProviderError(err error, prefix *string) string {
	return utils.FormatProviderError(utils.NormalizeProviderError(err), prefix)
}

// doProviderRequest performs an HTTP request using the injected fetch hook when
// present, otherwise the process HTTP client. The response body is always
// available as a stream.
func doProviderRequest(ctx context.Context, options *types.ProviderRequestOptions, method, url string, headers map[string]string, body []byte) (*http.Response, error) {
	if options != nil && options.Fetch != nil {
		response, err := options.Fetch(&types.HTTPRequest{Method: method, URL: url, Headers: headers, Body: body})
		if err != nil {
			return nil, err
		}
		status := 200
		responseHeaders := http.Header{}
		if response != nil {
			status = response.Status
			for key, value := range response.Headers {
				responseHeaders.Set(key, value)
			}
		}
		responseBody := []byte(nil)
		if response != nil {
			responseBody = response.Body
		}
		return &http.Response{
			StatusCode: status,
			Header:     responseHeaders,
			Body:       io.NopCloser(bytes.NewReader(responseBody)),
			Status:     http.StatusText(status),
		}, nil
	}

	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	return http.DefaultClient.Do(request)
}

type httpResult struct {
	response *http.Response
	body     []byte
}

// performRequestWithRetry performs the request and normalizes a non-2xx status
// into a providerError so utils.RetryProviderRequest can classify it.
func performRequestWithRetry(ctx context.Context, options *types.ProviderRequestOptions, method, url string, headers map[string]string, body []byte) (*http.Response, []byte, error) {
	request := func() (httpResult, error) {
		response, err := doProviderRequest(ctx, options, method, url, headers, body)
		if err != nil {
			return httpResult{}, err
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return httpResult{response: response}, nil
		}
		raw, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			return httpResult{}, readErr
		}
		return httpResult{}, &providerError{status: response.StatusCode, body: string(raw)}
	}

	maxRetries := options.MaxRetries
	maxRetryDelay := (*float64)(nil)
	if options.MaxRetryDelayMs != nil {
		value := float64(*options.MaxRetryDelayMs)
		maxRetryDelay = &value
	}
	var result httpResult
	var err error
	if maxRetries != nil && *maxRetries > 0 {
		result, err = utils.RetryProviderRequest(ctx, request, utils.ProviderRetryOptions{
			MaxRetries:      maxRetries,
			MaxRetryDelayMs: maxRetryDelay,
			Signal:          ctx,
		})
	} else {
		result, err = request()
	}
	if err != nil {
		return nil, nil, err
	}
	return result.response, result.body, nil
}

// readSSE parses a server-sent event stream, invoking yield for every decoded
// JSON `data:` payload. Frames are terminated by a blank line; the residual
// frame at EOF is processed. `[DONE]` is ignored.
func readSSE(ctx context.Context, body io.Reader, signal <-chan struct{}, yield func(map[string]any) error) error {
	reader := bufioNewReader(body)
	buffer := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if aborted(signal) {
			return fmt.Errorf("Request was aborted")
		}
		chunk := make([]byte, 8192)
		n, readErr := reader.Read(chunk)
		if n > 0 {
			buffer += string(chunk[:n])
		}
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		done := readErr == io.EOF
		if done && strings.TrimSpace(buffer) != "" {
			buffer += "\n\n"
		}
		for {
			index := strings.Index(buffer, "\n\n")
			if index == -1 {
				break
			}
			frame := buffer[:index]
			buffer = buffer[index+2:]
			dataLines := []string{}
			for _, line := range strings.Split(frame, "\n") {
				line = strings.TrimSuffix(line, "\r")
				if strings.HasPrefix(line, "data:") {
					dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				}
			}
			if len(dataLines) == 0 {
				continue
			}
			data := strings.TrimSpace(strings.Join(dataLines, "\n"))
			if data == "" || data == "[DONE]" {
				continue
			}
			event, err := decodeJSONObject([]byte(data))
			if err != nil {
				return fmt.Errorf("Invalid SSE JSON: %w", err)
			}
			if yieldErr := yield(event); yieldErr != nil {
				return yieldErr
			}
		}
		if done {
			return nil
		}
	}
}

// decodeJSONObject decodes a JSON object preserving numeric precision.
func decodeJSONObject(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

// jsonRaw marshals an arbitrary parsed JSON value back to raw JSON.
func jsonRaw(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

// jsonObject converts an arbitrary value into a JSON object map.
func jsonObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func stringValue(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func floatValue(value any) (float64, bool) {
	switch v := value.(type) {
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case json.Number:
		if parsed, err := v.Float64(); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func boolValue(value any) (bool, bool) {
	flag, ok := value.(bool)
	return flag, ok
}

// =============================================================================
// Public option types
// =============================================================================

// OpenAIResponsesStreamOptions are the stream options shared by the OpenAI,
// Azure and Codex Responses processors.
type OpenAIResponsesStreamOptions struct {
	// ServiceTier is the requested service tier, used for pricing.
	ServiceTier *string
	// GrammarToolInputProperties maps grammar-constrained tool names to their
	// single string input property.
	GrammarToolInputProperties map[string]string
	// ResolveServiceTier reconciles the response and request service tiers.
	ResolveServiceTier func(responseServiceTier *string, requestServiceTier *string) *string
	// ApplyServiceTierPricing applies a tier-specific cost multiplier.
	ApplyServiceTierPricing func(usage *types.Usage, serviceTier *string)
}

// ConvertResponsesMessagesOptions are the options for response input
// conversion.
type ConvertResponsesMessagesOptions struct {
	IncludeSystemPrompt            *bool
	GrammarToolInputProperties     map[string]string
	SupportsMidConvoSystemMessages bool
	SupportsAdditionalTools        bool
	SupportsToolSearch             bool
	ToolOptions                    *ConvertResponsesToolsOptions
}

// ConvertResponsesToolsOptions are the options for response tool conversion.
type ConvertResponsesToolsOptions struct {
	// Strict is the default strict flag (upstream `boolean | null`). StrictNull
	// records the explicit JSON null form, which differs from an omitted value.
	Strict                     *bool
	StrictNull                 bool
	SupportsStrictMode         *bool
	SupportsOpenAIGrammarTools *bool
	ToolSearchResult           bool
}

func supportsStrictModeDefault(options *ConvertResponsesToolsOptions) bool {
	if options == nil || options.SupportsStrictMode == nil {
		return true
	}
	return *options.SupportsStrictMode
}

func supportsGrammarToolsDefault(options *ConvertResponsesToolsOptions) bool {
	if options == nil || options.SupportsOpenAIGrammarTools == nil {
		return false
	}
	return *options.SupportsOpenAIGrammarTools
}

// =============================================================================
// Message conversion
// =============================================================================

func encodeTextSignatureV1(id string, phase *types.TextSignaturePhase) string {
	payload := types.TextSignatureV1{V: 1, Id: id, Phase: phase}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func parseTextSignature(signature *string) (string, *types.TextSignaturePhase, bool) {
	if signature == nil || *signature == "" {
		return "", nil, false
	}
	raw := *signature
	if strings.HasPrefix(raw, "{") {
		if parsed := types.ParseTextSignatureV1(raw); parsed != nil {
			return parsed.Id, parsed.Phase, true
		}
	}
	return raw, nil, true
}

type toolResultOutput struct {
	// Text is set for the plain string form.
	Text string
	// Parts is set for the multi-part content form.
	Parts []map[string]any
	// IsParts discriminates the two forms.
	IsParts bool
}

func convertToolResultOutput(model *types.Model, content []types.ContentBlock) toolResultOutput {
	parts := []string{}
	for _, block := range content {
		if block.Type == types.ContentTypeText && block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	textResult := strings.Join(parts, "\n")
	images := []*types.ImageContent{}
	for i := range content {
		if content[i].Type == types.ContentTypeImage && content[i].Image != nil {
			images = append(images, content[i].Image)
		}
	}
	hasText := len(textResult) > 0
	supportsImages := model != nil && model.SupportsImageInput()

	if len(images) == 0 || !supportsImages {
		switch {
		case hasText:
			return toolResultOutput{Text: utils.SanitizeSurrogates(textResult)}
		case len(images) > 0:
			return toolResultOutput{Text: "(see attached image)"}
		default:
			return toolResultOutput{Text: "(no tool output)"}
		}
	}

	out := toolResultOutput{IsParts: true}
	if hasText {
		out.Parts = append(out.Parts, map[string]any{"type": "input_text", "text": utils.SanitizeSurrogates(textResult)})
	}
	for _, image := range images {
		out.Parts = append(out.Parts, map[string]any{
			"type":      "input_image",
			"detail":    "auto",
			"image_url": "data:" + image.MimeType + ";base64," + image.Data,
		})
	}
	return out
}

// ConvertResponsesMessages converts a transcript into OpenAI Responses input
// items. The allowedToolCallProviders set controls which providers keep their
// pipe-separated item ids.
func ConvertResponsesMessages(model *types.Model, context *types.TranscriptContext, allowedToolCallProviders map[string]bool, options *ConvertResponsesMessagesOptions) []map[string]any {
	supportsMidConvo := options != nil && options.SupportsMidConvoSystemMessages
	normalizedContext := utils.ResolveTranscript(*context, &supportsMidConvo)
	messages := []map[string]any{}

	normalizeIDPart := func(part string) string {
		var builder strings.Builder
		for _, r := range part {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
				builder.WriteRune(r)
			} else {
				builder.WriteRune('_')
			}
		}
		sanitized := builder.String()
		if len(sanitized) > 64 {
			sanitized = sanitized[:64]
		}
		return strings.TrimRight(sanitized, "_")
	}

	buildForeignResponsesItemID := func(itemID string) string {
		normalized := "fc_" + utils.ShortHash(itemID)
		if len(normalized) > 64 {
			normalized = normalized[:64]
		}
		return normalized
	}

	normalizeToolCallID := func(id string, _ *types.Model, source types.AssistantMessage) string {
		if !allowedToolCallProviders[string(model.Provider)] {
			return normalizeIDPart(id)
		}
		if !strings.Contains(id, "|") {
			return normalizeIDPart(id)
		}
		parts := strings.SplitN(id, "|", 2)
		callID, itemID := parts[0], parts[1]
		normalizedCallID := normalizeIDPart(callID)
		isForeign := source.Provider != model.Provider || source.Api != model.Api
		normalizedItemID := normalizeIDPart(itemID)
		if isForeign {
			normalizedItemID = buildForeignResponsesItemID(itemID)
		}
		if !strings.HasPrefix(normalizedItemID, "fc_") {
			normalizedItemID = normalizeIDPart("fc_" + normalizedItemID)
		}
		return normalizedCallID + "|" + normalizedItemID
	}

	transformedMessages := TransformMessages(normalizedContext.Messages, model, normalizeToolCallID)
	supportsToolAdditions := false
	if options != nil {
		supportsToolAdditions = options.SupportsAdditionalTools || options.SupportsToolSearch
	}
	transcriptTools := utils.ResolveTranscriptTools(normalizedContext.Messages, supportsToolAdditions)

	appendSystemToolAdditions := func(message types.SystemMessage, seed string) {
		tools := []types.Tool{}
		if transcriptTools.AnchorsAdditions {
			tools = message.ToolsAdded
		}
		if len(tools) == 0 {
			return
		}
		if options != nil && options.SupportsAdditionalTools {
			converted := ConvertResponsesTools(tools, options.ToolOptions)
			messages = append(messages, map[string]any{
				"type":  "additional_tools",
				"role":  "developer",
				"tools": converted,
			})
			return
		}
		if options == nil || !options.SupportsToolSearch {
			return
		}
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, tool.Name)
		}
		callID := "pi_tool_load_" + utils.ShortHash(seed+":"+strings.Join(names, ","))
		messages = append(messages, map[string]any{
			"type":      "tool_search_call",
			"call_id":   callID,
			"execution": "client",
			"status":    "completed",
			"arguments": map[string]any{"query": strings.Join(names, " "), "limit": len(names)},
		})
		toolOptions := &ConvertResponsesToolsOptions{ToolSearchResult: true}
		if options.ToolOptions != nil {
			copyOptions := *options.ToolOptions
			copyOptions.ToolSearchResult = true
			toolOptions = &copyOptions
		}
		messages = append(messages, map[string]any{
			"type":      "tool_search_output",
			"call_id":   callID,
			"execution": "client",
			"status":    "completed",
			"tools":     ConvertResponsesTools(tools, toolOptions),
		})
	}

	includeSystemPrompt := true
	if options != nil && options.IncludeSystemPrompt != nil {
		includeSystemPrompt = *options.IncludeSystemPrompt
	}
	supportsDeveloperRole := true
	if compat := responsesCompat(model); compat != nil && compat.SupportsDeveloperRole != nil {
		supportsDeveloperRole = *compat.SupportsDeveloperRole
	}
	instructionRole := "system"
	if model.Reasoning && supportsDeveloperRole {
		instructionRole = "developer"
	}

	msgIndex := 0
	sourceIndex := 0
	for _, message := range transformedMessages {
		isLeadingSystemMessage := sourceIndex == 0 && message.Role == types.SystemMessageRole
		sourceIndex++

		switch message.Role {
		case types.SystemMessageRole:
			if message.System == nil {
				continue
			}
			if !isLeadingSystemMessage {
				appendSystemToolAdditions(*message.System, fmt.Sprintf("system:%d", msgIndex))
			}
			if !isLeadingSystemMessage || includeSystemPrompt {
				text := ""
				if isLeadingSystemMessage {
					text = utils.GetSystemMessageText(*message.System)
				} else {
					text = utils.RenderSystemMessageUpdate(*message.System)
				}
				if len(text) > 0 {
					messages = append(messages, map[string]any{"role": instructionRole, "content": utils.SanitizeSurrogates(text)})
				}
			}

		case types.UserMessageRole:
			if message.User == nil {
				continue
			}
			if !message.User.Content.Structured {
				messages = append(messages, map[string]any{
					"role":    "user",
					"content": []map[string]any{{"type": "input_text", "text": utils.SanitizeSurrogates(message.User.Content.Text)}},
				})
				break
			}
			content := []map[string]any{}
			for _, block := range message.User.Content.Blocks {
				if block.Type == types.ContentTypeText && block.Text != nil {
					content = append(content, map[string]any{"type": "input_text", "text": utils.SanitizeSurrogates(block.Text.Text)})
				} else if block.Type == types.ContentTypeImage && block.Image != nil {
					content = append(content, map[string]any{
						"type":      "input_image",
						"detail":    "auto",
						"image_url": "data:" + block.Image.MimeType + ";base64," + block.Image.Data,
					})
				}
			}
			if len(content) == 0 {
				break
			}
			messages = append(messages, map[string]any{"role": "user", "content": content})

		case types.AssistantMessageRole:
			if message.Assistant == nil {
				continue
			}
			assistantMsg := *message.Assistant
			isSameProviderAndAPI := assistantMsg.Provider == model.Provider && assistantMsg.Api == model.Api
			isSameModel := isSameProviderAndAPI && assistantMsg.Model == model.Id
			isDifferentModel := isSameProviderAndAPI && assistantMsg.Model != model.Id
			textBlockIndex := 0

			for _, block := range assistantMsg.Content {
				switch block.Type {
				case types.ContentTypeThinking:
					if block.Thinking != nil && block.Thinking.ThinkingSignature != nil {
						var reasoningItem map[string]any
						if err := json.Unmarshal([]byte(*block.Thinking.ThinkingSignature), &reasoningItem); err == nil {
							messages = append(messages, reasoningItem)
						}
					}
				case types.ContentTypeText:
					if block.Text == nil {
						continue
					}
					textBlock := block.Text
					parsedID, parsedPhase, ok := parseTextSignature(textBlock.TextSignature)
					fallbackMessageID := fmt.Sprintf("msg_pi_%d", msgIndex)
					if textBlockIndex > 0 {
						fallbackMessageID = fmt.Sprintf("msg_pi_%d_%d", msgIndex, textBlockIndex)
					}
					textBlockIndex++
					msgID := parsedID
					if !ok || msgID == "" {
						msgID = fallbackMessageID
					} else if len(msgID) > 64 {
						msgID = "msg_" + utils.ShortHash(msgID)
					}
					item := map[string]any{
						"type":    "message",
						"role":    "assistant",
						"content": []map[string]any{{"type": "output_text", "text": utils.SanitizeSurrogates(textBlock.Text), "annotations": []any{}}},
						"status":  "completed",
						"id":      msgID,
					}
					if parsedPhase != nil {
						item["phase"] = string(*parsedPhase)
					}
					messages = append(messages, item)
				case types.ContentTypeToolCall:
					if block.ToolCall == nil {
						continue
					}
					toolCall := block.ToolCall
					parts := strings.SplitN(toolCall.Id, "|", 2)
					callID := parts[0]
					itemIDRaw := ""
					if len(parts) > 1 {
						itemIDRaw = parts[1]
					}
					var customInputProperty *string
					if options != nil && options.GrammarToolInputProperties != nil {
						if property, present := options.GrammarToolInputProperties[toolCall.Name]; present {
							value := property
							customInputProperty = &value
						}
					}
					itemID := itemIDRaw
					itemIDNil := false
					if isDifferentModel && strings.HasPrefix(itemID, "fc_") {
						itemIDNil = true
					} else if customInputProperty == nil && !strings.HasPrefix(itemID, "fc_") {
						itemIDNil = true
					}
					if customInputProperty != nil {
						input, err := GetGrammarToolInput(toolCall.Name, toolCallArguments(toolCall), *customInputProperty)
						if err != nil {
							input = ""
						}
						item := map[string]any{
							"type":    "custom_tool_call",
							"call_id": callID,
							"name":    toolCall.Name,
							"input":   utils.SanitizeSurrogates(input),
						}
						if !itemIDNil {
							item["id"] = itemID
						}
						if isSameModel && toolCall.Namespace != nil {
							item["namespace"] = *toolCall.Namespace
						}
						messages = append(messages, item)
					} else {
						item := map[string]any{
							"type":      "function_call",
							"call_id":   callID,
							"name":      toolCall.Name,
							"arguments": string(toolCall.Arguments),
						}
						if !itemIDNil {
							item["id"] = itemID
						}
						if isSameModel && toolCall.Namespace != nil {
							item["namespace"] = *toolCall.Namespace
						}
						messages = append(messages, item)
					}
				}
			}

		case types.ToolResultMessageRole:
			if message.ToolResult == nil {
				continue
			}
			callID := strings.SplitN(message.ToolResult.ToolCallId, "|", 2)[0]
			output := convertToolResultOutput(model, message.ToolResult.Content)
			isCustom := false
			if options != nil && options.GrammarToolInputProperties != nil {
				_, isCustom = options.GrammarToolInputProperties[message.ToolResult.ToolName]
			}
			var outputValue any
			if output.IsParts {
				outputValue = output.Parts
			} else {
				outputValue = output.Text
			}
			if isCustom {
				messages = append(messages, map[string]any{"type": "custom_tool_call_output", "call_id": callID, "output": outputValue})
			} else {
				messages = append(messages, map[string]any{"type": "function_call_output", "call_id": callID, "output": outputValue})
			}
		}

		if !isLeadingSystemMessage {
			msgIndex++
		}
	}

	return messages
}

func toolCallArguments(toolCall *types.ToolCall) map[string]any {
	if toolCall == nil || len(toolCall.Arguments) == 0 {
		return map[string]any{}
	}
	decoder := json.NewDecoder(bytes.NewReader(toolCall.Arguments))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return map[string]any{}
	}
	return value
}

func responsesCompat(model *types.Model) *types.OpenAIResponsesCompat {
	if model == nil {
		return nil
	}
	return model.Compat.OpenAIResponses
}

// =============================================================================
// Tool conversion
// =============================================================================

// ConvertResponsesTools converts tools into the Responses API tool shape.
func ConvertResponsesTools(tools []types.Tool, options *ConvertResponsesToolsOptions) []map[string]any {
	defaultStrict := false
	strictIsNull := false
	if options != nil && options.StrictNull {
		strictIsNull = true
	}
	if options != nil && options.Strict != nil {
		defaultStrict = *options.Strict
	}
	supportsStrictMode := supportsStrictModeDefault(options)
	supportsGrammarTools := supportsGrammarToolsDefault(options)
	toolSearchResult := options != nil && options.ToolSearchResult

	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		grammar, err := ResolveGrammarConstrainedSampling(tool, supportsGrammarTools)
		if err == nil && grammar != nil {
			item := map[string]any{
				"type":        "custom",
				"name":        tool.Name,
				"description": tool.Description,
				"format": map[string]any{
					"type":       "grammar",
					"syntax":     grammar.Format,
					"definition": grammar.Definition,
				},
			}
			if toolSearchResult {
				item["defer_loading"] = true
			}
			result = append(result, item)
			continue
		}

		constrainedStrict, strictErr := ResolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		strict := (*bool)(nil)
		if strictErr == nil && constrainedStrict != nil {
			strict = constrainedStrict
		} else if strictIsNull {
			strict = nil
		} else {
			value := defaultStrict
			strict = &value
		}

		parameters, _ := GetJSONSchemaToolParameters(tool, strict)
		item := map[string]any{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  parameters,
		}
		if toolSearchResult {
			item["defer_loading"] = true
		}
		if supportsStrictMode {
			item["strict"] = strict
		}
		result = append(result, item)
	}
	return result
}

// =============================================================================
// Stream processing
// =============================================================================

type customToolInput struct {
	property   string
	jsonBuffer GrammarToolInputJSONBuffer
}

type streamingToolCall struct {
	tool         *types.ToolCall
	partialJSON  *string
	customInput  *customToolInput
	contentIndex int
}

type responsesOutputSlot struct {
	kind         string
	contentIndex int
	thinking     *types.ThinkingContent
	text         *types.TextContent
	tool         *streamingToolCall
}

type responsesStreamState struct {
	output                   *types.AssistantMessage
	stream                   *types.AssistantMessageEventStream
	model                    *types.Model
	options                  *OpenAIResponsesStreamOptions
	sawTerminalResponseEvent bool
	outputSlots              map[int]*responsesOutputSlot
	reasoningBlocksByID      map[string]*types.ThinkingContent
}

func (s *responsesStreamState) getSlot(outputIndex int, kind string) *responsesOutputSlot {
	slot, ok := s.outputSlots[outputIndex]
	if !ok || slot.kind != kind {
		return nil
	}
	return slot
}

func (s *responsesStreamState) pushToolCallDelta(slot *responsesOutputSlot, delta *string) {
	if delta == nil {
		return
	}
	s.stream.Push(types.NewToolCallDeltaEvent(slot.contentIndex, *delta, *s.output))
}

func (s *responsesStreamState) writeToolArguments(slot *responsesOutputSlot, arguments json.RawMessage) {
	if slot.tool != nil {
		slot.tool.tool.Arguments = arguments
		if slot.contentIndex < len(s.output.Content) {
			s.output.Content[slot.contentIndex].ToolCall = slot.tool.tool
		}
	}
}

func (s *responsesStreamState) createSlot(outputIndex int, item map[string]any) *responsesOutputSlot {
	itemType, _ := stringValue(item["type"])
	switch itemType {
	case "reasoning":
		s.output.Content = append(s.output.Content, types.ThinkingBlock(""))
		blockPtr := s.output.Content[len(s.output.Content)-1].Thinking
		slot := &responsesOutputSlot{kind: "thinking", contentIndex: len(s.output.Content) - 1, thinking: blockPtr}
		s.outputSlots[outputIndex] = slot
		s.stream.Push(types.NewThinkingStartEvent(slot.contentIndex, *s.output))
		return slot
	case "message":
		s.applyMessagePhaseStopReason(item)
		s.output.Content = append(s.output.Content, types.TextBlock(""))
		textPtr := s.output.Content[len(s.output.Content)-1].Text
		slot := &responsesOutputSlot{kind: "text", contentIndex: len(s.output.Content) - 1, text: textPtr}
		s.outputSlots[outputIndex] = slot
		s.stream.Push(types.NewTextStartEvent(slot.contentIndex, *s.output))
		return slot
	case "function_call":
		callID, _ := stringValue(item["call_id"])
		itemID, _ := stringValue(item["id"])
		name, _ := stringValue(item["name"])
		arguments, _ := stringValue(item["arguments"])
		tool := &types.ToolCall{Type: types.ContentTypeToolCall, Id: callID + "|" + itemID, Name: name, Arguments: json.RawMessage("{}")}
		if namespace, ok := stringValue(item["namespace"]); ok {
			tool.Namespace = &namespace
		}
		block := types.ToolCallBlock(*tool)
		s.output.Content = append(s.output.Content, block)
		partialJSON := arguments
		slot := &responsesOutputSlot{
			kind:         "toolCall",
			contentIndex: len(s.output.Content) - 1,
			tool:         &streamingToolCall{tool: tool, partialJSON: &partialJSON, contentIndex: len(s.output.Content) - 1},
		}
		s.output.Content[slot.contentIndex].ToolCall = tool
		s.outputSlots[outputIndex] = slot
		s.stream.Push(types.NewToolCallStartEvent(slot.contentIndex, *s.output))
		return slot
	case "custom_tool_call":
		callID, _ := stringValue(item["call_id"])
		itemID, _ := stringValue(item["id"])
		name, _ := stringValue(item["name"])
		inputProperty := "input"
		if s.options != nil && s.options.GrammarToolInputProperties != nil {
			if property, present := s.options.GrammarToolInputProperties[name]; present {
				inputProperty = property
			}
		}
		input, _ := stringValue(item["input"])
		tool := &types.ToolCall{Type: types.ContentTypeToolCall, Id: callID + "|" + itemID, Name: name, Arguments: jsonRaw(map[string]any{inputProperty: input})}
		if namespace, ok := stringValue(item["namespace"]); ok {
			tool.Namespace = &namespace
		}
		s.output.Content = append(s.output.Content, types.ToolCallBlock(*tool))
		slot := &responsesOutputSlot{
			kind:         "toolCall",
			contentIndex: len(s.output.Content) - 1,
			tool: &streamingToolCall{
				tool:         tool,
				customInput:  &customToolInput{property: inputProperty, jsonBuffer: GrammarToolInputJSONBuffer{}},
				contentIndex: len(s.output.Content) - 1,
			},
		}
		s.output.Content[slot.contentIndex].ToolCall = tool
		s.outputSlots[outputIndex] = slot
		s.stream.Push(types.NewToolCallStartEvent(slot.contentIndex, *s.output))
		return slot
	}
	return nil
}

func (s *responsesStreamState) getOrCreateSlot(outputIndex int, item map[string]any) *responsesOutputSlot {
	if slot, ok := s.outputSlots[outputIndex]; ok {
		return slot
	}
	return s.createSlot(outputIndex, item)
}

func (s *responsesStreamState) applyMessagePhaseStopReason(item map[string]any) {
	if itemType, _ := stringValue(item["type"]); itemType == "message" {
		if phase, _ := stringValue(item["phase"]); phase == "final_answer" {
			s.output.StopReason = types.StopReasonStop
		}
	}
}

func (s *responsesStreamState) applyEvent(event map[string]any) error {
	eventType, _ := stringValue(event["type"])
	switch eventType {
	case "response.created":
		if response := jsonObject(event["response"]); response != nil {
			if id, ok := stringValue(response["id"]); ok {
				s.output.ResponseId = &id
			}
		}
	case "response.output_item.added":
		outputIndex, _ := intValue(event["output_index"])
		if item := jsonObject(event["item"]); item != nil {
			s.createSlot(outputIndex, item)
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		outputIndex, _ := intValue(event["output_index"])
		slot := s.getSlot(outputIndex, "thinking")
		if slot == nil {
			return nil
		}
		delta, _ := stringValue(event["delta"])
		slot.thinking.Thinking += delta
		s.stream.Push(types.NewThinkingDeltaEvent(slot.contentIndex, delta, *s.output))
	case "response.reasoning_summary_part.done":
		outputIndex, _ := intValue(event["output_index"])
		slot := s.getSlot(outputIndex, "thinking")
		if slot == nil {
			return nil
		}
		slot.thinking.Thinking += "\n\n"
		s.stream.Push(types.NewThinkingDeltaEvent(slot.contentIndex, "\n\n", *s.output))
	case "response.output_text.delta", "response.refusal.delta":
		outputIndex, _ := intValue(event["output_index"])
		slot := s.getSlot(outputIndex, "text")
		if slot == nil {
			return nil
		}
		delta, _ := stringValue(event["delta"])
		slot.text.Text += delta
		s.stream.Push(types.NewTextDeltaEvent(slot.contentIndex, delta, *s.output))
	case "response.function_call_arguments.delta":
		outputIndex, _ := intValue(event["output_index"])
		slot := s.getSlot(outputIndex, "toolCall")
		if slot == nil || slot.tool == nil || slot.tool.partialJSON == nil {
			return nil
		}
		delta, _ := stringValue(event["delta"])
		*slot.tool.partialJSON += delta
		s.writeToolArguments(slot, jsonRaw(utils.ParseStreamingJSON(*slot.tool.partialJSON)))
		s.pushToolCallDelta(slot, &delta)
	case "response.function_call_arguments.done":
		outputIndex, _ := intValue(event["output_index"])
		slot := s.getSlot(outputIndex, "toolCall")
		if slot == nil || slot.tool == nil || slot.tool.partialJSON == nil {
			return nil
		}
		previous := *slot.tool.partialJSON
		arguments, _ := stringValue(event["arguments"])
		*slot.tool.partialJSON = arguments
		s.writeToolArguments(slot, jsonRaw(utils.ParseStreamingJSON(arguments)))
		if strings.HasPrefix(arguments, previous) {
			delta := arguments[len(previous):]
			if len(delta) > 0 {
				s.pushToolCallDelta(slot, &delta)
			}
		}
	case "response.custom_tool_call_input.delta":
		outputIndex, _ := intValue(event["output_index"])
		slot := s.getSlot(outputIndex, "toolCall")
		if slot == nil || slot.tool == nil || slot.tool.customInput == nil {
			return nil
		}
		delta, _ := stringValue(event["delta"])
		next := getCustomToolCallInput(slot.tool) + delta
		s.appendCustomToolCallInput(slot, next, false)
	case "response.custom_tool_call_input.done":
		outputIndex, _ := intValue(event["output_index"])
		slot := s.getSlot(outputIndex, "toolCall")
		if slot == nil || slot.tool == nil || slot.tool.customInput == nil {
			return nil
		}
		input, _ := stringValue(event["input"])
		s.appendCustomToolCallInput(slot, input, true)
	case "response.output_item.done":
		outputIndex, _ := intValue(event["output_index"])
		item := jsonObject(event["item"])
		if item == nil {
			return nil
		}
		s.applyMessagePhaseStopReason(item)
		slot := s.getOrCreateSlot(outputIndex, item)
		s.finishOutputItem(outputIndex, item, slot)
	case "response.completed", "response.incomplete":
		if response := jsonObject(event["response"]); response != nil {
			return s.finalizeResponse(response)
		}
	case "error":
		code, _ := stringValue(event["code"])
		message, _ := stringValue(event["message"])
		return fmt.Errorf("Error Code %s: %s", code, message)
	case "response.failed":
		s.sawTerminalResponseEvent = true
		response := jsonObject(event["response"])
		if response != nil {
			if status, ok := stringValue(response["status"]); ok {
				s.output.RawStopReason = &status
			}
		}
		var errObj map[string]any
		var details map[string]any
		if response != nil {
			errObj = jsonObject(response["error"])
			details = jsonObject(response["incomplete_details"])
		}
		message := "Unknown error (no error details in response)"
		if errObj != nil {
			code, _ := stringValue(errObj["code"])
			if code == "" {
				code = "unknown"
			}
			detail, _ := stringValue(errObj["message"])
			if detail == "" {
				detail = "no message"
			}
			message = code + ": " + detail
		} else if details != nil {
			if reason, ok := stringValue(details["reason"]); ok {
				message = "incomplete: " + reason
			}
		}
		return fmt.Errorf("%s", message)
	}
	return nil
}

func getCustomToolCallInput(tool *streamingToolCall) string {
	if tool == nil || tool.tool == nil || tool.customInput == nil {
		return ""
	}
	arguments := toolCallArguments(tool.tool)
	value, _ := arguments[tool.customInput.property].(string)
	return value
}

func (s *responsesStreamState) appendCustomToolCallInput(slot *responsesOutputSlot, nextInput string, close bool) {
	tool := slot.tool
	if tool == nil || tool.customInput == nil {
		return
	}
	delta, err := AppendGrammarToolInputJSONDelta(&tool.customInput.jsonBuffer, tool.customInput.property, nextInput, close)
	if err != nil {
		return
	}
	s.writeToolArguments(slot, jsonRaw(map[string]any{tool.customInput.property: nextInput}))
	if delta != nil {
		s.pushToolCallDelta(slot, delta)
	}
}

func (s *responsesStreamState) finishOutputItem(outputIndex int, item map[string]any, slot *responsesOutputSlot) {
	itemType, _ := stringValue(item["type"])
	switch itemType {
	case "reasoning":
		if slot == nil || slot.kind != "thinking" {
			return
		}
		summaryText := joinContentText(item["summary"])
		contentText := joinContentText(item["content"])
		if summaryText != "" {
			slot.thinking.Thinking = summaryText
		} else if contentText != "" {
			slot.thinking.Thinking = contentText
		}
		signature := jsonRaw(item)
		signatureText := string(signature)
		slot.thinking.ThinkingSignature = &signatureText
		if id, ok := stringValue(item["id"]); ok {
			s.reasoningBlocksByID[id] = slot.thinking
		}
		s.stream.Push(types.NewThinkingEndEvent(slot.contentIndex, slot.thinking.Thinking, *s.output))
		delete(s.outputSlots, outputIndex)
	case "message":
		if slot == nil || slot.kind != "text" {
			return
		}
		slot.text.Text = joinMessageContent(item["content"])
		id, _ := stringValue(item["id"])
		phase := (*types.TextSignaturePhase)(nil)
		if phaseValue, ok := stringValue(item["phase"]); ok {
			typed := types.TextSignaturePhase(phaseValue)
			phase = &typed
		}
		signature := encodeTextSignatureV1(id, phase)
		slot.text.TextSignature = &signature
		s.stream.Push(types.NewTextEndEvent(slot.contentIndex, slot.text.Text, *s.output))
		delete(s.outputSlots, outputIndex)
	case "function_call":
		if slot == nil || slot.kind != "toolCall" || slot.tool == nil || slot.tool.partialJSON == nil {
			return
		}
		arguments, _ := stringValue(item["arguments"])
		if arguments == "" {
			arguments = *slot.tool.partialJSON
		}
		if arguments == "" {
			arguments = "{}"
		}
		s.writeToolArguments(slot, jsonRaw(utils.ParseStreamingJSON(arguments)))
		if namespace, ok := stringValue(item["namespace"]); ok {
			slot.tool.tool.Namespace = &namespace
		}
		slot.tool.partialJSON = nil
		s.stream.Push(types.NewToolCallEndEvent(slot.contentIndex, *slot.tool.tool, *s.output))
		delete(s.outputSlots, outputIndex)
	case "custom_tool_call":
		if slot == nil || slot.kind != "toolCall" || slot.tool == nil || slot.tool.customInput == nil {
			return
		}
		input, _ := stringValue(item["input"])
		if input == "" {
			input = getCustomToolCallInput(slot.tool)
		}
		s.appendCustomToolCallInput(slot, input, true)
		if namespace, ok := stringValue(item["namespace"]); ok {
			slot.tool.tool.Namespace = &namespace
		}
		slot.tool.customInput = nil
		s.stream.Push(types.NewToolCallEndEvent(slot.contentIndex, *slot.tool.tool, *s.output))
		delete(s.outputSlots, outputIndex)
	}
}

func joinContentText(value any) string {
	items, _ := value.([]any)
	parts := []string{}
	for _, entry := range items {
		if object := jsonObject(entry); object != nil {
			if text, ok := stringValue(object["text"]); ok {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func joinMessageContent(value any) string {
	items, _ := value.([]any)
	var builder strings.Builder
	for _, entry := range items {
		object := jsonObject(entry)
		if object == nil {
			continue
		}
		if itemType, _ := stringValue(object["type"]); itemType == "output_text" {
			if text, ok := stringValue(object["text"]); ok {
				builder.WriteString(text)
			}
		} else if refusal, ok := stringValue(object["refusal"]); ok {
			builder.WriteString(refusal)
		}
	}
	return builder.String()
}

func (s *responsesStreamState) backfillReasoningSignatures(responseOutput []any) {
	for _, entry := range responseOutput {
		item := jsonObject(entry)
		if item == nil {
			continue
		}
		if itemType, _ := stringValue(item["type"]); itemType != "reasoning" {
			continue
		}
		encrypted, _ := stringValue(item["encrypted_content"])
		if encrypted == "" {
			continue
		}
		id, _ := stringValue(item["id"])
		block := s.reasoningBlocksByID[id]
		if block == nil || block.ThinkingSignature == nil {
			continue
		}
		var storedItem map[string]any
		if err := json.Unmarshal([]byte(*block.ThinkingSignature), &storedItem); err != nil {
			continue
		}
		if existing, _ := stringValue(storedItem["encrypted_content"]); existing != "" {
			continue
		}
		storedItem["encrypted_content"] = encrypted
		encoded, _ := json.Marshal(storedItem)
		text := string(encoded)
		block.ThinkingSignature = &text
	}
}

func (s *responsesStreamState) finalizeResponse(response map[string]any) error {
	s.sawTerminalResponseEvent = true
	if output, ok := response["output"].([]any); ok {
		s.backfillReasoningSignatures(output)
	}
	if id, ok := stringValue(response["id"]); ok && id != "" {
		s.output.ResponseId = &id
	}
	if usage := jsonObject(response["usage"]); usage != nil {
		inputTokens, _ := floatValue(usage["input_tokens"])
		outputTokens, _ := floatValue(usage["output_tokens"])
		totalTokens, _ := floatValue(usage["total_tokens"])
		inputDetails := jsonObject(usage["input_tokens_details"])
		cachedTokens := 0.0
		cacheWriteTokens := 0.0
		if inputDetails != nil {
			cachedTokens, _ = floatValue(inputDetails["cached_tokens"])
			cacheWriteTokens, _ = floatValue(inputDetails["cache_write_tokens"])
		}
		outputDetails := jsonObject(usage["output_tokens_details"])
		reasoningTokens := 0.0
		if outputDetails != nil {
			reasoningTokens, _ = floatValue(outputDetails["reasoning_tokens"])
		}
		input := inputTokens - cachedTokens - cacheWriteTokens
		if input < 0 {
			input = 0
		}
		s.output.Usage = types.Usage{
			Input:       input,
			Output:      outputTokens,
			CacheRead:   cachedTokens,
			CacheWrite:  cacheWriteTokens,
			Reasoning:   &reasoningTokens,
			TotalTokens: totalTokens,
		}
	}
	calculateCost(s.model, &s.output.Usage)
	if s.options != nil && s.options.ApplyServiceTierPricing != nil {
		var serviceTier *string
		if s.options.ResolveServiceTier != nil {
			serviceTier = s.options.ResolveServiceTier(responseServiceTier(response), s.options.ServiceTier)
		} else {
			serviceTier = responseServiceTier(response)
			if serviceTier == nil {
				serviceTier = s.options.ServiceTier
			}
		}
		s.options.ApplyServiceTierPricing(&s.output.Usage, serviceTier)
	}
	status, _ := stringValue(response["status"])
	incompleteReason := ""
	if details := jsonObject(response["incomplete_details"]); details != nil {
		if reason, ok := stringValue(details["reason"]); ok {
			incompleteReason = reason
		}
	}
	if status != "" {
		raw := status
		if incompleteReason != "" {
			raw = status + "." + incompleteReason
		}
		s.output.RawStopReason = &raw
	}
	mappedStop, errorMessage, err := mapResponsesStopReason(status, incompleteReason)
	if err != nil {
		return err
	}
	s.output.StopReason = mappedStop
	if errorMessage == nil {
		s.output.ErrorMessage = nil
	} else {
		s.output.ErrorMessage = errorMessage
	}
	hasToolCall := false
	for _, block := range s.output.Content {
		if block.Type == types.ContentTypeToolCall {
			hasToolCall = true
			break
		}
	}
	if hasToolCall && s.output.StopReason == types.StopReasonStop {
		s.output.StopReason = types.StopReasonToolUse
	}
	return nil
}

func responseServiceTier(response map[string]any) *string {
	if value, ok := stringValue(response["service_tier"]); ok {
		return &value
	}
	return nil
}

func mapResponsesStopReason(status string, incompleteReason string) (types.StopReason, *string, error) {
	if status == "" {
		return types.StopReasonStop, nil, nil
	}
	switch status {
	case "completed":
		return types.StopReasonStop, nil, nil
	case "incomplete":
		if incompleteReason == "max_output_tokens" {
			return types.StopReasonLength, nil, nil
		}
		message := "Response incomplete without a provider reason"
		if incompleteReason != "" {
			message = "Response incomplete: " + incompleteReason
		}
		return types.StopReasonError, &message, nil
	case "failed", "cancelled":
		return types.StopReasonError, nil, nil
	case "in_progress", "queued":
		return types.StopReasonStop, nil, nil
	default:
		return "", nil, fmt.Errorf("Unhandled stop reason: %s", status)
	}
}

// ProcessResponsesStream consumes a Responses SSE event source and drives the
// assistant message stream. The caller owns the final done/error events.
func ProcessResponsesStream(next func() (map[string]any, bool, error), output *types.AssistantMessage, stream *types.AssistantMessageEventStream, model *types.Model, options *OpenAIResponsesStreamOptions) error {
	state := &responsesStreamState{
		output:              output,
		stream:              stream,
		model:               model,
		options:             options,
		outputSlots:         map[int]*responsesOutputSlot{},
		reasoningBlocksByID: map[string]*types.ThinkingContent{},
	}
	for {
		event, ok, err := next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if err := state.applyEvent(event); err != nil {
			return err
		}
	}
	if !state.sawTerminalResponseEvent {
		return fmt.Errorf("OpenAI Responses stream ended before a terminal response event")
	}
	return nil
}

// =============================================================================
// HTTP SSE helper
// =============================================================================

// newBufferedReader wraps a reader for byte-wise SSE reads.
func bufioNewReader(reader io.Reader) io.Reader {
	return reader
}

// responseHeaderTimeout returns a deadline context for the response headers.
func responseHeaderTimeout(ctx context.Context, timeoutMs *int) (context.Context, context.CancelFunc) {
	if timeoutMs == nil || *timeoutMs <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(*timeoutMs)*time.Millisecond)
}
