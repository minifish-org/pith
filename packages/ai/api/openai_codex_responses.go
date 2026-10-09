// This file is a Go port of packages/ai/src/api/openai-codex-responses.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Deliberate differences from the pinned TypeScript:
//   - The Codex backend accepts zstd-compressed request bodies. Go's standard
//     library has no zstd encoder, so the SSE request body is sent
//     uncompressed. The wire-visible regression is a larger request body only;
//     the response processing is unchanged.
//   - registerSessionResourceCleanup lives in the root ai package, which the
//     api package must not import. Codex WebSocket connections are therefore
//     closed explicitly through CloseOpenAICodexWebSocketSessions.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

const (
	defaultCodexBaseURL              = "https://chatgpt.com/backend-api"
	jwtClaimPath                     = "https://api.openai.com/auth"
	defaultCodexMaxRetries           = 0
	codexBaseDelayMs                 = 1000
	defaultCodexMaxRetryDelayMs      = 60000
	defaultWebSocketConnectTimeoutMs = 15000
	// A completed response can carry large encrypted reasoning or tool arguments.
	// Use the agent proxy's 16 MiB event-line budget while keeping message reads finite.
	codexWebSocketReadLimit             = 16 << 20
	sessionWebSocketCacheTTLMs          = 5 * 60 * 1000
	sessionWebSocketMaxAgeMs            = 55 * 60 * 1000
	openAIBetaResponsesWebSockets       = "responses_websockets=2026-02-06"
	webSocketMessageTooBigCloseCode     = 1009
	webSocketConnectionLimitReachedCode = "websocket_connection_limit_reached"
	previousResponseNotFoundCode        = "previous_response_not_found"
)

// codexToolCallProviders are the providers whose Responses item ids Codex keeps.
var codexToolCallProviders = map[string]bool{
	string(types.ProviderOpenAI):      true,
	string(types.ProviderOpenAICodex): true,
	string(types.ProviderOpencode):    true,
}

// OpenAICodexResponsesOptions are the Codex Responses stream options.
type OpenAICodexResponsesOptions struct {
	types.StreamOptions
	ReasoningEffort     *string
	ReasoningSummary    *string
	HasReasoningSummary bool
	ServiceTier         *string
	TextVerbosity       *string
	ToolChoice          *string
}

// OpenAICodexWebSocketDebugStats is the per-session WebSocket debug snapshot.
type OpenAICodexWebSocketDebugStats struct {
	Requests                int     `json:"requests"`
	ConnectionsCreated      int     `json:"connectionsCreated"`
	ConnectionsReused       int     `json:"connectionsReused"`
	CachedContextRequests   int     `json:"cachedContextRequests"`
	StoreTrueRequests       int     `json:"storeTrueRequests"`
	FullContextRequests     int     `json:"fullContextRequests"`
	DeltaRequests           int     `json:"deltaRequests"`
	LastInputItems          int     `json:"lastInputItems"`
	LastDeltaInputItems     *int    `json:"lastDeltaInputItems,omitempty"`
	LastPreviousResponseID  *string `json:"lastPreviousResponseId,omitempty"`
	WebSocketFailures       int     `json:"websocketFailures"`
	SSEFallbacks            int     `json:"sseFallbacks"`
	WebSocketFallbackActive *bool   `json:"websocketFallbackActive,omitempty"`
	LastWebSocketError      *string `json:"lastWebSocketError,omitempty"`
}

type codexContinuationState struct {
	lastRequestBody   map[string]any
	lastResponseID    string
	lastResponseItems []map[string]any
}

type cachedCodexWebSocket struct {
	conn         *websocket.Conn
	busy         bool
	createdAt    time.Time
	idleTimer    *time.Timer
	continuation *codexContinuationState
}

var (
	codexWebSocketMu       sync.Mutex
	codexWebSocketSessions = map[string]map[string]*cachedCodexWebSocket{}
	codexDebugStats        = map[string]*OpenAICodexWebSocketDebugStats{}
	codexSSEFallback       = map[string]bool{}
)

func codexGetOrCreateStats(sessionID string) *OpenAICodexWebSocketDebugStats {
	stats, ok := codexDebugStats[sessionID]
	if !ok {
		stats = &OpenAICodexWebSocketDebugStats{}
		codexDebugStats[sessionID] = stats
	}
	return stats
}

// GetOpenAICodexWebSocketDebugStats returns a copy of the WebSocket debug stats
// for a session, or nil when none were recorded.
func GetOpenAICodexWebSocketDebugStats(sessionID string) *OpenAICodexWebSocketDebugStats {
	codexWebSocketMu.Lock()
	defer codexWebSocketMu.Unlock()
	stats, ok := codexDebugStats[sessionID]
	if !ok {
		return nil
	}
	copyStats := *stats
	return &copyStats
}

