// This file is a Go port of packages/ai/src/api/mistral-conversations.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Streaming implementation for the native Mistral Chat Completions endpoint.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

const mistralToolCallIDLength = 9
const maxMistralErrorBodyChars = 4000

// MistralReasoningEffort is the provider-native reasoning effort value.
type MistralReasoningEffort string

// Mistral reasoning efforts.
const (
	MistralReasoningEffortNone MistralReasoningEffort = "none"
	MistralReasoningEffortHigh MistralReasoningEffort = "high"
)

// MistralToolChoiceFunction is the named-function tool choice form.
type MistralToolChoiceFunction struct {
	Name string `json:"name"`
}

// MistralToolChoice is the provider-specific tool selection hint.
//
// Exactly one of Mode ("auto" | "none" | "any" | "required") or Function is set.
type MistralToolChoice struct {
	Mode     *string                    `json:"-"`
	Function *MistralToolChoiceFunction `json:"-"`
}

// MistralOptions are the provider-specific options for the Mistral API.
type MistralOptions struct {
	types.StreamOptions
	ToolChoice      *MistralToolChoice      `json:"-"`
	PromptMode      *string                 `json:"-"`
	ReasoningEffort *MistralReasoningEffort `json:"-"`
}

// MistralConversationsStream streams responses from the native Mistral Chat
// Completions endpoint.
func MistralConversationsStream(model *types.Model, context *types.TranscriptContext, options *MistralOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()

	supportsMidConvo := false
	if model.Compat.MistralConversations != nil && model.Compat.MistralConversations.SupportsMidConvoSystemMessages != nil {
		supportsMidConvo = *model.Compat.MistralConversations.SupportsMidConvoSystemMessages
	}
	normalizedContext := utils.ResolveTranscript(*context, &supportsMidConvo)

	go func() {
		output := createMistralOutput(model)

		apiKey := (*string)(nil)
		if options != nil {
			apiKey = options.APIKey
		}
		if apiKey == nil || *apiKey == "" {
			mistralTerminate(stream, output, fmt.Errorf("No API key for provider: %s", model.Provider), false)
			return
		}

		normalizeToolCallID := createMistralToolCallIDNormalizer()
		transformedMessages := TransformMessages(normalizedContext.Messages, model, func(id string, _ *types.Model, _ types.AssistantMessage) string {
			return normalizeToolCallID(id)
		})

		payload, err := buildMistralChatPayload(model, normalizedContext, transformedMessages, options)
		if err != nil {
			mistralTerminate(stream, output, err, false)
			return
		}
		if options != nil && options.OnPayload != nil {
			next, payloadErr := options.OnPayload(payload, model)
			if payloadErr != nil {
				mistralTerminate(stream, output, payloadErr, false)
				return
			}
			if nextMap, ok := next.(map[string]any); ok {
				payload = nextMap
			}
		}

		requestContext, cancelRequest := mistralRequestContext(options)
		defer cancelRequest()

		response, err := requestMistralStream(requestContext, model, payload, *apiKey, options)
		if err != nil {
			mistralTerminate(stream, output, err, aborted(mistralSignal(options)))
			return
		}
		defer response.Body.Close()

		stream.Push(types.NewStartEvent(*output))
		consumeErr := consumeMistralChatStream(requestContext, model, output, stream, response.Body, mistralOptionObserver(options))
		if consumeErr != nil {
			mistralTerminate(stream, output, consumeErr, aborted(mistralSignal(options)))
			return
		}

		if aborted(mistralSignal(options)) {
			mistralTerminate(stream, output, fmt.Errorf("Request was aborted"), true)
			return
		}
		if output.StopReason == types.StopReasonPending {
			mistralTerminate(stream, output, fmt.Errorf("Mistral stream ended without a finish reason"), false)
			return
		}
		if output.StopReason == types.StopReasonAborted || output.StopReason == types.StopReasonError {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			mistralTerminate(stream, output, fmt.Errorf("%s", message), false)
			return
		}

		stream.Push(types.NewDoneEvent(output.StopReason, *output))
		stream.End(output)
	}()

	return stream
}

