// This file is a Go port of packages/ai/src/api/openai-responses.ts from Pi at
// revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

const openAIResponsesMinOutputTokens = 16

// openAIToolCallProviders are the providers whose pipe-separated Responses item
// ids are preserved.
var openAIToolCallProviders = map[string]bool{
	string(types.ProviderOpenAI):      true,
	string(types.ProviderOpenAICodex): true,
	string(types.ProviderOpencode):    true,
}

// OpenAIResponsesOptions are the OpenAI Responses-specific stream options.
type OpenAIResponsesOptions struct {
	types.StreamOptions
	ReasoningEffort     *string
	ReasoningSummary    *string
	HasReasoningSummary bool
	ServiceTier         *string
	ToolChoice          any
}

type resolvedOpenAIResponsesCompat struct {
	SupportsDeveloperRole           bool
	SupportsMidConvoSystemMessages  bool
	SessionAffinityFormat           string
	SupportsLongCacheRetention      bool
	SupportsStrictMode              bool
	SupportsOpenAIGrammarTools      bool
	SupportsAdditionalTools         bool
	SupportsToolSearch              bool
	SupportsExplicitPromptCacheMode bool
	SupportsMaxOutputTokens         bool
}

func detectSessionAffinityFormat(model *types.Model) string {
	if model.Provider == types.ProviderOpenRouter || strings.Contains(model.BaseUrl, "openrouter.ai") {
		return "openrouter"
	}
	return "openai"
}

func getOpenAIResponsesCompat(model *types.Model) resolvedOpenAIResponsesCompat {
	compat := resolvedOpenAIResponsesCompat{
		SupportsDeveloperRole:           true,
		SupportsMidConvoSystemMessages:  false,
		SessionAffinityFormat:           detectSessionAffinityFormat(model),
		SupportsLongCacheRetention:      true,
		SupportsStrictMode:              false,
		SupportsOpenAIGrammarTools:      false,
		SupportsAdditionalTools:         false,
		SupportsToolSearch:              false,
		SupportsExplicitPromptCacheMode: false,
		SupportsMaxOutputTokens:         true,
	}
	if raw := responsesCompat(model); raw != nil {
		apply := func(target *bool, value *bool) {
			if value != nil {
				*target = *value
			}
		}
		apply(&compat.SupportsDeveloperRole, raw.SupportsDeveloperRole)
		apply(&compat.SupportsMidConvoSystemMessages, raw.SupportsMidConvoSystemMessages)
		apply(&compat.SupportsLongCacheRetention, raw.SupportsLongCacheRetention)
		apply(&compat.SupportsStrictMode, raw.SupportsStrictMode)
		apply(&compat.SupportsOpenAIGrammarTools, raw.SupportsOpenAIGrammarTools)
		apply(&compat.SupportsAdditionalTools, raw.SupportsAdditionalTools)
		apply(&compat.SupportsToolSearch, raw.SupportsToolSearch)
		apply(&compat.SupportsExplicitPromptCacheMode, raw.SupportsExplicitPromptCacheMode)
		apply(&compat.SupportsMaxOutputTokens, raw.SupportsMaxOutputTokens)
		if raw.SessionAffinityFormat != nil {
			compat.SessionAffinityFormat = string(*raw.SessionAffinityFormat)
		}
	}
	return compat
}

func getOpenAIPromptCacheRetention(compat resolvedOpenAIResponsesCompat, cacheRetention types.CacheRetention) *string {
	if cacheRetention == types.CacheRetentionLong && compat.SupportsLongCacheRetention && !compat.SupportsExplicitPromptCacheMode {
		value := "24h"
		return &value
	}
	return nil
}

func getOpenAIPromptCacheOptions(compat resolvedOpenAIResponsesCompat, cacheRetention types.CacheRetention) map[string]any {
	if !compat.SupportsExplicitPromptCacheMode {
		return nil
	}
	if cacheRetention == types.CacheRetentionNone {
		return map[string]any{"mode": "explicit"}
	}
	if cacheRetention == types.CacheRetentionLong && compat.SupportsLongCacheRetention {
		return map[string]any{"ttl": "30m"}
	}
	return nil
}