// ResetOpenAICodexWebSocketDebugStats clears the debug stats for one session, or
// for every session when sessionID is nil.
func ResetOpenAICodexWebSocketDebugStats(sessionID *string) {
	codexWebSocketMu.Lock()
	defer codexWebSocketMu.Unlock()
	if sessionID != nil {
		delete(codexDebugStats, *sessionID)
		delete(codexSSEFallback, *sessionID)
		return
	}
	codexDebugStats = map[string]*OpenAICodexWebSocketDebugStats{}
	codexSSEFallback = map[string]bool{}
}

// CloseOpenAICodexWebSocketSessions closes and forgets cached WebSocket
// connections for one session, or for every session when sessionID is nil.
func CloseOpenAICodexWebSocketSessions(sessionID *string) {
	codexWebSocketMu.Lock()
	defer codexWebSocketMu.Unlock()
	closeEntry := func(entry *cachedCodexWebSocket) {
		if entry.idleTimer != nil {
			entry.idleTimer.Stop()
		}
		closeWebSocketSilently(entry.conn)
	}
	if sessionID != nil {
		for _, entry := range codexWebSocketSessions[*sessionID] {
			closeEntry(entry)
		}
		delete(codexWebSocketSessions, *sessionID)
		return
	}
	for _, entries := range codexWebSocketSessions {
		for _, entry := range entries {
			closeEntry(entry)
		}
	}
	codexWebSocketSessions = map[string]map[string]*cachedCodexWebSocket{}
}

func codexIsSSEFallbackActive(sessionID *string) bool {
	if sessionID == nil {
		return false
	}
	codexWebSocketMu.Lock()
	defer codexWebSocketMu.Unlock()
	return codexSSEFallback[*sessionID]
}

func codexRecordSSEFallback(sessionID *string) {
	if sessionID == nil {
		return
	}
	codexWebSocketMu.Lock()
	defer codexWebSocketMu.Unlock()
	stats := codexGetOrCreateStats(*sessionID)
	stats.SSEFallbacks++
	active := codexSSEFallback[*sessionID]
	stats.WebSocketFallbackActive = &active
}

func codexRecordWebSocketFailure(sessionID *string, err error) {
	if sessionID == nil {
		return
	}
	codexWebSocketMu.Lock()
	defer codexWebSocketMu.Unlock()
	codexSSEFallback[*sessionID] = true
	stats := codexGetOrCreateStats(*sessionID)
	stats.WebSocketFailures++
	text := err.Error()
	stats.LastWebSocketError = &text
	active := true
	stats.WebSocketFallbackActive = &active
}

