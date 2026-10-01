// This file is a Go port of packages/ai/src/api/pi-messages.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// pi-messages API implementation.
//
// Streams pi's own message protocol directly to a backend: the request is a
// single POST of `{ model, context, options }` to `<baseUrl>/messages`, the
// response is an SSE stream of serialized assistant-message events plus a
// terminal `done`/`error` event. This is the wire protocol spoken by the Radius
// gateway, but any backend implementing it can be used, e.g. via a models.json
// custom provider with `"api": "pi-messages"`.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// PiMessagesToolChoiceFunction is the named-function tool choice form.
type PiMessagesToolChoiceFunction struct {
	Name string `json:"name"`
}

// PiMessagesToolChoice is the provider-specific tool selection hint. Exactly one
// of Mode ("auto" | "none" | "required") or Function is set.
type PiMessagesToolChoice struct {
	Mode     *string                       `json:"-"`
	Function *PiMessagesToolChoiceFunction `json:"-"`
}

// PiMessagesOptions are the provider-specific options for the pi-messages API.
type PiMessagesOptions struct {
	types.StreamOptions
	Reasoning  *types.ThinkingLevel  `json:"-"`
	ToolChoice *PiMessagesToolChoice `json:"-"`
	// Debug asks the backend for debug metadata (e.g. routing response headers).
	Debug *bool `json:"-"`
}

// PiMessagesRewriteImpact is the impact summary of a server-side message rewrite
// (e.g. a gateway policy).
type PiMessagesRewriteImpact struct {
	PolicyID            string  `json:"policyId"`
	PolicyVersion       float64 `json:"policyVersion"`
	Changed             bool    `json:"changed"`
	TokenCountChange    float64 `json:"tokenCountChange"`
	MessageCountChange  float64 `json:"messageCountChange"`
	SystemPromptChanged bool    `json:"systemPromptChanged"`
}

// PiMessagesEvent is a serialized assistant-message event as sent by a
// pi-messages backend. Only the fields relevant to the event type are populated.
type PiMessagesEvent struct {
	Type                  string                   `json:"type"`
	ContentIndex          *int                     `json:"contentIndex,omitempty"`
	Delta                 *string                  `json:"delta,omitempty"`
	Content               *string                  `json:"content,omitempty"`
	ContentSignature      *string                  `json:"contentSignature,omitempty"`
	Redacted              *bool                    `json:"redacted,omitempty"`
	ID                    *string                  `json:"id,omitempty"`
	ToolName              *string                  `json:"toolName,omitempty"`
	ToolCall              *types.ToolCall          `json:"toolCall,omitempty"`
	Reason                *types.StopReason        `json:"reason,omitempty"`
	Usage                 *types.Usage             `json:"usage,omitempty"`
	ResponseID            *string                  `json:"responseId,omitempty"`
	ProviderThinkingLevel *string                  `json:"providerThinkingLevel,omitempty"`
	Rewrite               *PiMessagesRewriteImpact `json:"rewrite,omitempty"`
	ErrorMessage          *string                  `json:"errorMessage,omitempty"`
}

// PiMessagesResponseError reports a non-2xx pi-messages response with the HTTP
// fields needed for diagnostics.
type PiMessagesResponseError struct {
	message           string
	Code              *string
	DiagnosticDetails types.JsonObject
}

// NewPiMessagesResponseError builds a response error.
func NewPiMessagesResponseError(message string, code *string, details types.JsonObject) *PiMessagesResponseError {
	return &PiMessagesResponseError{message: message, Code: code, DiagnosticDetails: details}
}

// Error implements error.
func (e *PiMessagesResponseError) Error() string { return e.message }

// Name reports the error name for diagnostics.
func (e *PiMessagesResponseError) Name() string { return "PiMessagesResponseError" }

// piMessagesErrorBody is the parsed `{ error: {...} }` shape.
type piMessagesErrorBody struct {
	Error map[string]any `json:"error"`
}