// hasHeader reports whether a header with a non-empty value is present.
func hasHeader(headers types.ProviderHeaders, name string) bool {
	expected := strings.ToLower(name)
	for key, value := range headers {
		if strings.ToLower(key) == expected && value != nil && strings.TrimSpace(*value) != "" {
			return true
		}
	}
	return false
}

// getClientAPIKey resolves the request API key, accepting an explicit
// authorization header as a substitute.
func getClientAPIKey(provider types.ProviderId, apiKey *string, headers types.ProviderHeaders) (string, error) {
	if apiKey != nil && *apiKey != "" {
		return *apiKey, nil
	}
	if hasHeader(headers, "authorization") || hasHeader(headers, "cf-aig-authorization") {
		return "unused", nil
	}
	return "", fmt.Errorf("No API key for provider: %s", provider)
}

// resolveCacheRetention resolves the prompt cache retention preference.
func resolveCacheRetention(cacheRetention *types.CacheRetention, env types.ProviderEnv) types.CacheRetention {
	if cacheRetention != nil {
		return *cacheRetention
	}
	if value := utils.GetProviderEnvValue("PI_CACHE_RETENTION", env); value != nil && *value == "long" {
		return types.CacheRetentionLong
	}
	return types.CacheRetentionShort
}

// applyProviderHeaders merges model, provider-specific and caller headers, with
// the caller's last so they win.
func applyProviderHeaders(model *types.Model, providerHeaders types.ProviderHeaders, options *types.ProviderRequestOptions) map[string]string {
	headers := map[string]string{"User-Agent": utils.GetPiUserAgent()}
	for key, value := range model.Headers {
		headers[key] = value
	}
	if model.Provider == types.ProviderGitHubCopilot && providerHeaders != nil {
		for key, value := range providerHeaders {
			if value == nil {
				delete(headers, key)
				continue
			}
			headers[key] = *value
		}
	}
	if options != nil {
		for key, value := range options.Headers {
			if value == nil {
				delete(headers, key)
				continue
			}
			headers[key] = *value
		}
	}
	return headers
}