// OpenAICodexResponsesStream is the streaming entry point for the OpenAI Codex
// Responses API.
func OpenAICodexResponsesStream(model *types.Model, context *types.TranscriptContext, options *OpenAICodexResponsesOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	supportsMidConvo := false
	if raw := responsesCompat(model); raw != nil && raw.SupportsMidConvoSystemMessages != nil {
		supportsMidConvo = *raw.SupportsMidConvoSystemMessages
	}
	normalizedContext := resolveTranscriptContext(context, supportsMidConvo)

	go func() {
		output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
		output.StopReason = types.StopReasonPending

		apiKey := (*string)(nil)
		if options != nil {
			apiKey = options.APIKey
		}
		if apiKey == nil || *apiKey == "" {
			terminateCodexStream(stream, &output, model, fmt.Errorf("No API key for provider: %s", model.Provider), false)
			return
		}
		accountID, err := extractCodexAccountID(*apiKey)
		if err != nil {
			terminateCodexStream(stream, &output, model, err, false)
			return
		}

		grammarProperties := createCodexGrammarProperties(normalizedContext, model)
		cacheSessionID := (*string)(nil)
		if options == nil || options.CacheRetention == nil || *options.CacheRetention != types.CacheRetentionNone {
			if options != nil {
				cacheSessionID = options.SessionId
			}
		}
		codexSessionID := ClampOpenAIPromptCacheKey(cacheSessionID)
		body := buildCodexRequestBody(model, normalizedContext, options, codexSessionID, grammarProperties)

		var reqOptions *types.ProviderRequestOptions
		if options != nil {
			reqOptions = &options.ProviderRequestOptions
			if options.OnPayload != nil {
				if next, payloadErr := options.OnPayload(body, model); payloadErr != nil {
					terminateCodexStream(stream, &output, model, payloadErr, false)
					return
				} else if nextMap, ok := next.(map[string]any); ok {
					body = nextMap
				}
			}
		}

		bodyJSON, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			terminateCodexStream(stream, &output, model, marshalErr, false)
			return
		}

		startEmitted := false
		onStart := func() {
			if !startEmitted {
				startEmitted = true
				stream.Push(types.NewStartEvent(output))
			}
		}

		transport := "auto"
		if options != nil && options.Transport != nil {
			transport = string(*options.Transport)
		}
		websocketDisabled := transport != "sse" && codexIsSSEFallbackActive(codexSessionID)
		if websocketDisabled {
			codexRecordSSEFallback(codexSessionID)
		}

		if transport != "sse" && !websocketDisabled {
			wsErr := processCodexWebSocket(resolveCodexWebSocketURL(model.BaseUrl), body, model, &output, stream, onStart, apiKey, accountID, codexSessionID, grammarProperties, options)
			if wsErr == nil {
				if aborted(codexOptionSignal(options)) {
					terminateCodexStream(stream, &output, model, fmt.Errorf("Request was aborted"), true)
					return
				}
				if output.StopReason == types.StopReasonPending {
					terminateCodexStream(stream, &output, model, fmt.Errorf("Codex stream ended without a stop reason"), false)
					return
				}
				if output.StopReason == types.StopReasonAborted || output.StopReason == types.StopReasonError {
					message := "An unknown error occurred"
					if output.ErrorMessage != nil {
						message = *output.ErrorMessage
					}
					terminateCodexStream(stream, &output, model, fmt.Errorf("%s", message), output.StopReason == types.StopReasonAborted)
					return
				}
				stream.Push(types.NewDoneEvent(output.StopReason, output))
				stream.End(&output)
				return
			}
			// Before the message stream starts, fall back to SSE, except for a
			// non-transport observer failure.
			if startEmitted || isProviderStreamEventCallbackError(wsErr) {
				terminateCodexStream(stream, &output, model, wsErr, aborted(codexOptionSignal(options)))
				return
			}
			codexRecordWebSocketFailure(codexSessionID, wsErr)
			codexRecordSSEFallback(codexSessionID)
		}

		headers := buildCodexSSEHeaders(model, reqOptions, accountID, *apiKey, codexSessionID)
		requestContext, cancel := contextForSignal(contextBackground(), codexOptionSignal(options))
		defer cancel()

		response, _, requestErr := performRequestWithRetry(requestContext, reqOptions, http.MethodPost, resolveCodexURL(model.BaseUrl), headers, bodyJSON)
		if requestErr != nil {
			terminateCodexStream(stream, &output, model, requestErr, aborted(codexOptionSignal(options)))
			return
		}
		defer response.Body.Close()

		if options != nil && options.OnResponse != nil {
			options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, model)
		}

		onStart()

		eventQueue := make(chan map[string]any, 16)
		errQueue := make(chan error, 1)
		go func() {
			defer close(eventQueue)
			errQueue <- readSSE(requestContext, response.Body, codexOptionSignal(options), func(event map[string]any) error {
				eventQueue <- event
				return nil
			})
		}()

		next := codexNextFunc(func() (map[string]any, bool, error) {
			event, ok := <-eventQueue
			if !ok {
				select {
				case readErr := <-errQueue:
					if readErr != nil {
						return nil, false, readErr
					}
				default:
				}
				return nil, false, nil
			}
			return event, true, nil
		}, &output, model, codexOptionObserver(options))

		if err := ProcessResponsesStream(next, &output, stream, model, &OpenAIResponsesStreamOptions{
			ServiceTier:                codexOptionServiceTier(options),
			GrammarToolInputProperties: grammarProperties,
			ResolveServiceTier:         resolveCodexServiceTier,
			ApplyServiceTierPricing: func(usage *types.Usage, serviceTier *string) {
				applyCodexServiceTierPricing(usage, serviceTier, model)
			},
		}); err != nil {
			terminateCodexStream(stream, &output, model, err, aborted(codexOptionSignal(options)))
			return
		}

		if aborted(codexOptionSignal(options)) {
			terminateCodexStream(stream, &output, model, fmt.Errorf("Request was aborted"), true)
			return
		}
		if output.StopReason == types.StopReasonPending {
			terminateCodexStream(stream, &output, model, fmt.Errorf("Codex stream ended without a stop reason"), false)
			return
		}
		if output.StopReason == types.StopReasonAborted || output.StopReason == types.StopReasonError {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			terminateCodexStream(stream, &output, model, fmt.Errorf("%s", message), output.StopReason == types.StopReasonAborted)
			return
		}

		stream.Push(types.NewDoneEvent(output.StopReason, output))
		stream.End(&output)
	}()

	return stream
}

// OpenAICodexResponsesStreamSimple is the unified-reasoning entry point for the
// Codex Responses API.
func OpenAICodexResponsesStreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	apiKey := (*string)(nil)
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == nil || *apiKey == "" {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
			terminateCodexStream(stream, &output, model, fmt.Errorf("No API key for provider: %s", model.Provider), false)
		}()
		return stream
	}
	base := BuildBaseOptions(model, context, options, apiKey)
	typed := &OpenAICodexResponsesOptions{StreamOptions: base}
	if options != nil {
		if options.ToolChoice != nil {
			value := string(*options.ToolChoice)
			typed.ToolChoice = &value
		}
		if options.Reasoning != nil {
			clamped := clampThinkingLevel(model, *options.Reasoning)
			if clamped != types.ThinkingOff {
				effort := string(clamped)
				typed.ReasoningEffort = &effort
			}
		}
	}
	return OpenAICodexResponsesStream(model, context, typed)
}