func parsePiMessagesErrorBody(body string) *piMessagesErrorBody {
	var parsed struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil
	}
	if len(parsed.Error) == 0 {
		return nil
	}
	var errorObject map[string]any
	if err := json.Unmarshal(parsed.Error, &errorObject); err != nil {
		return nil
	}
	return &piMessagesErrorBody{Error: errorObject}
}

func truncatePiMessagesDiagnosticString(value string) string {
	const maxLength = 8192
	runes := []rune(value)
	if len(runes) > maxLength {
		return string(runes[:maxLength]) + "…"
	}
	return value
}

func formatPiMessagesResponseError(status int, statusText string, body string, errorBody *piMessagesErrorBody) string {
	message := ""
	code := ""
	if errorBody != nil {
		if text, ok := errorBody.Error["message"].(string); ok {
			message = text
		}
		if text, ok := errorBody.Error["code"].(string); ok {
			code = text
		}
	}
	suffix := body
	if message != "" {
		suffix = message
	}
	codeSuffix := ""
	if code != "" {
		codeSuffix = " (" + code + ")"
	}
	return fmt.Sprintf("%d %s: %s%s", status, statusText, suffix, codeSuffix)
}

func createPiMessagesResponseError(model *types.Model, requestURL string, status int, statusText string, body string) *PiMessagesResponseError {
	errorBody := parsePiMessagesErrorBody(body)
	code := ""
	if errorBody != nil {
		if text, ok := errorBody.Error["code"].(string); ok {
			code = text
		}
	}
	details := types.JsonObject{
		"version":     float64(1),
		"provider":    string(model.Provider),
		"model":       model.Id,
		"url":         requestURL,
		"status":      float64(status),
		"statusText":  statusText,
		"timestampMs": nowMillis(),
	}
	if errorBody != nil {
		details["error"] = errorBody.Error
	} else {
		details["body"] = truncatePiMessagesDiagnosticString(body)
	}
	var codePtr *string
	if code != "" {
		codePtr = &code
	}
	return NewPiMessagesResponseError(formatPiMessagesResponseError(status, statusText, body, errorBody), codePtr, details)
}

func createPiMessagesEmptyUsage() types.Usage {
	return types.Usage{Cost: types.UsageCost{}}
}

func appendPiMessagesRewriteDiagnostic(message *types.AssistantMessage, rewrite *PiMessagesRewriteImpact) {
	if rewrite == nil {
		return
	}
	message.Diagnostics = append(message.Diagnostics, types.AssistantMessageDiagnostic{
		Type:      "pi_messages_rewrite",
		Timestamp: nowMillis(),
		Details: map[string]any{
			"policyId":            rewrite.PolicyID,
			"policyVersion":       rewrite.PolicyVersion,
			"changed":             rewrite.Changed,
			"tokenCountChange":    rewrite.TokenCountChange,
			"messageCountChange":  rewrite.MessageCountChange,
			"systemPromptChanged": rewrite.SystemPromptChanged,
		},
	})
}

func appendPiMessagesResponseDiagnostic(message *types.AssistantMessage, err *PiMessagesResponseError) {
	errorInfo := utils.ExtractDiagnosticError(err)
	message.Diagnostics = append(message.Diagnostics, types.AssistantMessageDiagnostic{
		Type:      "pi_messages_response_failure",
		Timestamp: nowMillis(),
		Error:     errorInfo,
		Details:   err.DiagnosticDetails,
	})
}