// MistralConversationsStreamSimple maps provider-agnostic SimpleStreamOptions to
// Mistral options.
func MistralConversationsStreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	apiKey := (*string)(nil)
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == nil || *apiKey == "" {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := createMistralOutput(model)
			mistralTerminate(stream, output, fmt.Errorf("No API key for provider: %s", model.Provider), false)
		}()
		return stream
	}

	base := BuildBaseOptions(model, context, options, apiKey)
	typed := &MistralOptions{StreamOptions: base}
	if options != nil {
		typed.ToolChoice = mapSimpleToolChoice(options.ToolChoice)
	}

	var reasoning *types.ModelThinkingLevel
	if options != nil && options.Reasoning != nil {
		clamped := clampThinkingLevel(model, *options.Reasoning)
		if clamped != types.ThinkingOff {
			reasoning = &clamped
		}
	}
	// Models with a thinking level map use `reasoning_effort`; other reasoning
	// models use `prompt_mode`. The presence of the map, not the model name,
	// selects the mechanism.
	hasEffortMap := model.Reasoning && model.ThinkingLevelMap != nil
	if hasEffortMap {
		if reasoning != nil {
			effort := mistralMappedReasoningEffort(model, *reasoning)
			typed.ReasoningEffort = &effort
		} else if off, ok := model.ThinkingLevelMap[types.ThinkingOff]; ok && off != nil {
			effort := MistralReasoningEffort(*off)
			typed.ReasoningEffort = &effort
		}
	}
	if model.Reasoning && !hasEffortMap && reasoning != nil {
		promptMode := "reasoning"
		typed.PromptMode = &promptMode
	}

	return MistralConversationsStream(model, context, typed)
}

func createMistralOutput(model *types.Model) *types.AssistantMessage {
	output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
	output.Content = []types.ContentBlock{}
	output.Usage = types.Usage{
		Cost: types.UsageCost{},
	}
	return &output
}

func mistralSignal(options *MistralOptions) <-chan struct{} {
	if options == nil {
		return nil
	}
	return options.Signal
}

// mistralOptionObserver returns the optional provider-stream-event observer.
func mistralOptionObserver(options *MistralOptions) func(data any, model *types.Model) error {
	if options == nil {
		return nil
	}
	return options.OnProviderStreamEvent
}

// mistralTerminate strips the streaming scratch buffer from the partial output
// and pushes a terminal error event.
func mistralTerminate(stream *types.AssistantMessageEventStream, output *types.AssistantMessage, err error, wasAborted bool) {
	output.StopReason = types.StopReasonError
	if wasAborted {
		output.StopReason = types.StopReasonAborted
	}
	message := formatMistralError(err)
	output.ErrorMessage = &message
	stream.Push(types.NewErrorEvent(output.StopReason, *output))
	stream.End(output)
}

func createMistralToolCallIDNormalizer() func(string) string {
	idMap := map[string]string{}
	reverseMap := map[string]string{}
	return func(id string) string {
		if existing, ok := idMap[id]; ok {
			return existing
		}
		attempt := 0
		for {
			candidate := deriveMistralToolCallID(id, attempt)
			if owner, ok := reverseMap[candidate]; !ok || owner == id {
				idMap[id] = candidate
				reverseMap[candidate] = id
				return candidate
			}
			attempt++
		}
	}
}

func deriveMistralToolCallID(id string, attempt int) string {
	normalized := stripMistralToolCallIDChars(id)
	if attempt == 0 && len(normalized) == mistralToolCallIDLength {
		return normalized
	}
	seedBase := normalized
	if seedBase == "" {
		seedBase = id
	}
	seed := seedBase
	if attempt != 0 {
		seed = fmt.Sprintf("%s:%d", seedBase, attempt)
	}
	hashed := stripMistralToolCallIDChars(utils.ShortHash(seed))
	if len(hashed) > mistralToolCallIDLength {
		hashed = hashed[:mistralToolCallIDLength]
	}
	return hashed
}

var mistralToolCallIDUnsafe = regexp.MustCompile(`[^a-zA-Z0-9]`)

func stripMistralToolCallIDChars(value string) string {
	return mistralToolCallIDUnsafe.ReplaceAllString(value, "")
}

func formatMistralError(err error) string {
	if err == nil {
		return "null"
	}
	var httpErr *mistralHTTPError
	if asHTTP, ok := err.(*mistralHTTPError); ok {
		httpErr = asHTTP
	}
	if httpErr != nil {
		body := strings.TrimSpace(httpErr.body)
		if body != "" {
			return fmt.Sprintf("Mistral API error (%d): %s", httpErr.statusCode, utils.TruncateErrorText(body, maxMistralErrorBodyChars))
		}
		return fmt.Sprintf("Mistral API error (%d): %s", httpErr.statusCode, httpErr.Error())
	}
	if errText, ok := err.(interface{ Error() string }); ok {
		return errText.Error()
	}
	return utils.SafeJSONStringify(err)
}

type mistralHTTPError struct {
	statusCode int
	body       string
	statusText string
}

func (e *mistralHTTPError) Error() string {
	if e.statusText != "" {
		return e.statusText
	}
	return fmt.Sprintf("Request failed with status %d", e.statusCode)
}

// ProviderErrorFields exposes the SDK-shaped fields probed by
// utils.NormalizeProviderError.
func (e *mistralHTTPError) ProviderErrorFields() map[string]any {
	return map[string]any{"statusCode": e.statusCode, "body": e.body}
}