func buildCodexRequestBody(model *types.Model, context *types.TranscriptContext, options *OpenAICodexResponsesOptions, cacheSessionID *string, grammarProperties map[string]string) map[string]any {
	supportsStrict := true
	supportsGrammar := false
	supportsAdditionalTools := false
	supportsToolSearch := false
	if raw := responsesCompat(model); raw != nil {
		if raw.SupportsStrictMode != nil {
			supportsStrict = *raw.SupportsStrictMode
		}
		if raw.SupportsOpenAIGrammarTools != nil {
			supportsGrammar = *raw.SupportsOpenAIGrammarTools
		}
		if raw.SupportsAdditionalTools != nil {
			supportsAdditionalTools = *raw.SupportsAdditionalTools
		}
		if raw.SupportsToolSearch != nil {
			supportsToolSearch = *raw.SupportsToolSearch
		}
	}
	transcriptTools := utils.ResolveTranscriptTools(context.Messages, supportsAdditionalTools || supportsToolSearch)
	includeSystemPrompt := false
	toolOptions := &ConvertResponsesToolsOptions{StrictNull: true, SupportsStrictMode: &supportsStrict, SupportsOpenAIGrammarTools: &supportsGrammar}
	messages := ConvertResponsesMessages(model, context, codexToolCallProviders, &ConvertResponsesMessagesOptions{
		IncludeSystemPrompt:            &includeSystemPrompt,
		GrammarToolInputProperties:     grammarProperties,
		SupportsMidConvoSystemMessages: codexSupportsMidConvo(model),
		SupportsAdditionalTools:        supportsAdditionalTools,
		SupportsToolSearch:             supportsToolSearch,
		ToolOptions:                    toolOptions,
	})

	instructions := ""
	if initial := utils.GetInitialSystemMessage(context.Messages); initial != nil {
		instructions = utils.GetSystemMessageText(*initial)
	}
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}

	body := map[string]any{
		"model":               model.Id,
		"store":               false,
		"stream":              true,
		"instructions":        instructions,
		"input":               messages,
		"text":                map[string]any{"verbosity": codexTextVerbosity(options)},
		"include":             []string{"reasoning.encrypted_content"},
		"tool_choice":         codexToolChoice(options),
		"parallel_tool_calls": true,
	}
	if cacheSessionID != nil {
		body["prompt_cache_key"] = *cacheSessionID
	}
	if options != nil && options.Temperature != nil {
		body["temperature"] = *options.Temperature
	}
	if options != nil && options.ServiceTier != nil {
		body["service_tier"] = *options.ServiceTier
	}
	if len(transcriptTools.RequestTools) > 0 {
		body["tools"] = ConvertResponsesTools(transcriptTools.RequestTools, toolOptions)
	}
	if options != nil && options.ReasoningEffort != nil {
		effort := *options.ReasoningEffort
		if effort == "none" {
			if _, present, supported := model.ThinkingLevelMap.Lookup(types.ThinkingOff); present && supported {
				if mapped, _, _ := model.ThinkingLevelMap.Lookup(types.ThinkingOff); mapped != "" {
					effort = mapped
				}
			}
		} else {
			effort = resolveThinkingLevelValue(model, effort)
		}
		summary := "auto"
		if options.HasReasoningSummary && options.ReasoningSummary != nil {
			summary = *options.ReasoningSummary
		}
		body["reasoning"] = map[string]any{"effort": effort, "summary": summary}
	} else if model.Reasoning {
		_, present, supported := model.ThinkingLevelMap.Lookup(types.ThinkingOff)
		if !present || supported {
			offValue := "none"
			if present && supported {
				if mapped, _, _ := model.ThinkingLevelMap.Lookup(types.ThinkingOff); mapped != "" {
					offValue = mapped
				}
			}
			body["reasoning"] = map[string]any{"effort": offValue}
		}
	}
	return body
}

func codexTextVerbosity(options *OpenAICodexResponsesOptions) string {
	if options != nil && options.TextVerbosity != nil && *options.TextVerbosity != "" {
		return *options.TextVerbosity
	}
	return "low"
}

func codexToolChoice(options *OpenAICodexResponsesOptions) string {
	if options != nil && options.ToolChoice != nil && *options.ToolChoice != "" {
		return *options.ToolChoice
	}
	return "auto"
}

func codexSupportsMidConvo(model *types.Model) bool {
	if raw := responsesCompat(model); raw != nil && raw.SupportsMidConvoSystemMessages != nil {
		return *raw.SupportsMidConvoSystemMessages
	}
	return false
}

func createCodexGrammarProperties(context *types.TranscriptContext, model *types.Model) map[string]string {
	supportsGrammar := false
	if raw := responsesCompat(model); raw != nil && raw.SupportsOpenAIGrammarTools != nil {
		supportsGrammar = *raw.SupportsOpenAIGrammarTools
	}
	return createGrammarToolInputPropertiesForContext(context, supportsGrammar)
}