func createPiMessagesEventConverter(model *types.Model) func(PiMessagesEvent) types.AssistantMessageEvent {
	partial := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
	partial.Content = []types.ContentBlock{}
	partial.Usage = createPiMessagesEmptyUsage()
	partial.StopReason = types.StopReasonPending
	toolJSON := map[int]string{}

	return func(event PiMessagesEvent) types.AssistantMessageEvent {
		switch event.Type {
		case "done":
			if event.Reason != nil {
				partial.StopReason = *event.Reason
			}
			if event.Usage != nil {
				partial.Usage = *event.Usage
			}
			if event.ResponseID != nil {
				partial.ResponseId = event.ResponseID
			}
			if event.ProviderThinkingLevel != nil {
				partial.ProviderThinkingLevel = event.ProviderThinkingLevel
			}
			appendPiMessagesRewriteDiagnostic(&partial, event.Rewrite)
			reason := partial.StopReason
			return types.NewDoneEvent(reason, partial)

		case "error":
			if event.Reason != nil {
				partial.StopReason = *event.Reason
			}
			if event.Usage != nil {
				partial.Usage = *event.Usage
			}
			partial.ErrorMessage = event.ErrorMessage
			if event.ResponseID != nil {
				partial.ResponseId = event.ResponseID
			}
			if event.ProviderThinkingLevel != nil {
				partial.ProviderThinkingLevel = event.ProviderThinkingLevel
			}
			appendPiMessagesRewriteDiagnostic(&partial, event.Rewrite)
			reason := partial.StopReason
			return types.NewErrorEvent(reason, partial)

		case "start":
			return types.NewStartEvent(partial)

		case "text_start":
			if event.ContentIndex != nil {
				partial.Content = setPiMessagesContentBlock(partial.Content, *event.ContentIndex, types.TextBlock(""))
			}
			return types.NewTextStartEvent(piMessagesIndex(event), partial)

		case "text_delta":
			if event.ContentIndex != nil && event.Delta != nil {
				index := *event.ContentIndex
				if index >= 0 && index < len(partial.Content) && partial.Content[index].Text != nil {
					partial.Content[index].Text.Text += *event.Delta
				}
			}
			return types.NewTextDeltaEvent(piMessagesIndex(event), piMessagesDelta(event), partial)

		case "text_end":
			if event.ContentIndex != nil {
				index := *event.ContentIndex
				if index >= 0 && index < len(partial.Content) && partial.Content[index].Text != nil {
					if event.Content != nil {
						partial.Content[index].Text.Text = *event.Content
					}
					partial.Content[index].Text.TextSignature = event.ContentSignature
				}
			}
			return types.NewTextEndEvent(piMessagesIndex(event), piMessagesContent(event), partial)

		case "thinking_start":
			if event.ContentIndex != nil {
				partial.Content = setPiMessagesContentBlock(partial.Content, *event.ContentIndex, types.ThinkingBlock(""))
			}
			return types.NewThinkingStartEvent(piMessagesIndex(event), partial)

		case "thinking_delta":
			if event.ContentIndex != nil && event.Delta != nil {
				index := *event.ContentIndex
				if index >= 0 && index < len(partial.Content) && partial.Content[index].Thinking != nil {
					partial.Content[index].Thinking.Thinking += *event.Delta
				}
			}
			return types.NewThinkingDeltaEvent(piMessagesIndex(event), piMessagesDelta(event), partial)

		case "thinking_end":
			if event.ContentIndex != nil {
				index := *event.ContentIndex
				if index >= 0 && index < len(partial.Content) && partial.Content[index].Thinking != nil {
					if event.Content != nil {
						partial.Content[index].Thinking.Thinking = *event.Content
					}
					partial.Content[index].Thinking.ThinkingSignature = event.ContentSignature
					partial.Content[index].Thinking.Redacted = event.Redacted
				}
			}
			return types.NewThinkingEndEvent(piMessagesIndex(event), piMessagesContent(event), partial)

		case "toolcall_start":
			if event.ContentIndex != nil {
				id := ""
				if event.ID != nil {
					id = *event.ID
				}
				name := ""
				if event.ToolName != nil {
					name = *event.ToolName
				}
				call := types.ToolCall{
					Type:      types.ContentTypeToolCall,
					Id:        id,
					Name:      name,
					Arguments: json.RawMessage("{}"),
				}
				partial.Content = setPiMessagesContentBlock(partial.Content, *event.ContentIndex, types.ToolCallBlock(call))
				toolJSON[*event.ContentIndex] = ""
			}
			return types.NewToolCallStartEvent(piMessagesIndex(event), partial)

		case "toolcall_delta":
			if event.ContentIndex != nil {
				index := *event.ContentIndex
				jsonText := toolJSON[index] + piMessagesDelta(event)
				toolJSON[index] = jsonText
				if index >= 0 && index < len(partial.Content) && partial.Content[index].ToolCall != nil {
					partial.Content[index].ToolCall.Arguments = rawFromAny(utils.ParseStreamingJSON(jsonText))
				}
			}
			return types.NewToolCallDeltaEvent(piMessagesIndex(event), piMessagesDelta(event), partial)

		case "toolcall_end":
			if event.ContentIndex != nil && event.ToolCall != nil {
				index := *event.ContentIndex
				if index >= 0 && index < len(partial.Content) && partial.Content[index].ToolCall != nil {
					call := *event.ToolCall
					if call.Type == "" {
						call.Type = types.ContentTypeToolCall
					}
					partial.Content[index].ToolCall = &call
				}
				delete(toolJSON, index)
				toolCall := types.ToolCall{}
				if index >= 0 && index < len(partial.Content) && partial.Content[index].ToolCall != nil {
					toolCall = *partial.Content[index].ToolCall
				}
				return types.NewToolCallEndEvent(index, toolCall, partial)
			}
			return types.AssistantMessageEvent{Type: types.AssistantEventToolCallEnd}
		}

		return types.AssistantMessageEvent{Type: types.AssistantMessageEventType(event.Type), Partial: &partial}
	}
}