// mistralRequestContext couples the explicit abort signal with the request
// timeout used for both the request and the streaming body read. The caller owns
// the returned cancel func.
func mistralRequestContext(options *MistralOptions) (context.Context, context.CancelFunc) {
	timeoutMs := 60000
	if options != nil && options.TimeoutMs != nil {
		timeoutMs = *options.TimeoutMs
	}
	ctxSignal, cancelSignal := contextForSignal(contextBackground(), mistralSignal(options))
	requestContext, cancelTimeout := context.WithTimeout(ctxSignal, time.Duration(timeoutMs)*time.Millisecond)
	return requestContext, func() {
		cancelTimeout()
		cancelSignal()
	}
}

func requestMistralStream(requestContext context.Context, model *types.Model, payload map[string]any, apiKey string, options *MistralOptions) (*http.Response, error) {
	baseURL, err := resolveMistralURL(model.BaseUrl)
	if err != nil {
		return nil, err
	}
	headers := buildMistralHeaders(model, apiKey, options)

	body, err := json.Marshal(toMistralWirePayload(payload))
	if err != nil {
		return nil, err
	}

	var reqOptions *types.ProviderRequestOptions
	if options != nil {
		reqOptions = &options.ProviderRequestOptions
	}
	response, err := doProviderRequest(requestContext, reqOptions, http.MethodPost, baseURL, headers, body)
	if err != nil {
		return nil, err
	}

	if options != nil && options.OnResponse != nil {
		options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, model)
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		return nil, &mistralHTTPError{statusCode: response.StatusCode, body: string(raw), statusText: response.Status}
	}
	if response.Body == nil {
		return nil, fmt.Errorf("Mistral response has no body")
	}
	return response, nil
}

func resolveMistralURL(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/"
	ref, err := url.Parse("v1/chat/completions")
	if err != nil {
		return "", err
	}
	return parsed.ResolveReference(ref).String(), nil
}

func buildMistralHeaders(model *types.Model, apiKey string, options *MistralOptions) map[string]string {
	headers := map[string]string{
		"User-Agent":    utils.GetPiUserAgent(),
		"accept":        "text/event-stream",
		"authorization": "Bearer " + apiKey,
		"content-type":  "application/json",
	}
	applyMistralHeaderOverrides(headers, stringHeaderOverrides(model.Headers))
	if options != nil {
		applyMistralHeaderOverrides(headers, options.Headers)
	}

	hasExplicitAffinity := hasMistralHeaderOverride(model.Headers, "x-affinity") || (options != nil && hasMistralProviderHeaderOverride(options.Headers, "x-affinity"))
	if shouldUseMistralPromptCaching(options) && !hasExplicitAffinity {
		headers[http.CanonicalHeaderKey("x-affinity")] = *options.SessionId
	}
	return headers
}

func stringHeaderOverrides(headers map[string]string) map[string]*string {
	if headers == nil {
		return nil
	}
	out := map[string]*string{}
	for key, value := range headers {
		v := value
		out[key] = &v
	}
	return out
}

func applyMistralHeaderOverrides(headers map[string]string, overrides map[string]*string) {
	for name, value := range overrides {
		key := http.CanonicalHeaderKey(name)
		if value == nil {
			delete(headers, key)
			continue
		}
		headers[key] = *value
	}
}

func hasMistralHeaderOverride(overrides map[string]string, target string) bool {
	for name := range overrides {
		if strings.EqualFold(name, target) {
			return true
		}
	}
	return false
}

func hasMistralProviderHeaderOverride(overrides types.ProviderHeaders, target string) bool {
	for name := range overrides {
		if strings.EqualFold(name, target) {
			return true
		}
	}
	return false
}

var mistralPropertyRemaps = [][2]string{
	{"topP", "top_p"},
	{"maxTokens", "max_tokens"},
	{"randomSeed", "random_seed"},
	{"responseFormat", "response_format"},
	{"toolChoice", "tool_choice"},
	{"presencePenalty", "presence_penalty"},
	{"frequencyPenalty", "frequency_penalty"},
	{"parallelToolCalls", "parallel_tool_calls"},
	{"reasoningEffort", "reasoning_effort"},
	{"promptMode", "prompt_mode"},
	{"promptCacheKey", "prompt_cache_key"},
	{"safePrompt", "safe_prompt"},
}

func toMistralWirePayload(payload map[string]any) map[string]any {
	wirePayload := map[string]any{}
	for key, value := range payload {
		wirePayload[key] = value
	}
	for _, remap := range mistralPropertyRemaps {
		remapMistralProperty(wirePayload, remap[0], remap[1])
	}

	if messages, ok := payload["messages"].([]map[string]any); ok {
		wireMessages := make([]map[string]any, 0, len(messages))
		for _, message := range messages {
			wireMessages = append(wireMessages, toMistralWireMessage(message))
		}
		wirePayload["messages"] = wireMessages
	}

	if responseFormat, ok := wirePayload["response_format"].(map[string]any); ok {
		wireResponseFormat := map[string]any{}
		for key, value := range responseFormat {
			wireResponseFormat[key] = value
		}
		remapMistralProperty(wireResponseFormat, "jsonSchema", "json_schema")
		if jsonSchema, ok := wireResponseFormat["json_schema"].(map[string]any); ok {
			wireJSONSchema := map[string]any{}
			for key, value := range jsonSchema {
				wireJSONSchema[key] = value
			}
			remapMistralProperty(wireJSONSchema, "schemaDefinition", "schema")
			wireResponseFormat["json_schema"] = wireJSONSchema
		}
		wirePayload["response_format"] = wireResponseFormat
	}

	return wirePayload
}