func resolveCodexURL(baseURL string) string {
	raw := baseURL
	if strings.TrimSpace(raw) == "" {
		raw = defaultCodexBaseURL
	}
	normalized := strings.TrimRight(raw, "/")
	if strings.HasSuffix(normalized, "/codex/responses") {
		return normalized
	}
	if strings.HasSuffix(normalized, "/codex") {
		return normalized + "/responses"
	}
	return normalized + "/codex/responses"
}

func resolveCodexWebSocketURL(baseURL string) string {
	url := resolveCodexURL(baseURL)
	url = strings.Replace(url, "https://", "wss://", 1)
	url = strings.Replace(url, "http://", "ws://", 1)
	return url
}

// codexNextFunc normalizes Codex protocol events before they reach the shared
// Responses processor.
// providerStreamEventCallbackError wraps an OnProviderStreamEvent failure so
// Codex never treats it as a transient transport error that could trigger a
// WebSocket retry or SSE fallback. The original error text is preserved through
// Error/Unwrap.
type providerStreamEventCallbackError struct {
	cause error
}

func (e *providerStreamEventCallbackError) Error() string {
	if e.cause == nil {
		return "provider stream event callback failed"
	}
	return e.cause.Error()
}

func (e *providerStreamEventCallbackError) Unwrap() error { return e.cause }

// isProviderStreamEventCallbackError reports whether err (or a wrapped error)
// is an observer callback failure.
func isProviderStreamEventCallbackError(err error) bool {
	var target *providerStreamEventCallbackError
	return errors.As(err, &target)
}

func codexNextFunc(read func() (map[string]any, bool, error), output *types.AssistantMessage, model *types.Model, observer func(data any, model *types.Model) error) func() (map[string]any, bool, error) {
	stopped := false
	return func() (map[string]any, bool, error) {
		if stopped {
			return nil, false, nil
		}
		for {
			event, ok, err := read()
			if err != nil {
				return nil, false, err
			}
			if !ok {
				return nil, false, nil
			}
			if observer != nil {
				// Observe the raw Codex event before mapping. Wrap the error so it
				// stays out of Codex's WebSocket retry and SSE fallback path.
				if observeErr := observer(event, model); observeErr != nil {
					return nil, false, &providerStreamEventCallbackError{cause: observeErr}
				}
			}
			eventType, _ := stringValue(event["type"])
			switch eventType {
			case "error":
				code, _ := stringValue(event["code"])
				message, _ := stringValue(event["message"])
				if message == "" {
					message = code
				}
				if message == "" {
					encoded, _ := json.Marshal(event)
					message = string(encoded)
				}
				return nil, false, fmt.Errorf("Codex error: %s", message)
			case "response.failed":
				response := jsonObject(event["response"])
				message := "Codex response failed"
				if errorObj := jsonObject(response["error"]); errorObj != nil {
					if text, ok := stringValue(errorObj["message"]); ok && text != "" {
						message = text
					}
				}
				return nil, false, fmt.Errorf("%s", message)
			case "response.done", "response.completed", "response.incomplete":
				response := jsonObject(event["response"])
				if response != nil {
					if endTurn, ok := boolValue(response["end_turn"]); ok {
						output.EndTurn = &endTurn
					}
					if status, ok := stringValue(response["status"]); ok {
						response["status"] = normalizeCodexStatus(status)
					}
				}
				event["type"] = "response.completed"
				event["response"] = response
				stopped = true
				return event, true, nil
			default:
				return event, true, nil
			}
		}
	}
}

func normalizeCodexStatus(status string) string {
	switch status {
	case "completed", "incomplete", "failed", "cancelled", "queued", "in_progress":
		return status
	default:
		return ""
	}
}

func resolveCodexServiceTier(responseServiceTier *string, requestServiceTier *string) *string {
	if responseServiceTier != nil && *responseServiceTier == "default" && requestServiceTier != nil &&
		(*requestServiceTier == "flex" || *requestServiceTier == "priority") {
		return requestServiceTier
	}
	if responseServiceTier != nil {
		return responseServiceTier
	}
	return requestServiceTier
}

func applyCodexServiceTierPricing(usage *types.Usage, serviceTier *string, model *types.Model) {
	if usage == nil || serviceTier == nil {
		return
	}
	multiplier := 1.0
	switch *serviceTier {
	case "flex":
		multiplier = 0.5
	case "priority":
		if model.Id == "gpt-5.5" {
			multiplier = 2.5
		} else {
			multiplier = 2
		}
	}
	if multiplier == 1 {
		return
	}
	usage.Cost.Input *= multiplier
	usage.Cost.Output *= multiplier
	usage.Cost.CacheRead *= multiplier
	usage.Cost.CacheWrite *= multiplier
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
}