func setPiMessagesContentBlock(content []types.ContentBlock, index int, block types.ContentBlock) []types.ContentBlock {
	for len(content) <= index {
		content = append(content, types.ContentBlock{})
	}
	content[index] = block
	return content
}

func piMessagesIndex(event PiMessagesEvent) int {
	if event.ContentIndex == nil {
		return 0
	}
	return *event.ContentIndex
}

func piMessagesDelta(event PiMessagesEvent) string {
	if event.Delta == nil {
		return ""
	}
	return *event.Delta
}

func piMessagesContent(event PiMessagesEvent) string {
	if event.Content == nil {
		return ""
	}
	return *event.Content
}

// readPiMessagesEvents reads pi-messages SSE frames. Frame boundaries are blank
// lines after normalizing CRLF; only the first data line of a frame is used.
// Each event is yielded both as its narrow typed form and as the raw parsed
// JSON object so an observer can retain provider fields the typed decoder
// discards.
func readPiMessagesEvents(ctx context.Context, body io.Reader, yield func(PiMessagesEvent, map[string]any) error) error {
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
		buffer = strings.ReplaceAll(buffer, "\r\n", "\n")
		for {
			split := strings.Index(buffer, "\n\n")
			if split == -1 {
				break
			}
			raw := buffer[:split]
			buffer = buffer[split+2:]
			event, rawEvent, ok, err := parsePiMessagesEvent(raw)
			if err != nil {
				return err
			}
			if ok {
				if yieldErr := yield(event, rawEvent); yieldErr != nil {
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
		event, rawEvent, ok, err := parsePiMessagesEvent(buffer)
		if err != nil {
			return err
		}
		if ok {
			return yield(event, rawEvent)
		}
	}
	return nil
}

func parsePiMessagesEvent(raw string) (PiMessagesEvent, map[string]any, bool, error) {
	data := ""
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "data:") {
			data = strings.TrimSpace(line[5:])
			break
		}
	}
	if data == "" || data == "[DONE]" {
		return PiMessagesEvent{}, nil, false, nil
	}
	// Retain the raw parsed object before the typed conversion so unknown
	// provider fields are observable even though PiMessagesEvent is narrow.
	var rawEvent map[string]any
	if err := json.Unmarshal([]byte(data), &rawEvent); err != nil {
		return PiMessagesEvent{}, nil, false, err
	}
	var event PiMessagesEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return PiMessagesEvent{}, nil, false, err
	}
	return event, rawEvent, true, nil
}