var mistralContentChunkRemaps = [][2]string{
	{"imageUrl", "image_url"},
	{"documentUrl", "document_url"},
	{"documentName", "document_name"},
	{"fileId", "file_id"},
	{"referenceIds", "reference_ids"},
	{"inputAudio", "input_audio"},
}

func toMistralWireMessage(message map[string]any) map[string]any {
	wireMessage := map[string]any{}
	for key, value := range message {
		wireMessage[key] = value
	}
	remapMistralProperty(wireMessage, "toolCalls", "tool_calls")
	remapMistralProperty(wireMessage, "toolCallId", "tool_call_id")
	if content, ok := message["content"].([]map[string]any); ok {
		wireContent := make([]map[string]any, 0, len(content))
		for _, chunk := range content {
			wireContent = append(wireContent, toMistralWireContentChunk(chunk))
		}
		wireMessage["content"] = wireContent
	} else if content, ok := message["content"].([]any); ok {
		wireContent := make([]any, 0, len(content))
		for _, chunk := range content {
			if chunkMap, ok := chunk.(map[string]any); ok {
				wireContent = append(wireContent, toMistralWireContentChunk(chunkMap))
			} else {
				wireContent = append(wireContent, chunk)
			}
		}
		wireMessage["content"] = wireContent
	}
	return wireMessage
}

func toMistralWireContentChunk(chunk map[string]any) map[string]any {
	wireChunk := map[string]any{}
	for key, value := range chunk {
		wireChunk[key] = value
	}
	for _, remap := range mistralContentChunkRemaps {
		remapMistralProperty(wireChunk, remap[0], remap[1])
	}
	return wireChunk
}

func remapMistralProperty(record map[string]any, source, target string) {
	value, ok := record[source]
	if !ok {
		return
	}
	record[target] = value
	delete(record, source)
}

// findMistralEventBoundary locates the next SSE frame boundary, accepting the
// full set of CR/LF combinations the upstream reader accepts.
var mistralEventBoundary = regexp.MustCompile(`\r\n\r\n|\r\n\r|\r\n\n|\r\r\n|\n\r\n|\r\r|\n\r|\n\n`)

func findMistralEventBoundary(buffer string) (index, length int, ok bool) {
	match := mistralEventBoundary.FindStringIndex(buffer)
	if match == nil {
		return 0, 0, false
	}
	return match[0], match[1] - match[0], true
}

var mistralEventLineSplit = regexp.MustCompile(`\r\n|\r|\n`)

// parseMistralEvent parses one SSE frame. It reports done for the [DONE]
// sentinel.
func parseMistralEvent(raw string) (done bool, event map[string]any, err error) {
	dataLines := []string{}
	for _, line := range mistralEventLineSplit.Split(raw, -1) {
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimLeft(line[5:], " \t"))
		}
	}
	data := strings.TrimSpace(strings.Join(dataLines, "\n"))
	if data == "" {
		return false, nil, nil
	}
	if data == "[DONE]" {
		return true, nil, nil
	}
	var parsed map[string]any
	if err := decodeJSONValueStrict(data, &parsed); err != nil {
		return false, nil, fmt.Errorf("Invalid Mistral streaming event")
	}
	if _, ok := parsed["choices"].([]any); !ok {
		return false, nil, fmt.Errorf("Invalid Mistral streaming event")
	}
	return false, parsed, nil
}

func decodeJSONValueStrict(data string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}