func terminateCodexStream(stream *types.AssistantMessageEventStream, output *types.AssistantMessage, model *types.Model, err error, wasAborted bool) {
	cleanupStreamingBlocks(output)
	if wasAborted {
		output.StopReason = types.StopReasonAborted
	} else {
		output.StopReason = types.StopReasonError
	}
	message := fmtProviderError(err, nil)
	output.ErrorMessage = &message
	stream.Push(types.NewErrorEvent(output.StopReason, *output))
	stream.End(output)
}

func extractCodexAccountID(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("Failed to extract accountId from token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("Failed to extract accountId from token")
	}
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", fmt.Errorf("Failed to extract accountId from token")
	}
	auth, _ := payload[jwtClaimPath].(map[string]any)
	accountID, _ := auth["chatgpt_account_id"].(string)
	if accountID == "" {
		return "", fmt.Errorf("Failed to extract accountId from token")
	}
	return accountID, nil
}

func buildCodexBaseHeaders(model *types.Model, additional types.ProviderHeaders, accountID, token string) map[string]string {
	headers := map[string]string{}
	for key, value := range model.Headers {
		headers[key] = value
	}
	for key, value := range additional {
		if value == nil {
			delete(headers, key)
			continue
		}
		headers[key] = *value
	}
	headers["Authorization"] = "Bearer " + token
	headers["chatgpt-account-id"] = accountID
	headers["originator"] = "pi"
	headers["User-Agent"] = utils.GetPiUserAgent()
	return headers
}

func buildCodexSSEHeaders(model *types.Model, options *types.ProviderRequestOptions, accountID, token string, sessionID *string) map[string]string {
	var additional types.ProviderHeaders
	if options != nil {
		additional = options.Headers
	}
	headers := buildCodexBaseHeaders(model, additional, accountID, token)
	headers["OpenAI-Beta"] = "responses=experimental"
	headers["accept"] = "text/event-stream"
	headers["content-type"] = "application/json"
	if sessionID != nil {
		headers["session-id"] = *sessionID
		headers["x-client-request-id"] = *sessionID
	}
	return headers
}

func buildCodexWebSocketHeaders(model *types.Model, options *types.ProviderRequestOptions, accountID, token, requestID string) http.Header {
	var additional types.ProviderHeaders
	if options != nil {
		additional = options.Headers
	}
	headers := buildCodexBaseHeaders(model, additional, accountID, token)
	delete(headers, "accept")
	delete(headers, "content-type")
	delete(headers, "OpenAI-Beta")
	delete(headers, "openai-beta")
	headers["OpenAI-Beta"] = openAIBetaResponsesWebSockets
	headers["x-client-request-id"] = requestID
	headers["session-id"] = requestID
	result := http.Header{}
	for key, value := range headers {
		result.Set(key, value)
	}
	return result
}

// =============================================================================
// WebSocket transport
// =============================================================================

func closeWebSocketSilently(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	_ = conn.Close(websocket.StatusNormalClosure, "done")
}

func codexWebSocketReusable(conn *websocket.Conn) bool {
	return conn != nil
}

func scheduleCodexWebSocketExpiry(sessionID, accountID string, entry *cachedCodexWebSocket) {
	if entry.idleTimer != nil {
		entry.idleTimer.Stop()
	}
	entry.idleTimer = time.AfterFunc(time.Duration(sessionWebSocketCacheTTLMs)*time.Millisecond, func() {
		codexWebSocketMu.Lock()
		defer codexWebSocketMu.Unlock()
		if entry.busy {
			return
		}
		closeWebSocketSilently(entry.conn)
		entries := codexWebSocketSessions[sessionID]
		if entries != nil && entries[accountID] == entry {
			delete(entries, accountID)
		}
		if len(entries) == 0 {
			delete(codexWebSocketSessions, sessionID)
		}
	})
}