func createPiMessagesErrorEvent(model *types.Model, err error, wasAborted bool) types.AssistantMessageEvent {
	reason := types.StopReasonError
	if wasAborted {
		reason = types.StopReasonAborted
	}
	message := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
	message.Content = []types.ContentBlock{}
	message.Usage = createPiMessagesEmptyUsage()
	message.StopReason = reason
	text := "unknown error"
	if err != nil {
		text = err.Error()
	}
	message.ErrorMessage = &text

	if !wasAborted {
		var responseError *PiMessagesResponseError
		if asResponse, ok := err.(*PiMessagesResponseError); ok {
			responseError = asResponse
		}
		if responseError != nil {
			appendPiMessagesResponseDiagnostic(&message, responseError)
		}
	}
	return types.NewErrorEvent(reason, message)
}

func resolvePiCacheRetention(cacheRetention *types.CacheRetention, env types.ProviderEnv) *types.CacheRetention {
	if cacheRetention != nil {
		return cacheRetention
	}
	// Backend defaults apply when unset; only the legacy env opt-in is mapped.
	if value := utils.GetProviderEnvValue("PI_CACHE_RETENTION", env); value != nil && *value == "long" {
		long := types.CacheRetentionLong
		return &long
	}
	return nil
}

func piMessagesRequestContext(options *PiMessagesOptions) (context.Context, context.CancelFunc) {
	timeoutMs := 60000
	if options != nil && options.TimeoutMs != nil {
		timeoutMs = *options.TimeoutMs
	}
	var signal <-chan struct{}
	if options != nil {
		signal = options.Signal
	}
	ctxSignal, cancelSignal := contextForSignal(contextBackground(), signal)
	requestContext, cancelTimeout := context.WithTimeout(ctxSignal, time.Duration(timeoutMs)*time.Millisecond)
	return requestContext, func() {
		cancelTimeout()
		cancelSignal()
	}
}

func buildPiMessagesOptions(options *PiMessagesOptions) map[string]any {
	result := map[string]any{}
	if options == nil {
		return result
	}
	if options.Temperature != nil {
		result["temperature"] = *options.Temperature
	}
	if options.MaxTokens != nil {
		result["maxTokens"] = *options.MaxTokens
	}
	if options.Reasoning != nil {
		result["reasoning"] = string(*options.Reasoning)
	}
	if cacheRetention := resolvePiCacheRetention(options.CacheRetention, options.Env); cacheRetention != nil {
		result["cacheRetention"] = string(*cacheRetention)
	}
	if options.SessionId != nil {
		result["sessionId"] = *options.SessionId
	}
	if options.ToolChoice != nil {
		result["toolChoice"] = mapPiMessagesToolChoice(options.ToolChoice)
	}
	return result
}