// readMistralEvents reads an SSE body incrementally and yields each decoded
// completion chunk until [DONE] or EOF.
func readMistralEvents(ctx context.Context, body io.Reader, yield func(map[string]any) error) error {
	buffer := ""
	chunk := make([]byte, 8192)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := body.Read(chunk)
		if n > 0 {
			buffer += string(chunk[:n])
		}
		for {
			index, length, ok := findMistralEventBoundary(buffer)
			if !ok {
				break
			}
			raw := buffer[:index]
			buffer = buffer[index+length:]
			done, event, err := parseMistralEvent(raw)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			if event != nil {
				if yieldErr := yield(event); yieldErr != nil {
					return yieldErr
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if strings.TrimSpace(buffer) != "" {
		done, event, err := parseMistralEvent(buffer)
		if err != nil {
			return err
		}
		if !done && event != nil {
			return yield(event)
		}
	}
	return nil
}

func consumeMistralChatStream(ctx context.Context, model *types.Model, output *types.AssistantMessage, stream *types.AssistantMessageEventStream, body io.Reader, observer func(data any, model *types.Model) error) error {
	currentKind := ""
	currentIndex := -1
	toolBlocksByKey := map[string]int{}
	partialArgs := map[int]string{}

	finishCurrent := func() {
		if currentKind == "" || currentIndex < 0 {
			return
		}
		switch currentKind {
		case string(types.ContentTypeText):
			block := output.Content[currentIndex]
			text := ""
			if block.Text != nil {
				text = block.Text.Text
			}
			stream.Push(types.NewTextEndEvent(currentIndex, text, *output))
		case string(types.ContentTypeThinking):
			block := output.Content[currentIndex]
			thinking := ""
			if block.Thinking != nil {
				thinking = block.Thinking.Thinking
			}
			stream.Push(types.NewThinkingEndEvent(currentIndex, thinking, *output))
		}
	}

	err := readMistralEvents(ctx, body, func(completion map[string]any) error {
		if observer != nil {
			// Observe the raw parsed chunk before normalization.
			if observeErr := observer(completion, model); observeErr != nil {
				return observeErr
			}
		}
		if id, ok := stringValue(completion["id"]); ok && id != "" {
			if output.ResponseId == nil || *output.ResponseId == "" {
				output.ResponseId = &id
			}
		}

		if rawUsage, ok := completion["usage"].(map[string]any); ok {
			promptTokens := numberOrZero(rawUsage["prompt_tokens"])
			cachedPromptTokens := getMistralCachedPromptTokens(rawUsage, promptTokens)
			output.Usage.Input = maxFloat(0, promptTokens-cachedPromptTokens)
			output.Usage.Output = numberOrZero(rawUsage["completion_tokens"])
			output.Usage.CacheRead = cachedPromptTokens
			output.Usage.CacheWrite = 0
			totalTokens := numberOrZero(rawUsage["total_tokens"])
			if totalTokens == 0 {
				totalTokens = output.Usage.Input + output.Usage.Output + output.Usage.CacheRead + output.Usage.CacheWrite
			}
			output.Usage.TotalTokens = totalTokens
			calculateCost(model, &output.Usage)
		}

		choices, _ := completion["choices"].([]any)
		if len(choices) == 0 {
			return nil
		}
		choice, _ := choices[0].(map[string]any)
		if choice == nil {
			return nil
		}

		if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
			raw := finishReason
			output.RawStopReason = &raw
			stopReason, errorMessage := mapMistralChatStopReason(&raw)
			output.StopReason = stopReason
			if errorMessage != nil {
				output.ErrorMessage = errorMessage
			}
		}

		delta, _ := choice["delta"].(map[string]any)
		if delta != nil {
			if contentValue, present := delta["content"]; present && contentValue != nil {
				items := []any{}
				if text, ok := contentValue.(string); ok {
					items = append(items, text)
				} else if list, ok := contentValue.([]any); ok {
					items = list
				}
				for _, item := range items {
					if text, ok := item.(string); ok {
						textDelta := utils.SanitizeSurrogates(text)
						// Empty content deltas are sent around thinking and tool calls
						// by some Mistral-hosted models. Opening a block for them splits
						// a following thinking block, which the provider rejects on replay.
						if textDelta == "" {
							continue
						}
						if currentKind != string(types.ContentTypeText) {
							finishCurrent()
							block := types.TextBlock("")
							output.Content = append(output.Content, block)
							currentKind = string(types.ContentTypeText)
							currentIndex = len(output.Content) - 1
							stream.Push(types.NewTextStartEvent(currentIndex, *output))
						}
						if output.Content[currentIndex].Text != nil {
							output.Content[currentIndex].Text.Text += textDelta
						}
						stream.Push(types.NewTextDeltaEvent(currentIndex, textDelta, *output))
						continue
					}
					chunk, ok := item.(map[string]any)
					if !ok {
						continue
					}
					switch chunk["type"] {
					case "thinking":
						deltaText := mistralThinkingDeltaText(chunk)
						thinkingDelta := utils.SanitizeSurrogates(deltaText)
						if thinkingDelta == "" {
							continue
						}
						if currentKind != string(types.ContentTypeThinking) {
							finishCurrent()
							block := types.ThinkingBlock("")
							output.Content = append(output.Content, block)
							currentKind = string(types.ContentTypeThinking)
							currentIndex = len(output.Content) - 1
							stream.Push(types.NewThinkingStartEvent(currentIndex, *output))
						}
						if output.Content[currentIndex].Thinking != nil {
							output.Content[currentIndex].Thinking.Thinking += thinkingDelta
						}
						stream.Push(types.NewThinkingDeltaEvent(currentIndex, thinkingDelta, *output))
					case "text":
						text, _ := chunk["text"].(string)
						textDelta := utils.SanitizeSurrogates(text)
						if textDelta == "" {
							continue
						}
						if currentKind != string(types.ContentTypeText) {
							finishCurrent()
							block := types.TextBlock("")
							output.Content = append(output.Content, block)
							currentKind = string(types.ContentTypeText)
							currentIndex = len(output.Content) - 1
							stream.Push(types.NewTextStartEvent(currentIndex, *output))
						}
						if output.Content[currentIndex].Text != nil {
							output.Content[currentIndex].Text.Text += textDelta
						}
						stream.Push(types.NewTextDeltaEvent(currentIndex, textDelta, *output))
					}
				}
			}
		}

		toolCalls, _ := delta["tool_calls"].([]any)
		for _, toolCallValue := range toolCalls {
			toolCall, ok := toolCallValue.(map[string]any)
			if !ok {
				continue
			}
			if currentKind != "" {
				finishCurrent()
				currentKind = ""
				currentIndex = -1
			}
			callID, _ := toolCall["id"].(string)
			if callID == "" || callID == "null" {
				index := int(numberOrZero(toolCall["index"]))
				callID = deriveMistralToolCallID(fmt.Sprintf("toolcall:%d", index), 0)
			}
			key := callID
			if _, hasIndex := toolCall["index"]; hasIndex {
				key = fmt.Sprintf("%d", int(numberOrZero(toolCall["index"])))
			}
			existingIndex, hasExisting := toolBlocksByKey[key]
			blockIndex := -1
			if hasExisting && existingIndex < len(output.Content) && output.Content[existingIndex].Type == types.ContentTypeToolCall {
				blockIndex = existingIndex
			}
			if blockIndex < 0 {
				block := types.ToolCallBlock(types.ToolCall{
					Type:      types.ContentTypeToolCall,
					Id:        callID,
					Name:      mapStringValue(toolCall, "function", "name"),
					Arguments: json.RawMessage("{}"),
				})
				output.Content = append(output.Content, block)
				blockIndex = len(output.Content) - 1
				toolBlocksByKey[key] = blockIndex
				partialArgs[blockIndex] = ""
				stream.Push(types.NewToolCallStartEvent(blockIndex, *output))
			}

			argsDelta := ""
			if function, ok := toolCall["function"].(map[string]any); ok {
				switch arguments := function["arguments"].(type) {
				case string:
					argsDelta = arguments
				case map[string]any:
					encoded, _ := json.Marshal(arguments)
					argsDelta = string(encoded)
				default:
					if function["arguments"] != nil {
						encoded, _ := json.Marshal(function["arguments"])
						argsDelta = string(encoded)
					}
				}
			}
			partialArgs[blockIndex] += argsDelta
			if output.Content[blockIndex].ToolCall != nil {
				output.Content[blockIndex].ToolCall.Arguments = rawFromAny(utils.ParseStreamingJSON(partialArgs[blockIndex]))
			}
			stream.Push(types.NewToolCallDeltaEvent(blockIndex, argsDelta, *output))
		}
		return nil
	})
	if err != nil {
		return err
	}

	finishCurrent()
	for _, index := range toolBlocksByKey {
		if index < 0 || index >= len(output.Content) || output.Content[index].Type != types.ContentTypeToolCall {
			continue
		}
		toolBlock := output.Content[index].ToolCall
		if toolBlock == nil {
			continue
		}
		toolBlock.Arguments = rawFromAny(utils.ParseStreamingJSON(partialArgs[index]))
		stream.Push(types.NewToolCallEndEvent(index, *toolBlock, *output))
	}
	return nil
}

func mistralThinkingDeltaText(chunk map[string]any) string {
	parts, _ := chunk["thinking"].([]any)
	var builder strings.Builder
	for _, part := range parts {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		text, _ := partMap["text"].(string)
		builder.WriteString(text)
	}
	return builder.String()
}

func mapStringValue(record map[string]any, key, nested string) string {
	inner, ok := record[key].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := inner[nested].(string)
	return value
}

func rawFromAny(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

func numberOrZero(value any) float64 {
	if number, ok := floatValue(value); ok {
		return number
	}
	return 0
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func getMistralCachedPromptTokens(usage map[string]any, promptTokens float64) float64 {
	rawCachedTokens, ok := firstNumericField(usage,
		[2]string{"promptTokensDetails", "cachedTokens"},
		[2]string{"prompt_tokens_details", "cached_tokens"},
		[2]string{"promptTokenDetails", "cachedTokens"},
		[2]string{"prompt_token_details", "cached_tokens"},
		[2]string{"", "numCachedTokens"},
		[2]string{"", "num_cached_tokens"},
	)
	if !ok {
		rawCachedTokens = 0
	}
	return minFloat(promptTokens, maxFloat(0, rawCachedTokens))
}

func firstNumericField(record map[string]any, paths ...[2]string) (float64, bool) {
	for _, path := range paths {
		var value any = record
		if path[0] != "" {
			nested, ok := record[path[0]].(map[string]any)
			if !ok {
				continue
			}
			value = nested
		}
		nested, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if number, ok := floatValue(nested[path[1]]); ok {
			return number, true
		}
	}
	return 0, false
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func buildMistralChatPayload(model *types.Model, context types.TranscriptContext, messages []types.Message, options *MistralOptions) (map[string]any, error) {
	payload := map[string]any{
		"model":    model.Id,
		"stream":   true,
		"messages": toMistralChatMessages(messages, modelSupportsImages(model)),
	}

	currentTools := utils.GetCurrentTools(utils.TranscriptMessages(context.Messages))
	if len(currentTools) > 0 {
		tools, err := toMistralFunctionTools(currentTools)
		if err != nil {
			return nil, err
		}
		payload["tools"] = tools
	}
	if options != nil {
		if options.Temperature != nil {
			payload["temperature"] = *options.Temperature
		}
		if options.MaxTokens != nil {
			payload["maxTokens"] = *options.MaxTokens
		}
		if options.ToolChoice != nil {
			payload["toolChoice"] = mapMistralToolChoice(options.ToolChoice)
		}
		if options.PromptMode != nil {
			payload["promptMode"] = *options.PromptMode
		}
		if options.ReasoningEffort != nil {
			payload["reasoningEffort"] = string(*options.ReasoningEffort)
		}
		if shouldUseMistralPromptCaching(options) && options.SessionId != nil {
			payload["promptCacheKey"] = *options.SessionId
		}
	}
	return payload, nil
}

func shouldUseMistralPromptCaching(options *MistralOptions) bool {
	if options == nil {
		return false
	}
	if options.CacheRetention != nil && *options.CacheRetention == types.CacheRetentionNone {
		return false
	}
	return options.SessionId != nil && *options.SessionId != ""
}

func toMistralFunctionTools(tools []types.Tool) ([]map[string]any, error) {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		strict, err := ResolveJSONSchemaStrictSampling(tool, true)
		if err != nil {
			return nil, err
		}
		schema, err := GetJSONSchemaToolParameters(tool, strict)
		if err != nil {
			return nil, err
		}
		var schemaValue any
		if err := decodeJSONValueStrict(string(schema), &schemaValue); err != nil {
			return nil, err
		}
		strictValue := false
		if strict != nil {
			strictValue = *strict
		}
		result = append(result, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  stripMistralSymbolKeys(schemaValue),
				"strict":      strictValue,
			},
		})
	}
	return result, nil
}

// stripMistralSymbolKeys recursively rebuilds JSON values. Go JSON values
// cannot carry symbol keys, so this is an identity for decoded JSON; it is kept
// so the conversion step matches the upstream contract explicitly.
func stripMistralSymbolKeys(value any) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, stripMistralSymbolKeys(item))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for key, entry := range typed {
			out[key] = stripMistralSymbolKeys(entry)
		}
		return out
	default:
		return value
	}
}