// OpenAIResponsesStream is the streaming entry point for the OpenAI Responses
// API.
func OpenAIResponsesStream(model *types.Model, context *types.TranscriptContext, options *OpenAIResponsesOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	supportsMidConvo := false
	if context != nil {
		supportsMidConvo = getOpenAIResponsesCompat(model).SupportsMidConvoSystemMessages
	}
	normalizedContext := resolveTranscriptContext(context, supportsMidConvo)

	go func() {
		output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
		output.StopReason = types.StopReasonPending

		var reqOptions *types.ProviderRequestOptions
		if options != nil {
			reqOptions = &options.ProviderRequestOptions
		}

		apiKey, err := getClientAPIKey(model.Provider, providerOptionAPIKey(options), providerOptionHeaders(options))
		if err != nil {
			terminateResponsesStream(stream, &output, model, err, false)
			return
		}

		compat := getOpenAIResponsesCompat(model)
		cacheRetention := resolveCacheRetention(providerOptionCacheRetention(options), providerOptionEnv(options))
		var cacheSessionID *string
		if cacheRetention != types.CacheRetentionNone {
			cacheSessionID = providerOptionSessionID(options)
		}
		grammarProperties := createGrammarToolInputPropertiesForContext(normalizedContext, compat.SupportsOpenAIGrammarTools)
		params := buildOpenAIResponsesParams(model, normalizedContext, options, compat, cacheRetention, cacheSessionID, grammarProperties)

		if options != nil && options.OnPayload != nil {
			if next, payloadErr := options.OnPayload(params, model); payloadErr != nil {
				terminateResponsesStream(stream, &output, model, payloadErr, false)
				return
			} else if next != nil {
				if nextMap, ok := next.(map[string]any); ok {
					params = nextMap
				}
			}
		}

		headers := applyProviderHeaders(model, buildProviderHeadersForCompat(model, compat, cacheSessionID, normalizedContext), reqOptions)
		headers["content-type"] = "application/json"
		headers["authorization"] = "Bearer " + apiKey

		body, marshalErr := json.Marshal(params)
		if marshalErr != nil {
			terminateResponsesStream(stream, &output, model, marshalErr, false)
			return
		}

		requestContext, cancel := contextForSignal(contextBackground(), providerOptionSignal(options))
		defer cancel()

		response, _, requestErr := performRequestWithRetry(requestContext, reqOptions, http.MethodPost, strings.TrimRight(model.BaseUrl, "/")+"/responses", headers, body)
		if requestErr != nil {
			terminateResponsesStream(stream, &output, model, requestErr, aborted(providerOptionSignal(options)))
			return
		}
		defer response.Body.Close()

		if options != nil && options.OnResponse != nil {
			options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, model)
		}

		stream.Push(types.NewStartEvent(output))

		next := func() (map[string]any, bool, error) {
			return nil, false, nil
		}
		eventQueue := make(chan map[string]any, 16)
		errQueue := make(chan error, 1)
		go func() {
			defer close(eventQueue)
			errQueue <- readSSE(requestContext, response.Body, providerOptionSignal(options), func(event map[string]any) error {
				eventQueue <- event
				return nil
			})
		}()
		next = func() (map[string]any, bool, error) {
			select {
			case event, ok := <-eventQueue:
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
			}
		}

		if err := ProcessResponsesStream(next, &output, stream, model, &OpenAIResponsesStreamOptions{
			ServiceTier:                providerOptionServiceTier(options),
			GrammarToolInputProperties: grammarProperties,
			ApplyServiceTierPricing: func(usage *types.Usage, serviceTier *string) {
				applyOpenAIServiceTierPricing(usage, serviceTier, model)
			},
		}); err != nil {
			terminateResponsesStream(stream, &output, model, err, aborted(providerOptionSignal(options)))
			return
		}

		if aborted(providerOptionSignal(options)) {
			terminateResponsesStream(stream, &output, model, fmt.Errorf("Request was aborted"), true)
			return
		}
		if output.StopReason == types.StopReasonPending {
			terminateResponsesStream(stream, &output, model, fmt.Errorf("OpenAI Responses stream ended without a stop reason"), false)
			return
		}
		if output.StopReason == types.StopReasonAborted || output.StopReason == types.StopReasonError {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			terminateResponsesStream(stream, &output, model, fmt.Errorf("%s", message), output.StopReason == types.StopReasonAborted)
			return
		}

		stream.Push(types.NewDoneEvent(output.StopReason, output))
		stream.End(&output)
	}()

	return stream
}

// OpenAIResponsesStreamSimple is the unified-reasoning entry point for the
// OpenAI Responses API.
func OpenAIResponsesStreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	apiKey := (*string)(nil)
	if options != nil {
		apiKey = options.APIKey
	}
	if _, err := getClientAPIKey(model.Provider, apiKey, providerSimpleHeaders(options)); err != nil {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
			terminateResponsesStream(stream, &output, model, err, false)
		}()
		return stream
	}

	base := BuildBaseOptions(model, context, options, apiKey)
	typed := &OpenAIResponsesOptions{StreamOptions: base}
	if options != nil {
		typed.ToolChoice = options.ToolChoice
	}
	if options != nil && options.Reasoning != nil {
		clamped := clampThinkingLevel(model, *options.Reasoning)
		if clamped != types.ThinkingOff {
			effort := string(clamped)
			typed.ReasoningEffort = &effort
		}
	}
	return OpenAIResponsesStream(model, context, typed)
}