func acquireCodexWebSocket(ctx context.Context, url string, headers http.Header, sessionID *string, accountID string, connectTimeoutMs *int) (*websocket.Conn, *cachedCodexWebSocket, bool, func(keep bool)) {
	if sessionID == nil {
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
		if err != nil {
			return nil, nil, false, func(bool) {}
		}
		conn.SetReadLimit(codexWebSocketReadLimit)
		return conn, nil, false, func(bool) {}
	}

	codexWebSocketMu.Lock()
	entries := codexWebSocketSessions[*sessionID]
	cached := entries[accountID]
	if cached != nil {
		if cached.idleTimer != nil {
			cached.idleTimer.Stop()
			cached.idleTimer = nil
		}
		if !cached.busy && time.Since(cached.createdAt) >= time.Duration(sessionWebSocketMaxAgeMs)*time.Millisecond {
			closeWebSocketSilently(cached.conn)
			delete(entries, accountID)
			cached = nil
		} else if !cached.busy && codexWebSocketReusable(cached.conn) {
			cached.busy = true
			codexWebSocketMu.Unlock()
			release := func(keep bool) {
				codexWebSocketMu.Lock()
				defer codexWebSocketMu.Unlock()
				if !keep {
					closeWebSocketSilently(cached.conn)
					current := codexWebSocketSessions[*sessionID]
					if current != nil && current[accountID] == cached {
						delete(current, accountID)
					}
					if len(current) == 0 {
						delete(codexWebSocketSessions, *sessionID)
					}
					return
				}
				cached.busy = false
				scheduleCodexWebSocketExpiry(*sessionID, accountID, cached)
			}
			return cached.conn, cached, true, release
		}
	}
	codexWebSocketMu.Unlock()

	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: headers})
	if err != nil {
		return nil, nil, false, func(bool) {}
	}
	conn.SetReadLimit(codexWebSocketReadLimit)
	entry := &cachedCodexWebSocket{conn: conn, busy: true, createdAt: time.Now()}

	codexWebSocketMu.Lock()
	entries = codexWebSocketSessions[*sessionID]
	if entries == nil {
		entries = map[string]*cachedCodexWebSocket{}
		codexWebSocketSessions[*sessionID] = entries
	}
	entries[accountID] = entry
	codexWebSocketMu.Unlock()

	release := func(keep bool) {
		codexWebSocketMu.Lock()
		defer codexWebSocketMu.Unlock()
		if !keep {
			closeWebSocketSilently(entry.conn)
			if entry.idleTimer != nil {
				entry.idleTimer.Stop()
			}
			current := codexWebSocketSessions[*sessionID]
			if current != nil && current[accountID] == entry {
				delete(current, accountID)
			}
			if len(current) == 0 {
				delete(codexWebSocketSessions, *sessionID)
			}
			return
		}
		entry.busy = false
		scheduleCodexWebSocketExpiry(*sessionID, accountID, entry)
	}
	return conn, entry, false, release
}

func processCodexWebSocket(url string, body map[string]any, model *types.Model, output *types.AssistantMessage, stream *types.AssistantMessageEventStream, onStart func(), apiKey *string, accountID string, codexSessionID *string, grammarProperties map[string]string, options *OpenAICodexResponsesOptions) error {
	requestID := ""
	if codexSessionID != nil {
		requestID = *codexSessionID
	} else {
		generated, err := utils.UUIDv7(nil)
		if err == nil {
			requestID = generated
		}
	}
	var reqOptions *types.ProviderRequestOptions
	if options != nil {
		reqOptions = &options.ProviderRequestOptions
	}
	headers := buildCodexWebSocketHeaders(model, reqOptions, accountID, *apiKey, requestID)

	ctx, cancel := contextForSignal(contextBackground(), codexOptionSignal(options))
	defer cancel()

	conn, entry, reused, release := acquireCodexWebSocket(ctx, url, headers, codexSessionID, accountID, codexOptionWebSocketTimeout(options))
	if conn == nil {
		return fmt.Errorf("WebSocket transport is not available")
	}
	keepConnection := true
	useCachedContext := options == nil || options.Transport == nil ||
		*options.Transport == types.TransportWebsocketCached || *options.Transport == types.TransportAuto

	requestBody := body
	if useCachedContext && entry != nil {
		codexWebSocketMu.Lock()
		continuation := entry.continuation
		codexWebSocketMu.Unlock()
		if continuation != nil {
			if cached, ok := buildCachedCodexRequestBody(continuation, body); ok {
				requestBody = cached
			} else {
				codexWebSocketMu.Lock()
				entry.continuation = nil
				codexWebSocketMu.Unlock()
			}
		}
	}

	if codexSessionID != nil {
		codexWebSocketMu.Lock()
		stats := codexGetOrCreateStats(*codexSessionID)
		stats.Requests++
		if reused {
			stats.ConnectionsReused++
		} else {
			stats.ConnectionsCreated++
		}
		if useCachedContext {
			stats.CachedContextRequests++
		}
		if store, ok := requestBody["store"].(bool); ok && store {
			stats.StoreTrueRequests++
		}
		stats.LastInputItems = codexInputLength(requestBody)
		if previous, ok := requestBody["previous_response_id"].(string); ok && previous != "" {
			stats.DeltaRequests++
			lastDelta := codexInputLength(requestBody)
			stats.LastDeltaInputItems = &lastDelta
			stats.LastPreviousResponseID = &previous
		} else {
			stats.FullContextRequests++
			stats.LastDeltaInputItems = nil
			stats.LastPreviousResponseID = nil
		}
		codexWebSocketMu.Unlock()
	}

	defer func() {
		release(keepConnection)
	}()

	payload, err := json.Marshal(appendCodexCreateEvent(requestBody))
	if err != nil {
		return err
	}
	if err := conn.Write(ctx, websocket.MessageText, payload); err != nil {
		keepConnection = false
		return err
	}

	messages := make(chan map[string]any, 16)
	readErrors := make(chan error, 1)
	go func() {
		defer close(messages)
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				readErrors <- err
				return
			}
			var event map[string]any
			if err := json.Unmarshal(data, &event); err != nil {
				readErrors <- fmt.Errorf("Invalid Codex WebSocket JSON: %w", err)
				return
			}
			eventType, _ := stringValue(event["type"])
			if eventType == "response.completed" || eventType == "response.done" || eventType == "response.incomplete" {
				messages <- event
				return
			}
			messages <- event
		}
	}()

	started := false
	rawNext := func() (map[string]any, bool, error) {
		event, ok := <-messages
		if !ok {
			select {
			case readErr := <-readErrors:
				if readErr != nil {
					return nil, false, readErr
				}
			default:
			}
			return nil, false, nil
		}
		if !started {
			started = true
			onStart()
		}
		return event, true, nil
	}
	next := codexNextFunc(rawNext, output, model, codexOptionObserver(options))

	if err := ProcessResponsesStream(next, output, stream, model, &OpenAIResponsesStreamOptions{
		ServiceTier:                codexOptionServiceTier(options),
		GrammarToolInputProperties: grammarProperties,
		ResolveServiceTier:         resolveCodexServiceTier,
		ApplyServiceTierPricing: func(usage *types.Usage, serviceTier *string) {
			applyCodexServiceTierPricing(usage, serviceTier, model)
		},
	}); err != nil {
		keepConnection = false
		return err
	}
	if aborted(codexOptionSignal(options)) {
		keepConnection = false
		return nil
	}
	if useCachedContext && entry != nil && output.ResponseId != nil {
		includeSystemPrompt := false
		normalized := utils.NormalizeContext(types.Context{Messages: []types.Message{types.NewAssistantMessageVariant(*output)}})
		items := ConvertResponsesMessages(model, normalized, codexToolCallProviders, &ConvertResponsesMessagesOptions{
			IncludeSystemPrompt:        &includeSystemPrompt,
			GrammarToolInputProperties: grammarProperties,
		})
		responseItems := make([]map[string]any, 0, len(items))
		for _, item := range items {
			itemType, _ := stringValue(item["type"])
			if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
				responseItems = append(responseItems, item)
			}
		}
		codexWebSocketMu.Lock()
		entry.continuation = &codexContinuationState{
			lastRequestBody:   body,
			lastResponseID:    *output.ResponseId,
			lastResponseItems: responseItems,
		}
		codexWebSocketMu.Unlock()
	}
	return nil
}