func toMistralChatMessages(messages []types.Message, supportsImages bool) []map[string]any {
	result := []map[string]any{}
	for index, message := range messages {
		switch message.Role {
		case types.SystemMessageRole:
			if message.System == nil {
				continue
			}
			var text string
			if index == 0 {
				text = utils.GetSystemMessageText(*message.System)
			} else {
				text = utils.RenderSystemMessageUpdate(*message.System)
			}
			if len(text) > 0 {
				result = append(result, map[string]any{"role": "system", "content": utils.SanitizeSurrogates(text)})
			}

		case types.UserMessageRole:
			if message.User == nil {
				continue
			}
			if !message.User.Content.Structured {
				result = append(result, map[string]any{"role": "user", "content": utils.SanitizeSurrogates(message.User.Content.Text)})
				continue
			}
			hadImages := false
			content := []map[string]any{}
			for _, item := range message.User.Content.Blocks {
				if item.Type == types.ContentTypeImage {
					hadImages = true
				}
				if item.Type == types.ContentTypeText && item.Text != nil {
					content = append(content, map[string]any{"type": "text", "text": utils.SanitizeSurrogates(item.Text.Text)})
				} else if supportsImages && item.Type == types.ContentTypeImage && item.Image != nil {
					content = append(content, map[string]any{"type": "image_url", "imageUrl": "data:" + item.Image.MimeType + ";base64," + item.Image.Data})
				}
			}
			if len(content) > 0 {
				result = append(result, map[string]any{"role": "user", "content": content})
				continue
			}
			if hadImages && !supportsImages {
				result = append(result, map[string]any{"role": "user", "content": "(image omitted: model does not support images)"})
			}

		case types.AssistantMessageRole:
			if message.Assistant == nil {
				continue
			}
			contentParts := []map[string]any{}
			toolCalls := []map[string]any{}
			for _, block := range message.Assistant.Content {
				switch block.Type {
				case types.ContentTypeText:
					if block.Text != nil && strings.TrimSpace(block.Text.Text) != "" {
						contentParts = append(contentParts, map[string]any{"type": "text", "text": utils.SanitizeSurrogates(block.Text.Text)})
					}
				case types.ContentTypeThinking:
					if block.Thinking != nil && strings.TrimSpace(block.Thinking.Thinking) != "" {
						contentParts = append(contentParts, map[string]any{
							"type":     "thinking",
							"thinking": []map[string]any{{"type": "text", "text": utils.SanitizeSurrogates(block.Thinking.Thinking)}},
						})
					}
				case types.ContentTypeToolCall:
					if block.ToolCall == nil {
						continue
					}
					arguments := "{}"
					if len(block.ToolCall.Arguments) > 0 {
						arguments = string(block.ToolCall.Arguments)
					}
					toolCalls = append(toolCalls, map[string]any{
						"id":   block.ToolCall.Id,
						"type": "function",
						"function": map[string]any{
							"name":      block.ToolCall.Name,
							"arguments": arguments,
						},
						"index": 0,
					})
				}
			}
			if len(contentParts) == 0 && len(toolCalls) == 0 {
				continue
			}
			assistantMessage := map[string]any{"role": "assistant", "prefix": false}
			if len(contentParts) > 0 {
				assistantMessage["content"] = contentParts
			}
			if len(toolCalls) > 0 {
				assistantMessage["toolCalls"] = toolCalls
			}
			result = append(result, assistantMessage)

		case types.ToolResultMessageRole:
			if message.ToolResult == nil {
				continue
			}
			toolContent := []map[string]any{}
			textParts := []string{}
			hasImages := false
			for _, part := range message.ToolResult.Content {
				if part.Type == types.ContentTypeText && part.Text != nil {
					textParts = append(textParts, utils.SanitizeSurrogates(part.Text.Text))
				}
				if part.Type == types.ContentTypeImage {
					hasImages = true
				}
			}
			textResult := strings.Join(textParts, "\n")
			toolText := buildMistralToolResultText(textResult, hasImages, supportsImages, message.ToolResult.IsError)
			toolContent = append(toolContent, map[string]any{"type": "text", "text": toolText})
			if supportsImages {
				for _, part := range message.ToolResult.Content {
					if part.Type != types.ContentTypeImage || part.Image == nil {
						continue
					}
					toolContent = append(toolContent, map[string]any{"type": "image_url", "imageUrl": "data:" + part.Image.MimeType + ";base64," + part.Image.Data})
				}
			}
			result = append(result, map[string]any{
				"role":       "tool",
				"toolCallId": message.ToolResult.ToolCallId,
				"name":       message.ToolResult.ToolName,
				"content":    toolContent,
			})
		}
	}
	return result
}