func mapPiMessagesToolChoice(choice *PiMessagesToolChoice) any {
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

// PiMessagesStream streams a pi-messages request.
func PiMessagesStream(model *types.Model, context *types.TranscriptContext, options *PiMessagesOptions) *types.AssistantMessageEventStream {
	eventStream := types.NewAssistantMessageEventStream()
	convertEvent := createPiMessagesEventConverter(model)

	go func() {
		apiKey := (*string)(nil)
		if options != nil {
			apiKey = options.APIKey
		}
		if apiKey == nil || *apiKey == "" {
			eventStream.Push(createPiMessagesErrorEvent(model, fmt.Errorf("No API key provided for provider %q", model.Provider), false))
			return
		}

		requestURL, err := url.Parse(strings.TrimRight(model.BaseUrl, "/") + "/messages")
		if err != nil {
			eventStream.Push(createPiMessagesErrorEvent(model, err, false))
			return
		}
		if options != nil && options.Debug != nil && *options.Debug {
			query := requestURL.Query()
			query.Set("debug", "1")
			requestURL.RawQuery = query.Encode()
		}

		payload := map[string]any{
			"model":   model.Id,
			"context": map[string]any{"messages": context.Messages},
			"options": buildPiMessagesOptions(options),
		}
		if options != nil && options.OnPayload != nil {
			next, payloadErr := options.OnPayload(payload, model)
			if payloadErr != nil {
				eventStream.Push(createPiMessagesErrorEvent(model, payloadErr, false))
				return
			}
			if nextMap, ok := next.(map[string]any); ok {
				payload = nextMap
			}
		}

		headers := map[string]string{
			"authorization": "Bearer " + *apiKey,
			"accept":        "text/event-stream",
			"content-type":  "application/json",
		}
		if options != nil {
			for key, value := range utils.ProviderHeadersToRecord(options.Headers) {
				headers[key] = value
			}
		}

		body, err := json.Marshal(payload)
		if err != nil {
			eventStream.Push(createPiMessagesErrorEvent(model, err, false))
			return
		}

		requestContext, cancel := piMessagesRequestContext(options)
		defer cancel()

		var reqOptions *types.ProviderRequestOptions
		if options != nil {
			reqOptions = &options.ProviderRequestOptions
		}
		response, err := doProviderRequest(requestContext, reqOptions, http.MethodPost, requestURL.String(), headers, body)
		if err != nil {
			eventStream.Push(createPiMessagesErrorEvent(model, err, options != nil && options.Signal != nil && aborted(options.Signal)))
			return
		}
		defer response.Body.Close()

		if options != nil && options.OnResponse != nil {
			options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, model)
		}

		if response.StatusCode < 200 || response.StatusCode >= 300 {
			raw, readErr := io.ReadAll(response.Body)
			if readErr != nil {
				eventStream.Push(createPiMessagesErrorEvent(model, readErr, false))
				return
			}
			responseError := createPiMessagesResponseError(model, requestURL.String(), response.StatusCode, response.Status, string(raw))
			eventStream.Push(createPiMessagesErrorEvent(model, responseError, false))
			return
		}
		if response.Body == nil {
			eventStream.Push(createPiMessagesErrorEvent(model, fmt.Errorf("%s response has no body", model.Provider), false))
			return
		}

		var terminal bool
		readErr := readPiMessagesEvents(requestContext, response.Body, func(piEvent PiMessagesEvent, rawEvent map[string]any) error {
			if options != nil && options.OnProviderStreamEvent != nil {
				if observeErr := options.OnProviderStreamEvent(rawEvent, model); observeErr != nil {
					return observeErr
				}
			}
			event := convertEvent(piEvent)
			eventStream.Push(event)
			if event.Type == types.AssistantEventDone || event.Type == types.AssistantEventError {
				terminal = true
				return errPiMessagesTerminal
			}
			return nil
		})
		if readErr != nil && readErr != errPiMessagesTerminal {
			eventStream.Push(createPiMessagesErrorEvent(model, readErr, options != nil && options.Signal != nil && aborted(options.Signal)))
			return
		}
		if terminal {
			return
		}
		eventStream.Push(createPiMessagesErrorEvent(model, fmt.Errorf("%s stream ended without a terminal event", model.Provider), false))
	}()

	return eventStream
}

// errPiMessagesTerminal stops the event reader once a terminal event is
// consumed; it never escapes the stream goroutine.
var errPiMessagesTerminal = fmt.Errorf("pi-messages terminal event")

// PiMessagesStreamSimple is the unified-reasoning entry point for the
// pi-messages API.
func PiMessagesStreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	typed := &PiMessagesOptions{}
	if options != nil {
		typed.StreamOptions = options.StreamOptions
		typed.Reasoning = options.Reasoning
		typed.ToolChoice = mapSimpleToPiMessagesToolChoice(options.ToolChoice)
	} else {
		return PiMessagesStream(model, context, nil)
	}
	return PiMessagesStream(model, context, typed)
}

func mapSimpleToPiMessagesToolChoice(choice *types.ToolChoice) *PiMessagesToolChoice {
	if choice == nil {
		return nil
	}
	mode := string(*choice)
	return &PiMessagesToolChoice{Mode: &mode}
}