func codexInputLength(body map[string]any) int {
	switch input := body["input"].(type) {
	case []map[string]any:
		return len(input)
	case []any:
		return len(input)
	default:
		return 0
	}
}

func codexBodiesMatchExceptInput(a, b map[string]any) bool {
	left, _ := json.Marshal(filterCodexInputFields(a))
	right, _ := json.Marshal(filterCodexInputFields(b))
	return string(left) == string(right)
}

func filterCodexInputFields(body map[string]any) map[string]any {
	filtered := map[string]any{}
	for key, value := range body {
		if key == "input" || key == "previous_response_id" {
			continue
		}
		filtered[key] = value
	}
	return filtered
}

func codexJSONEqual(a, b map[string]any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func buildCachedCodexRequestBody(continuation *codexContinuationState, body map[string]any) (map[string]any, bool) {
	if continuation == nil || continuation.lastResponseID == "" {
		return nil, false
	}
	if !codexBodiesMatchExceptInput(body, continuation.lastRequestBody) {
		return nil, false
	}
	currentInput, _ := body["input"].([]map[string]any)
	lastInput, _ := continuation.lastRequestBody["input"].([]map[string]any)
	baseline := make([]map[string]any, 0, len(lastInput)+len(continuation.lastResponseItems))
	baseline = append(baseline, lastInput...)
	baseline = append(baseline, continuation.lastResponseItems...)
	if len(currentInput) < len(baseline) {
		return nil, false
	}
	for i := range baseline {
		if !codexJSONEqual(currentInput[i], baseline[i]) {
			return nil, false
		}
	}
	delta := currentInput[len(baseline):]
	if len(delta) == 0 {
		return nil, false
	}
	result := map[string]any{}
	for key, value := range body {
		result[key] = value
	}
	result["previous_response_id"] = continuation.lastResponseID
	result["input"] = delta
	return result, true
}

func appendCodexCreateEvent(body map[string]any) map[string]any {
	event := map[string]any{"type": "response.create"}
	for key, value := range body {
		event[key] = value
	}
	return event
}

func codexOptionSignal(options *OpenAICodexResponsesOptions) <-chan struct{} {
	if options == nil {
		return nil
	}
	return options.Signal
}

func codexOptionObserver(options *OpenAICodexResponsesOptions) func(data any, model *types.Model) error {
	if options == nil {
		return nil
	}
	return options.OnProviderStreamEvent
}

func codexOptionServiceTier(options *OpenAICodexResponsesOptions) *string {
	if options == nil {
		return nil
	}
	return options.ServiceTier
}

func codexOptionWebSocketTimeout(options *OpenAICodexResponsesOptions) *int {
	if options == nil {
		return nil
	}
	return options.WebsocketConnectTimeoutMs
}