func buildMistralToolResultText(text string, hasImages, supportsImages, isError bool) string {
	trimmed := strings.TrimSpace(text)
	errorPrefix := ""
	if isError {
		errorPrefix = "[tool error] "
	}
	if len(trimmed) > 0 {
		imageSuffix := ""
		if hasImages && !supportsImages {
			imageSuffix = "\n[tool image omitted: model does not support images]"
		}
		return errorPrefix + trimmed + imageSuffix
	}
	if hasImages {
		if supportsImages {
			if isError {
				return "[tool error] (see attached image)"
			}
			return "(see attached image)"
		}
		if isError {
			return "[tool error] (image omitted: model does not support images)"
		}
		return "(image omitted: model does not support images)"
	}
	if isError {
		return "[tool error] (no tool output)"
	}
	return "(no tool output)"
}

// mistralMappedReasoningEffort mirrors `effortMap[reasoning] ?? "high"`: a
// missing or explicitly unsupported (nil) entry falls back to "high".
func mistralMappedReasoningEffort(model *types.Model, level types.ModelThinkingLevel) MistralReasoningEffort {
	if model.ThinkingLevelMap != nil {
		if value, ok := model.ThinkingLevelMap[level]; ok && value != nil {
			return MistralReasoningEffort(*value)
		}
	}
	return MistralReasoningEffortHigh
}

func mapMistralToolChoice(choice *MistralToolChoice) any {
	if choice == nil {
		return nil
	}
	if choice.Function != nil {
		return map[string]any{"type": "function", "function": map[string]any{"name": choice.Function.Name}}
	}
	if choice.Mode != nil {
		return *choice.Mode
	}
	return nil
}

func mapSimpleToolChoice(choice *types.ToolChoice) *MistralToolChoice {
	if choice == nil {
		return nil
	}
	mode := string(*choice)
	return &MistralToolChoice{Mode: &mode}
}

func mapMistralChatStopReason(reason *string) (types.StopReason, *string) {
	if reason == nil {
		return types.StopReasonStop, nil
	}
	switch *reason {
	case "stop":
		return types.StopReasonStop, nil
	case "length", "model_length":
		return types.StopReasonLength, nil
	case "tool_calls":
		return types.StopReasonToolUse, nil
	case "error":
		message := "Provider stopped with: error"
		return types.StopReasonError, &message
	default:
		message := "Provider stopped with: " + *reason
		return types.StopReasonError, &message
	}
}