func buildOpenAIResponsesParams(model *types.Model, context *types.TranscriptContext, options *OpenAIResponsesOptions, compat resolvedOpenAIResponsesCompat, cacheRetention types.CacheRetention, cacheSessionID *string, grammarProperties map[string]string) map[string]any {
	supportsToolAdditions := compat.SupportsAdditionalTools || compat.SupportsToolSearch
	transcriptTools := utils.ResolveTranscriptTools(context.Messages, supportsToolAdditions)
	toolOptions := &ConvertResponsesToolsOptions{
		SupportsStrictMode:         &compat.SupportsStrictMode,
		SupportsOpenAIGrammarTools: &compat.SupportsOpenAIGrammarTools,
	}
	includeSystemPrompt := true
	messages := ConvertResponsesMessages(model, context, openAIToolCallProviders, &ConvertResponsesMessagesOptions{
		IncludeSystemPrompt:            &includeSystemPrompt,
		GrammarToolInputProperties:     grammarProperties,
		SupportsMidConvoSystemMessages: compat.SupportsMidConvoSystemMessages,
		SupportsAdditionalTools:        compat.SupportsAdditionalTools,
		SupportsToolSearch:             compat.SupportsToolSearch,
		ToolOptions:                    toolOptions,
	})

	params := map[string]any{
		"model":  model.Id,
		"input":  messages,
		"stream": true,
		"store":  false,
	}

	if cacheRetention != types.CacheRetentionNone {
		if clamped := ClampOpenAIPromptCacheKey(cacheSessionID); clamped != nil {
			params["prompt_cache_key"] = *clamped
		}
	}
	if retention := getOpenAIPromptCacheRetention(compat, cacheRetention); retention != nil {
		params["prompt_cache_retention"] = *retention
	}
	if cacheOptions := getOpenAIPromptCacheOptions(compat, cacheRetention); cacheOptions != nil {
		params["prompt_cache_options"] = cacheOptions
	}

	if options != nil && options.MaxTokens != nil && compat.SupportsMaxOutputTokens {
		value := *options.MaxTokens
		if value < openAIResponsesMinOutputTokens {
			value = openAIResponsesMinOutputTokens
		}
		params["max_output_tokens"] = value
	}
	if options != nil && options.Temperature != nil {
		params["temperature"] = *options.Temperature
	}
	if options != nil && options.ServiceTier != nil {
		params["service_tier"] = *options.ServiceTier
	}
	if len(transcriptTools.RequestTools) > 0 {
		params["tools"] = ConvertResponsesTools(transcriptTools.RequestTools, toolOptions)
	}
	if options != nil && options.ToolChoice != nil {
		params["tool_choice"] = options.ToolChoice
	}

	if model.Reasoning {
		if options != nil && (options.ReasoningEffort != nil || options.HasReasoningSummary) {
			effort := "medium"
			if options.ReasoningEffort != nil {
				effort = resolveThinkingLevelValue(model, *options.ReasoningEffort)
			}
			summary := "auto"
			if options.HasReasoningSummary && options.ReasoningSummary != nil {
				summary = *options.ReasoningSummary
			}
			params["reasoning"] = map[string]any{"effort": effort, "summary": summary}
			params["include"] = []string{"reasoning.encrypted_content"}
		} else if model.Provider != types.ProviderGitHubCopilot {
			_, present, supported := model.ThinkingLevelMap.Lookup(types.ThinkingOff)
			if !present || supported {
				offValue := "none"
				if present && supported {
					if mapped, _, _ := model.ThinkingLevelMap.Lookup(types.ThinkingOff); mapped != "" {
						offValue = mapped
					}
				}
				params["reasoning"] = map[string]any{"effort": offValue}
			}
		}
		if model.Provider == types.ProviderXAI {
			params["include"] = []string{"reasoning.encrypted_content"}
		}
	}

	if options != nil {
		for key, value := range options.SamplingParams {
			params[key] = value
		}
	}
	return params
}

func resolveThinkingLevelValue(model *types.Model, level string) string {
	if model == nil {
		return level
	}
	if value, present, supported := model.ThinkingLevelMap.Lookup(types.ModelThinkingLevel(level)); present && supported {
		return value
	}
	return level
}

func buildProviderHeadersForCompat(model *types.Model, compat resolvedOpenAIResponsesCompat, cacheSessionID *string, context *types.TranscriptContext) types.ProviderHeaders {
	headers := types.ProviderHeaders{}
	if model.Provider == types.ProviderGitHubCopilot && context != nil {
		hasImages := HasCopilotVisionInput(context.Messages)
		for key, value := range BuildCopilotDynamicHeaders(context.Messages, hasImages) {
			text := value
			headers[key] = &text
		}
	}
	if cacheSessionID != nil {
		if compat.SessionAffinityFormat == "openrouter" {
			value := *cacheSessionID
			headers["x-session-id"] = &value
		} else {
			if compat.SessionAffinityFormat == "openai" {
				value := *cacheSessionID
				headers["session_id"] = &value
			}
			value := *cacheSessionID
			headers["x-client-request-id"] = &value
		}
	}
	return headers
}

func createGrammarToolInputPropertiesForContext(context *types.TranscriptContext, supportsGrammarTools bool) map[string]string {
	if context == nil {
		return map[string]string{}
	}
	properties, err := CreateGrammarToolInputProperties(utils.GetDeclaredTools(context.Messages), supportsGrammarTools)
	if err != nil {
		return map[string]string{}
	}
	return properties
}

func applyOpenAIServiceTierPricing(usage *types.Usage, serviceTier *string, model *types.Model) {
	multiplier := 1.0
	if serviceTier != nil {
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

func terminateResponsesStream(stream *types.AssistantMessageEventStream, output *types.AssistantMessage, model *types.Model, err error, wasAborted bool) {
	cleanupStreamingBlocks(output)
	if wasAborted {
		output.StopReason = types.StopReasonAborted
	} else {
		output.StopReason = types.StopReasonError
	}
	message := err.Error()
	if model != nil && model.Provider == types.ProviderOpenAI {
		message = fmtProviderError(err, stringPtr("OpenAI API error"))
	} else {
		prefix := ""
		if model != nil {
			prefix = string(model.Provider) + " API error"
		}
		message = fmtProviderError(err, &prefix)
	}
	output.ErrorMessage = &message
	stream.Push(types.NewErrorEvent(output.StopReason, *output))
	stream.End(output)
}

func cleanupStreamingBlocks(output *types.AssistantMessage) {
	// Streaming scratch buffers (partial JSON, grammar/custom input) live in the
	// per-stream state and never on the exported message, so the finalized
	// content is already clean. Retained for the call sites that mirror the
	// upstream catch-path cleanup.
	_ = output
}

func resolveTranscriptContext(context *types.TranscriptContext, supportsMidConvo bool) *types.TranscriptContext {
	input := types.TranscriptContext{}
	if context != nil {
		input = *context
	}
	flag := supportsMidConvo
	normalized := utils.ResolveTranscript(input, &flag)
	return &normalized
}

func stringPtr(value string) *string { return &value }

func contextBackground() context.Context { return context.Background() }

func providerOptionAPIKey(options *OpenAIResponsesOptions) *string {
	if options == nil {
		return nil
	}
	return options.APIKey
}

func providerOptionHeaders(options *OpenAIResponsesOptions) types.ProviderHeaders {
	if options == nil {
		return nil
	}
	return options.Headers
}

func providerOptionCacheRetention(options *OpenAIResponsesOptions) *types.CacheRetention {
	if options == nil {
		return nil
	}
	return options.CacheRetention
}

func providerOptionEnv(options *OpenAIResponsesOptions) types.ProviderEnv {
	if options == nil {
		return nil
	}
	return options.Env
}

func providerOptionSessionID(options *OpenAIResponsesOptions) *string {
	if options == nil {
		return nil
	}
	return options.SessionId
}

func providerOptionSignal(options *OpenAIResponsesOptions) <-chan struct{} {
	if options == nil {
		return nil
	}
	return options.Signal
}

func providerOptionServiceTier(options *OpenAIResponsesOptions) *string {
	if options == nil {
		return nil
	}
	return options.ServiceTier
}

func providerSimpleHeaders(options *types.SimpleStreamOptions) types.ProviderHeaders {
	if options == nil {
		return nil
	}
	return options.Headers
}
