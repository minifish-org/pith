// This file is a Go port of packages/ai/src/api/openai-completions.ts from Pi at
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

// OpenAICompletionsOptions are the OpenAI Chat Completions stream options.
type OpenAICompletionsOptions struct {
	types.StreamOptions
	ToolChoice      any
	ReasoningEffort *string
	ThinkingBudgets *types.ThinkingBudgets
}

// ConvertCompletionsMessagesOptions are the options for completions message
// conversion.
type ConvertCompletionsMessagesOptions struct {
	GrammarToolInputProperties map[string]string
}

type openAICompatCacheControl struct {
	Type string
	TTL  *string
}

func (c openAICompatCacheControl) toMap() map[string]any {
	result := map[string]any{"type": c.Type}
	if c.TTL != nil {
		result["ttl"] = *c.TTL
	}
	return result
}

type resolvedOpenAICompletionsCompat struct {
	SupportsStore                               bool
	SupportsDeveloperRole                       bool
	SupportsReasoningEffort                     bool
	SupportsUsageInStreaming                    bool
	SupportsFinishReason                        bool
	MaxTokensField                              string
	RequiresToolResultName                      bool
	RequiresAssistantAfterToolResult            bool
	RequiresThinkingAsText                      bool
	RequiresReasoningContentOnAssistantMessages bool
	ThinkingFormat                              string
	ChatTemplateKwargs                          map[string]types.ChatTemplateKwargValue
	ChatTemplateArgs                            map[string]types.ChatTemplateKwargValue
	OpenRouterRouting                           *types.OpenRouterRouting
	VercelGatewayRouting                        *types.VercelGatewayRouting
	ZaiToolStream                               bool
	ThinkingTokenBudgetField                    *types.ThinkingTokenBudgetField
	SupportsThinkingTokenBudget                 *bool
	SupportsStrictMode                          bool
	SupportsOpenAIGrammarTools                  bool
	SupportsMidConvoSystemMessages              bool
	SupportsMidConvoToolAdditions               bool
	CacheControlFormat                          *string
	SendSessionAffinityHeaders                  bool
	SessionAffinityFormat                       string
	SupportsLongCacheRetention                  bool
	VLLMPriority                                *int
}

func detectOpenAICompletionsCompat(model *types.Model) resolvedOpenAICompletionsCompat {
	provider := string(model.Provider)
	baseURL := model.BaseUrl

	isZai := provider == string(types.ProviderZai) || provider == string(types.ProviderZaiCodingCN) ||
		strings.Contains(baseURL, "api.z.ai") || strings.Contains(baseURL, "open.bigmodel.cn")
	isTogether := provider == string(types.ProviderTogether) || strings.Contains(baseURL, "api.together.ai") || strings.Contains(baseURL, "api.together.xyz")
	isMoonshot := provider == string(types.ProviderMoonshotAI) || provider == string(types.ProviderMoonshotAICN) || strings.Contains(baseURL, "api.moonshot.")
	isOpenRouter := provider == string(types.ProviderOpenRouter) || strings.Contains(baseURL, "openrouter.ai")
	isCloudflareWorkersAI := provider == string(types.ProviderCloudflareWorkersAI) || strings.Contains(baseURL, "api.cloudflare.com")
	isCloudflareAIGateway := provider == string(types.ProviderCloudflareAIGateway) || strings.Contains(baseURL, "gateway.ai.cloudflare.com")
	isNvidia := provider == string(types.ProviderNvidia) || strings.Contains(baseURL, "integrate.api.nvidia.com")
	isAntLing := provider == string(types.ProviderAntLing) || strings.Contains(baseURL, "api.ant-ling.com")
	isCerebras := provider == string(types.ProviderCerebras) || strings.Contains(baseURL, "cerebras.ai")
	isDeepSeek := provider == string(types.ProviderDeepSeek) || strings.Contains(strings.ToLower(baseURL), "deepseek.com")

	isNonStandard := isNvidia || isCerebras || provider == string(types.ProviderXAI) || strings.Contains(baseURL, "api.x.ai") ||
		isTogether || strings.Contains(baseURL, "chutes.ai") || isDeepSeek || isZai || isMoonshot ||
		provider == string(types.ProviderOpencode) || strings.Contains(baseURL, "opencode.ai") ||
		isCloudflareWorkersAI || isCloudflareAIGateway || isAntLing

	useMaxTokens := strings.Contains(baseURL, "chutes.ai") || isDeepSeek || isMoonshot ||
		isCloudflareAIGateway || isTogether || isNvidia || isAntLing || isZai

	isGrok := provider == string(types.ProviderXAI) || strings.Contains(baseURL, "api.x.ai")
	isOpenRouterDeveloperRoleModel := isOpenRouter && (strings.HasPrefix(model.Id, "anthropic/") || strings.HasPrefix(model.Id, "openai/"))

	thinkingFormat := "openai"
	switch {
	case isDeepSeek:
		thinkingFormat = "deepseek"
	case isZai:
		thinkingFormat = "zai"
	case isTogether:
		thinkingFormat = "together"
	case isAntLing:
		thinkingFormat = "ant-ling"
	case isOpenRouter:
		thinkingFormat = "openrouter"
	}

	compat := resolvedOpenAICompletionsCompat{
		SupportsStore:                               !isNonStandard,
		SupportsDeveloperRole:                       isOpenRouterDeveloperRoleModel || (!isNonStandard && !isOpenRouter),
		SupportsReasoningEffort:                     !isGrok && !isZai && !isMoonshot && !isTogether && !isCloudflareAIGateway && !isNvidia && !isAntLing,
		SupportsUsageInStreaming:                    true,
		SupportsFinishReason:                        true,
		MaxTokensField:                              "max_completion_tokens",
		RequiresToolResultName:                      false,
		RequiresAssistantAfterToolResult:            false,
		RequiresThinkingAsText:                      false,
		RequiresReasoningContentOnAssistantMessages: isDeepSeek,
		ThinkingFormat:                              thinkingFormat,
		ChatTemplateKwargs:                          map[string]types.ChatTemplateKwargValue{},
		ChatTemplateArgs:                            map[string]types.ChatTemplateKwargValue{},
		OpenRouterRouting:                           &types.OpenRouterRouting{},
		VercelGatewayRouting:                        &types.VercelGatewayRouting{},
		SupportsStrictMode:                          false,
		SupportsOpenAIGrammarTools:                  false,
		SupportsMidConvoSystemMessages:              false,
		SupportsMidConvoToolAdditions:               false,
		SendSessionAffinityHeaders:                  isOpenRouter,
		SessionAffinityFormat:                       "openai",
		SupportsLongCacheRetention:                  !(isTogether || isCloudflareWorkersAI || isCloudflareAIGateway || isNvidia || isAntLing),
	}
	if useMaxTokens {
		compat.MaxTokensField = "max_tokens"
	}
	if provider == string(types.ProviderOpenRouter) && strings.HasPrefix(model.Id, "anthropic/") {
		format := "anthropic"
		compat.CacheControlFormat = &format
	}
	if isOpenRouter {
		compat.SessionAffinityFormat = "openrouter"
	}
	return compat
}

func getOpenAICompletionsCompat(model *types.Model) resolvedOpenAICompletionsCompat {
	detected := detectOpenAICompletionsCompat(model)
	raw := (*types.OpenAICompletionsCompat)(nil)
	if model != nil {
		raw = model.Compat.OpenAICompletions
	}
	if raw == nil {
		return detected
	}
	applyBool := func(target *bool, value *bool) {
		if value != nil {
			*target = *value
		}
	}
	applyBool(&detected.SupportsStore, raw.SupportsStore)
	applyBool(&detected.SupportsDeveloperRole, raw.SupportsDeveloperRole)
	applyBool(&detected.SupportsReasoningEffort, raw.SupportsReasoningEffort)
	applyBool(&detected.SupportsUsageInStreaming, raw.SupportsUsageInStreaming)
	applyBool(&detected.SupportsFinishReason, raw.SupportsFinishReason)
	if raw.MaxTokensField != nil {
		detected.MaxTokensField = *raw.MaxTokensField
	}
	applyBool(&detected.RequiresToolResultName, raw.RequiresToolResultName)
	applyBool(&detected.RequiresAssistantAfterToolResult, raw.RequiresAssistantAfterToolResult)
	applyBool(&detected.RequiresThinkingAsText, raw.RequiresThinkingAsText)
	applyBool(&detected.RequiresReasoningContentOnAssistantMessages, raw.RequiresReasoningContentOnAssistantMessages)
	if raw.ThinkingFormat != nil {
		detected.ThinkingFormat = *raw.ThinkingFormat
	}
	if raw.OpenRouterRouting != nil {
		detected.OpenRouterRouting = raw.OpenRouterRouting
	}
	if raw.VercelGatewayRouting != nil {
		detected.VercelGatewayRouting = raw.VercelGatewayRouting
	}
	if raw.ChatTemplateKwargs != nil {
		detected.ChatTemplateKwargs = raw.ChatTemplateKwargs
	}
	if raw.ChatTemplateArgs != nil {
		detected.ChatTemplateArgs = raw.ChatTemplateArgs
	}
	applyBool(&detected.ZaiToolStream, raw.ZaiToolStream)
	if raw.SupportsThinkingTokenBudget != nil {
		value := *raw.SupportsThinkingTokenBudget
		detected.SupportsThinkingTokenBudget = &value
	}
	if raw.ThinkingTokenBudgetField != nil {
		detected.ThinkingTokenBudgetField = raw.ThinkingTokenBudgetField
	}
	applyBool(&detected.SupportsStrictMode, raw.SupportsStrictMode)
	applyBool(&detected.SupportsOpenAIGrammarTools, raw.SupportsOpenAIGrammarTools)
	applyBool(&detected.SupportsMidConvoSystemMessages, raw.SupportsMidConvoSystemMessages)
	applyBool(&detected.SupportsMidConvoToolAdditions, raw.SupportsMidConvoToolAdditions)
	if raw.CacheControlFormat != nil {
		detected.CacheControlFormat = raw.CacheControlFormat
	}
	applyBool(&detected.SendSessionAffinityHeaders, raw.SendSessionAffinityHeaders)
	if raw.SessionAffinityFormat != nil {
		detected.SessionAffinityFormat = string(*raw.SessionAffinityFormat)
	}
	applyBool(&detected.SupportsLongCacheRetention, raw.SupportsLongCacheRetention)
	if raw.VLLMPriority != nil {
		detected.VLLMPriority = raw.VLLMPriority
	}
	return detected
}

// hasToolHistory reports whether a transcript already carries tool calls or
// results.
func hasToolHistory(messages []types.Message) bool {
	for _, message := range messages {
		if message.Role == types.ToolResultMessageRole {
			return true
		}
		if message.Role == types.AssistantMessageRole && message.Assistant != nil {
			for _, block := range message.Assistant.Content {
				if block.Type == types.ContentTypeToolCall {
					return true
				}
			}
		}
	}
	return false
}

// OpenAICompletionsStream is the streaming entry point for OpenAI-compatible
// chat completions APIs.
func OpenAICompletionsStream(model *types.Model, context *types.TranscriptContext, options *OpenAICompletionsOptions) *types.AssistantMessageEventStream {
	compat := getOpenAICompletionsCompat(model)
	supportsMidConvo := compat.SupportsMidConvoSystemMessages
	normalizedContext := resolveTranscriptContext(context, supportsMidConvo)

	stream := types.NewAssistantMessageEventStream()
	go func() {
		output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
		output.StopReason = types.StopReasonPending

		apiKey, err := getClientAPIKey(model.Provider, completionsOptionAPIKey(options), completionsOptionHeaders(options))
		if err != nil {
			terminateCompletionsStream(stream, &output, err, false)
			return
		}

		grammarProperties := createCompletionsGrammarProperties(normalizedContext, compat.SupportsOpenAIGrammarTools)
		cacheRetention := resolveCacheRetention(completionsOptionCacheRetention(options), completionsOptionEnv(options))
		var cacheSessionID *string
		if cacheRetention != types.CacheRetentionNone {
			cacheSessionID = completionsOptionSessionID(options)
		}
		params := buildOpenAICompletionsParams(model, normalizedContext, options, compat, cacheRetention, cacheSessionID, grammarProperties)

		var reqOptions *types.ProviderRequestOptions
		if options != nil {
			reqOptions = &options.ProviderRequestOptions
			if options.OnPayload != nil {
				if next, payloadErr := options.OnPayload(params, model); payloadErr != nil {
					terminateCompletionsStream(stream, &output, payloadErr, false)
					return
				} else if nextMap, ok := next.(map[string]any); ok {
					params = nextMap
				}
			}
		}

		headers := applyProviderHeaders(model, buildCompletionsHeaders(model, compat, cacheSessionID, normalizedContext, reqOptions), reqOptions)
		headers["content-type"] = "application/json"
		headers["authorization"] = "Bearer " + apiKey

		body, marshalErr := json.Marshal(params)
		if marshalErr != nil {
			terminateCompletionsStream(stream, &output, marshalErr, false)
			return
		}

		requestContext, cancel := contextForSignal(contextBackground(), completionsOptionSignal(options))
		defer cancel()

		response, _, requestErr := performRequestWithRetry(requestContext, reqOptions, http.MethodPost, strings.TrimRight(model.BaseUrl, "/")+"/chat/completions", headers, body)
		if requestErr != nil {
			terminateCompletionsStream(stream, &output, requestErr, aborted(completionsOptionSignal(options)))
			return
		}
		defer response.Body.Close()

		if options != nil && options.OnResponse != nil {
			options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, model)
		}

		stream.Push(types.NewStartEvent(output))

		eventQueue := make(chan map[string]any, 16)
		errQueue := make(chan error, 1)
		go func() {
			defer close(eventQueue)
			errQueue <- readSSE(requestContext, response.Body, completionsOptionSignal(options), func(event map[string]any) error {
				eventQueue <- event
				return nil
			})
		}()

		processErr := processOpenAICompletionsEvents(func() (map[string]any, bool, error) {
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
		}, &output, stream, model, compat, grammarProperties)
		if processErr != nil {
			terminateCompletionsStream(stream, &output, processErr, aborted(completionsOptionSignal(options)))
			return
		}

		if aborted(completionsOptionSignal(options)) {
			terminateCompletionsStream(stream, &output, fmt.Errorf("Request was aborted"), true)
			return
		}
		if output.StopReason == types.StopReasonAborted {
			terminateCompletionsStream(stream, &output, fmt.Errorf("Request was aborted"), true)
			return
		}
		if output.StopReason == types.StopReasonError {
			message := "Provider returned an error stop reason"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			terminateCompletionsStream(stream, &output, fmt.Errorf("%s", message), false)
			return
		}
		if output.StopReason == types.StopReasonPending {
			terminateCompletionsStream(stream, &output, fmt.Errorf("Stream ended without finish_reason"), false)
			return
		}

		stream.Push(types.NewDoneEvent(output.StopReason, output))
		stream.End(&output)
	}()

	return stream
}

// OpenAICompletionsStreamSimple is the unified-reasoning entry point for
// OpenAI-compatible chat completions APIs.
func OpenAICompletionsStreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	apiKey := (*string)(nil)
	if options != nil {
		apiKey = options.APIKey
	}
	if _, err := getClientAPIKey(model.Provider, apiKey, providerSimpleHeaders(options)); err != nil {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
			terminateCompletionsStream(stream, &output, err, false)
		}()
		return stream
	}
	base := BuildBaseOptions(model, context, options, apiKey)
	typed := &OpenAICompletionsOptions{StreamOptions: base}
	if options != nil {
		typed.ToolChoice = options.ToolChoice
		typed.ThinkingBudgets = options.ThinkingBudgets
		if options.Reasoning != nil {
			clamped := clampThinkingLevel(model, *options.Reasoning)
			if clamped != types.ThinkingOff {
				effort := string(clamped)
				typed.ReasoningEffort = &effort
			}
		}
	}
	return OpenAICompletionsStream(model, context, typed)
}

func buildOpenAICompletionsParams(model *types.Model, context *types.TranscriptContext, options *OpenAICompletionsOptions, compat resolvedOpenAICompletionsCompat, cacheRetention types.CacheRetention, cacheSessionID *string, grammarProperties map[string]string) map[string]any {
	transcriptTools := utils.ResolveTranscriptTools(context.Messages, compat.SupportsMidConvoSystemMessages && compat.SupportsMidConvoToolAdditions)
	messages := ConvertMessages(model, context, compat, &ConvertCompletionsMessagesOptions{GrammarToolInputProperties: grammarProperties})
	cacheControl := completionsCacheControl(compat, cacheRetention)

	params := map[string]any{
		"model":    model.Id,
		"messages": messages,
		"stream":   true,
	}

	usePromptCache := (strings.Contains(model.BaseUrl, "api.openai.com") && cacheRetention != types.CacheRetentionNone) ||
		(cacheRetention == types.CacheRetentionLong && compat.SupportsLongCacheRetention)
	if usePromptCache {
		if clamped := ClampOpenAIPromptCacheKey(cacheSessionID); clamped != nil {
			params["prompt_cache_key"] = *clamped
		}
	}
	if cacheRetention == types.CacheRetentionLong && compat.SupportsLongCacheRetention {
		params["prompt_cache_retention"] = "24h"
	}

	if compat.SupportsUsageInStreaming {
		params["stream_options"] = map[string]any{"include_usage": true}
	}
	if compat.SupportsStore {
		params["store"] = false
	}
	if options != nil && options.MaxTokens != nil {
		if compat.MaxTokensField == "max_tokens" {
			params["max_tokens"] = *options.MaxTokens
		} else {
			params["max_completion_tokens"] = *options.MaxTokens
		}
	}
	if options != nil && options.Temperature != nil {
		params["temperature"] = *options.Temperature
	}
	if len(transcriptTools.RequestTools) > 0 {
		params["tools"] = convertCompletionsTools(transcriptTools.RequestTools, compat)
		if compat.ZaiToolStream {
			params["tool_stream"] = true
		}
	} else if hasToolHistory(context.Messages) {
		params["tools"] = []any{}
	}
	if cacheControl != nil {
		applyAnthropicCacheControl(messages, params, *cacheControl)
	}
	if options != nil && options.ToolChoice != nil {
		params["tool_choice"] = options.ToolChoice
	}
	if compat.VLLMPriority != nil {
		params["priority"] = *compat.VLLMPriority
	}

	thinkingBudget := resolveCompletionsThinkingBudget(model, options, params)
	applyCompletionsThinkingFormat(model, options, compat, params, thinkingBudget)

	thinkingTokenBudgetField := resolveThinkingTokenBudgetField(compat)
	if thinkingTokenBudgetField != nil && thinkingBudget != nil {
		params[string(*thinkingTokenBudgetField)] = *thinkingBudget
	}
	if model.Compat.OpenAICompletions != nil && model.Compat.OpenAICompletions.OpenRouterRouting != nil {
		params["provider"] = model.Compat.OpenAICompletions.OpenRouterRouting
	}
	if model.Compat.OpenAICompletions != nil && model.Compat.OpenAICompletions.VercelGatewayRouting != nil {
		routing := model.Compat.OpenAICompletions.VercelGatewayRouting
		if len(routing.Only) > 0 || len(routing.Order) > 0 {
			gateway := map[string]any{}
			if len(routing.Only) > 0 {
				gateway["only"] = routing.Only
			}
			if len(routing.Order) > 0 {
				gateway["order"] = routing.Order
			}
			params["providerOptions"] = map[string]any{"gateway": gateway}
		}
	}
	if options != nil {
		for key, value := range options.SamplingParams {
			params[key] = value
		}
	}
	return params
}

func resolveCompletionsThinkingBudget(model *types.Model, options *OpenAICompletionsOptions, params map[string]any) *int {
	if options == nil || options.ReasoningEffort == nil || !model.Reasoning {
		return nil
	}
	ceiling := model.MaxTokens
	if value, ok := params["max_tokens"].(int); ok {
		ceiling = float64(value)
	} else if value, ok := params["max_completion_tokens"].(int); ok {
		ceiling = float64(value)
	}
	budget := float64(ThinkingBudgetForLevel(types.ThinkingLevel(*options.ReasoningEffort), options.ThinkingBudgets))
	clamped := ClampThinkingBudgetToAnswerRoom(budget, ceiling)
	if clamped <= 0 {
		return nil
	}
	value := int(clamped)
	return &value
}

func applyCompletionsThinkingFormat(model *types.Model, options *OpenAICompletionsOptions, compat resolvedOpenAICompletionsCompat, params map[string]any, thinkingBudget *int) {
	var effort *string
	if options != nil {
		effort = options.ReasoningEffort
	}
	switch compat.ThinkingFormat {
	case "zai":
		if !model.Reasoning {
			return
		}
		if effort != nil {
			params["thinking"] = map[string]any{"type": "enabled", "clear_thinking": false}
			if compat.SupportsReasoningEffort {
				params["reasoning_effort"] = resolveThinkingLevelValue(model, *effort)
			}
		} else {
			params["thinking"] = map[string]any{"type": "disabled"}
		}
	case "qwen":
		if !model.Reasoning {
			return
		}
		params["enable_thinking"] = effort != nil
		if effort != nil && compat.SupportsReasoningEffort {
			params["reasoning_effort"] = resolveThinkingLevelValue(model, *effort)
		}
	case "qwen-chat-template":
		if !model.Reasoning {
			return
		}
		params["chat_template_kwargs"] = map[string]any{"enable_thinking": effort != nil, "preserve_thinking": true}
	case "chat-template":
		if !model.Reasoning {
			return
		}
		if values := buildChatTemplateValues(model, options, compat.ChatTemplateKwargs, thinkingBudget); values != nil {
			params["chat_template_kwargs"] = values
		}
	case "baseten":
		if !model.Reasoning {
			return
		}
		if values := buildChatTemplateValues(model, options, compat.ChatTemplateArgs, thinkingBudget); values != nil {
			params["chat_template_args"] = values
		}
		if compat.SupportsReasoningEffort {
			var requested *string
			if effort != nil {
				requested = effort
			}
			value := ""
			if requested != nil {
				value = resolveThinkingLevelValue(model, *requested)
			} else if mapped, present, supported := model.ThinkingLevelMap.Lookup(types.ThinkingOff); present && supported {
				value = mapped
			}
			if value != "" {
				params["reasoning_effort"] = value
			}
		}
	case "deepseek":
		if !model.Reasoning {
			return
		}
		if effort != nil {
			params["thinking"] = map[string]any{"type": "enabled"}
		} else {
			_, present, supported := model.ThinkingLevelMap.Lookup(types.ThinkingOff)
			if !present || supported {
				params["thinking"] = map[string]any{"type": "disabled"}
			}
		}
		if effort != nil && compat.SupportsReasoningEffort {
			params["reasoning_effort"] = resolveThinkingLevelValue(model, *effort)
		}
	case "openrouter":
		if !model.Reasoning {
			return
		}
		if effort != nil {
			params["reasoning"] = map[string]any{"effort": resolveThinkingLevelValue(model, *effort)}
		} else {
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
	case "ant-ling":
		if !model.Reasoning || effort == nil {
			return
		}
		if mapped, present, supported := model.ThinkingLevelMap.Lookup(types.ModelThinkingLevel(*effort)); present && supported {
			params["reasoning"] = map[string]any{"effort": mapped}
		}
	case "together":
		if !model.Reasoning {
			return
		}
		params["reasoning"] = map[string]any{"enabled": effort != nil}
		if effort != nil && compat.SupportsReasoningEffort {
			params["reasoning_effort"] = resolveThinkingLevelValue(model, *effort)
		}
	case "string-thinking":
		if !model.Reasoning {
			return
		}
		if effort != nil {
			params["thinking"] = resolveThinkingLevelValue(model, *effort)
		} else {
			_, present, supported := model.ThinkingLevelMap.Lookup(types.ThinkingOff)
			if !present || supported {
				offValue := "none"
				if present && supported {
					if mapped, _, _ := model.ThinkingLevelMap.Lookup(types.ThinkingOff); mapped != "" {
						offValue = mapped
					}
				}
				params["thinking"] = offValue
			}
		}
	default:
		if effort != nil && model.Reasoning && compat.SupportsReasoningEffort {
			params["reasoning_effort"] = resolveThinkingLevelValue(model, *effort)
		} else if effort == nil && model.Reasoning && compat.SupportsReasoningEffort {
			if mapped, present, supported := model.ThinkingLevelMap.Lookup(types.ThinkingOff); present && supported && mapped != "" {
				params["reasoning_effort"] = mapped
			}
		}
	}
}

func resolveThinkingTokenBudgetField(compat resolvedOpenAICompletionsCompat) *types.ThinkingTokenBudgetField {
	if compat.ThinkingTokenBudgetField != nil {
		return compat.ThinkingTokenBudgetField
	}
	if compat.SupportsThinkingTokenBudget != nil && *compat.SupportsThinkingTokenBudget {
		value := types.ThinkingTokenBudgetVLLM
		return &value
	}
	return nil
}

func buildChatTemplateValues(model *types.Model, options *OpenAICompletionsOptions, values map[string]types.ChatTemplateKwargValue, thinkingBudget *int) map[string]any {
	resolved := map[string]any{}
	for key, value := range values {
		if result, ok := resolveChatTemplateKwargValue(model, options, value, thinkingBudget); ok {
			resolved[key] = result
		}
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

func resolveChatTemplateKwargValue(model *types.Model, options *OpenAICompletionsOptions, value types.ChatTemplateKwargValue, thinkingBudget *int) (any, bool) {
	var effort *string
	if options != nil {
		effort = options.ReasoningEffort
	}
	raw := value.Value()
	switch typed := raw.(type) {
	case types.ChatTemplateVarRef:
		if effort == nil && typed.OmitWhenOff != nil && *typed.OmitWhenOff {
			return nil, false
		}
		switch typed.Var {
		case types.ChatTemplateVarThinkingEnabled:
			return effort != nil, true
		case types.ChatTemplateVarThinkingBudget:
			if thinkingBudget == nil {
				return nil, true
			}
			return *thinkingBudget, true
		}
	}
	if raw != nil {
		switch raw.(type) {
		case string, float64, int, bool:
			return raw, true
		}
	}
	if mapped, present, supported := model.ThinkingLevelMap.Lookup(thinkingLevelOrOff(effort)); present {
		if !supported {
			return nil, false
		}
		return mapped, true
	}
	if effort != nil {
		return *effort, true
	}
	return raw, raw == nil
}

func thinkingLevelOrOff(effort *string) types.ModelThinkingLevel {
	if effort == nil {
		return types.ThinkingOff
	}
	return types.ModelThinkingLevel(*effort)
}

func completionsCacheControl(compat resolvedOpenAICompletionsCompat, cacheRetention types.CacheRetention) *openAICompatCacheControl {
	if compat.CacheControlFormat == nil || *compat.CacheControlFormat != "anthropic" || cacheRetention == types.CacheRetentionNone {
		return nil
	}
	control := openAICompatCacheControl{Type: "ephemeral"}
	if cacheRetention == types.CacheRetentionLong && compat.SupportsLongCacheRetention {
		ttl := "1h"
		control.TTL = &ttl
	}
	return &control
}

func applyAnthropicCacheControl(messages []map[string]any, params map[string]any, control openAICompatCacheControl) {
	addCacheControlToSystemPrompt(messages, control)
	if tools, ok := params["tools"].([]map[string]any); ok && len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = control.toMap()
	}
	addCacheControlToLastConversationMessage(messages, control)
}

func addCacheControlToSystemPrompt(messages []map[string]any, control openAICompatCacheControl) {
	for _, message := range messages {
		if role, _ := message["role"].(string); role == "system" || role == "developer" {
			addCacheControlToTextContent(message, control)
			return
		}
	}
}

func addCacheControlToLastConversationMessage(messages []map[string]any, control openAICompatCacheControl) {
	for i := len(messages) - 1; i >= 0; i-- {
		role, _ := messages[i]["role"].(string)
		if role == "user" || role == "assistant" || role == "tool" {
			if addCacheControlToTextContent(messages[i], control) {
				return
			}
		}
	}
}

func addCacheControlToTextContent(message map[string]any, control openAICompatCacheControl) bool {
	content := message["content"]
	switch value := content.(type) {
	case string:
		if value == "" {
			return false
		}
		message["content"] = []map[string]any{{"type": "text", "text": value, "cache_control": control.toMap()}}
		return true
	case []map[string]any:
		for i := len(value) - 1; i >= 0; i-- {
			if partType, _ := value[i]["type"].(string); partType == "text" {
				value[i]["cache_control"] = control.toMap()
				return true
			}
		}
	}
	return false
}

// ConvertMessages converts a transcript into Chat Completions messages.
func ConvertMessages(model *types.Model, context *types.TranscriptContext, compat resolvedOpenAICompletionsCompat, options *ConvertCompletionsMessagesOptions) []map[string]any {
	supportsMidConvo := compat.SupportsMidConvoSystemMessages
	normalizedContext := resolveTranscriptContext(context, supportsMidConvo)
	params := []map[string]any{}

	normalizeToolCallID := func(id string, _ *types.Model, _ types.AssistantMessage) string {
		if strings.Contains(id, "|") {
			separator := strings.Index(id, "|")
			callID := normalizeCompletionsID(id[:separator])
			itemID := normalizeCompletionsID(id[separator+1:])
			combined := callID
			if itemID != "" {
				combined = callID + "_" + itemID
			}
			if len(combined) <= 40 {
				return combined
			}
			hash := utils.ShortHash(id)
			if len(hash) > 8 {
				hash = hash[:8]
			}
			prefixLength := 40 - len(hash) - 1
			if prefixLength < 1 {
				prefixLength = 1
			}
			prefix := callID
			if len(prefix) > prefixLength {
				prefix = prefix[:prefixLength]
			}
			return prefix + "_" + hash
		}
		if model.Provider == types.ProviderOpenAI && len(id) > 40 {
			return id[:40]
		}
		return id
	}

	transformedMessages := TransformMessages(normalizedContext.Messages, model, normalizeToolCallID)
	supportsToolAdditions := compat.SupportsMidConvoSystemMessages && compat.SupportsMidConvoToolAdditions
	transcriptTools := utils.ResolveTranscriptTools(normalizedContext.Messages, supportsToolAdditions)
	instructionRole := "system"
	if model.Reasoning && compat.SupportsDeveloperRole {
		instructionRole = "developer"
	}

	lastRole := ""
	for i := 0; i < len(transformedMessages); i++ {
		message := transformedMessages[i]
		if compat.RequiresAssistantAfterToolResult && lastRole == "toolResult" && message.Role == types.UserMessageRole {
			params = append(params, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
		}

		switch message.Role {
		case types.SystemMessageRole:
			if message.System == nil {
				continue
			}
			addedTools := []types.Tool{}
			if i > 0 && transcriptTools.AnchorsAdditions {
				addedTools = message.System.ToolsAdded
			}
			if len(addedTools) > 0 {
				params = append(params, map[string]any{"role": "system", "tools": convertCompletionsTools(addedTools, compat)})
			}
			text := ""
			if i == 0 {
				text = utils.GetSystemMessageText(*message.System)
			} else {
				text = utils.RenderSystemMessageUpdate(*message.System)
			}
			if len(text) > 0 {
				params = append(params, map[string]any{"role": instructionRole, "content": utils.SanitizeSurrogates(text)})
			}
		case types.UserMessageRole:
			if message.User == nil {
				continue
			}
			if !message.User.Content.Structured {
				params = append(params, map[string]any{"role": "user", "content": utils.SanitizeSurrogates(message.User.Content.Text)})
				break
			}
			content := []map[string]any{}
			for _, block := range message.User.Content.Blocks {
				if block.Type == types.ContentTypeText && block.Text != nil {
					if block.Text.Text == "" {
						continue
					}
					content = append(content, map[string]any{"type": "text", "text": utils.SanitizeSurrogates(block.Text.Text)})
				} else if block.Type == types.ContentTypeImage && block.Image != nil {
					content = append(content, map[string]any{
						"type":      "image_url",
						"image_url": map[string]any{"url": "data:" + block.Image.MimeType + ";base64," + block.Image.Data},
					})
				}
			}
			if len(content) == 0 {
				continue
			}
			params = append(params, map[string]any{"role": "user", "content": content})
		case types.AssistantMessageRole:
			if message.Assistant == nil {
				continue
			}
			assistant := *message.Assistant
			converted := convertAssistantMessage(model, compat, assistant, options)
			if converted == nil {
				continue
			}
			params = append(params, converted)
		case types.ToolResultMessageRole:
			imageBlocks := []map[string]any{}
			j := i
			for ; j < len(transformedMessages) && transformedMessages[j].Role == types.ToolResultMessageRole; j++ {
				toolMessage := transformedMessages[j].ToolResult
				if toolMessage == nil {
					continue
				}
				textParts := []string{}
				hasImages := false
				for _, block := range toolMessage.Content {
					if block.Type == types.ContentTypeText && block.Text != nil {
						textParts = append(textParts, block.Text.Text)
					}
					if block.Type == types.ContentTypeImage {
						hasImages = true
					}
				}
				textResult := strings.Join(textParts, "\n")
				hasText := len(textResult) > 0
				toolText := textResult
				if !hasText {
					if hasImages {
						toolText = "(see attached image)"
					} else {
						toolText = "(no tool output)"
					}
				}
				toolResultMessage := map[string]any{
					"role":         "tool",
					"content":      utils.SanitizeSurrogates(toolText),
					"tool_call_id": toolMessage.ToolCallId,
				}
				if compat.RequiresToolResultName && toolMessage.ToolName != "" {
					toolResultMessage["name"] = toolMessage.ToolName
				}
				params = append(params, toolResultMessage)
				if hasImages && model.SupportsImageInput() {
					for _, block := range toolMessage.Content {
						if block.Type == types.ContentTypeImage && block.Image != nil {
							imageBlocks = append(imageBlocks, map[string]any{
								"type":      "image_url",
								"image_url": map[string]any{"url": "data:" + block.Image.MimeType + ";base64," + block.Image.Data},
							})
						}
					}
				}
			}
			i = j - 1
			if len(imageBlocks) > 0 {
				if compat.RequiresAssistantAfterToolResult {
					params = append(params, map[string]any{"role": "assistant", "content": "I have processed the tool results."})
				}
				content := []map[string]any{{"type": "text", "text": "Attached image(s) from tool result:"}}
				content = append(content, imageBlocks...)
				params = append(params, map[string]any{"role": "user", "content": content})
				lastRole = "user"
			} else {
				lastRole = "toolResult"
			}
			continue
		}
		lastRole = message.Role
	}
	return params
}

func normalizeCompletionsID(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			builder.WriteRune(r)
		} else {
			builder.WriteRune('_')
		}
	}
	return builder.String()
}

func convertAssistantMessage(model *types.Model, compat resolvedOpenAICompletionsCompat, assistant types.AssistantMessage, options *ConvertCompletionsMessagesOptions) map[string]any {
	assistantMessage := map[string]any{
		"role":    "assistant",
		"content": nil,
	}
	if compat.RequiresAssistantAfterToolResult {
		assistantMessage["content"] = ""
	}

	textParts := []string{}
	for _, block := range assistant.Content {
		if block.Type == types.ContentTypeText && block.Text != nil && strings.TrimSpace(block.Text.Text) != "" {
			textParts = append(textParts, utils.SanitizeSurrogates(block.Text.Text))
		}
	}
	assistantText := strings.Join(textParts, "")

	thinkingBlocks := []*types.ThinkingContent{}
	toolCalls := []*types.ToolCall{}
	for i := range assistant.Content {
		switch assistant.Content[i].Type {
		case types.ContentTypeThinking:
			thinkingBlocks = append(thinkingBlocks, assistant.Content[i].Thinking)
		case types.ContentTypeToolCall:
			toolCalls = append(toolCalls, assistant.Content[i].ToolCall)
		}
	}

	var preservedReasoningDetails any
	for _, block := range thinkingBlocks {
		if block == nil || block.ThinkingSignature == nil {
			continue
		}
		if details, ok := parseOpenAIReasoningDetails(*block.ThinkingSignature); ok {
			preservedReasoningDetails = details
			break
		}
	}
	if preservedReasoningDetails == nil {
		legacy := []any{}
		for _, toolCall := range toolCalls {
			if toolCall == nil || toolCall.ThoughtSignature == nil {
				continue
			}
			if detail, ok := parseLegacyEncryptedReasoningDetail(*toolCall.ThoughtSignature); ok {
				legacy = append(legacy, detail)
			}
		}
		if len(legacy) > 0 {
			preservedReasoningDetails = legacy
		}
	}

	nonEmptyThinking := []*types.ThinkingContent{}
	for _, block := range thinkingBlocks {
		if block != nil && strings.TrimSpace(block.Thinking) != "" {
			nonEmptyThinking = append(nonEmptyThinking, block)
		}
	}
	if len(nonEmptyThinking) > 0 {
		if compat.RequiresThinkingAsText {
			texts := []string{}
			for _, block := range nonEmptyThinking {
				texts = append(texts, utils.SanitizeSurrogates(block.Thinking))
			}
			content := []map[string]any{{"type": "text", "text": strings.Join(texts, "\n\n")}}
			for _, text := range textParts {
				content = append(content, map[string]any{"type": "text", "text": text})
			}
			assistantMessage["content"] = content
		} else {
			if assistantText != "" {
				assistantMessage["content"] = assistantText
			}
			if preservedReasoningDetails == nil {
				signature := ""
				if nonEmptyThinking[0].ThinkingSignature != nil {
					signature = *nonEmptyThinking[0].ThinkingSignature
				}
				if model.Provider == types.ProviderOpencodeGo && signature == "reasoning" {
					signature = "reasoning_content"
				}
				if isOpenAICompletionsReasoningField(signature) {
					texts := []string{}
					for _, block := range nonEmptyThinking {
						texts = append(texts, block.Thinking)
					}
					assistantMessage[signature] = strings.Join(texts, "\n")
				}
			}
		}
	} else if assistantText != "" {
		assistantMessage["content"] = assistantText
	}

	if len(toolCalls) > 0 {
		converted := []map[string]any{}
		for _, toolCall := range toolCalls {
			if toolCall == nil {
				continue
			}
			var customInputProperty *string
			if options != nil && options.GrammarToolInputProperties != nil {
				if property, present := options.GrammarToolInputProperties[toolCall.Name]; present {
					value := property
					customInputProperty = &value
				}
			}
			if customInputProperty != nil {
				input, err := GetGrammarToolInput(toolCall.Name, toolCallArguments(toolCall), *customInputProperty)
				if err != nil {
					input = ""
				}
				converted = append(converted, map[string]any{
					"id":   toolCall.Id,
					"type": "custom",
					"custom": map[string]any{
						"name":  toolCall.Name,
						"input": utils.SanitizeSurrogates(input),
					},
				})
			} else {
				converted = append(converted, map[string]any{
					"id":   toolCall.Id,
					"type": "function",
					"function": map[string]any{
						"name":      toolCall.Name,
						"arguments": string(toolCall.Arguments),
					},
				})
			}
		}
		assistantMessage["tool_calls"] = converted
	}
	if preservedReasoningDetails != nil {
		assistantMessage["reasoning_details"] = preservedReasoningDetails
	}
	if compat.RequiresReasoningContentOnAssistantMessages && model.Reasoning {
		if _, present := assistantMessage["reasoning_content"]; !present {
			assistantMessage["reasoning_content"] = ""
		}
	}
	content := assistantMessage["content"]
	hasContent := false
	switch value := content.(type) {
	case string:
		hasContent = value != ""
	case []map[string]any:
		hasContent = len(value) > 0
	}
	if !hasContent {
		if _, hasToolCalls := assistantMessage["tool_calls"]; !hasToolCalls {
			return nil
		}
	}
	return assistantMessage
}

const openAICompletionsReasoningFieldReasoning = "reasoning"

var openAICompletionsReasoningFields = []string{"reasoning", "reasoning_content", "reasoning_text"}

func isOpenAICompletionsReasoningField(field string) bool {
	for _, candidate := range openAICompletionsReasoningFields {
		if candidate == field {
			return true
		}
	}
	return false
}

func parseOpenAIReasoningDetails(signature string) ([]any, bool) {
	if signature == "" {
		return nil, false
	}
	var parsed []any
	if err := json.Unmarshal([]byte(signature), &parsed); err != nil {
		return nil, false
	}
	if len(parsed) == 0 {
		return nil, false
	}
	for _, detail := range parsed {
		if !isOpenAIReasoningDetail(detail) {
			return nil, false
		}
	}
	return parsed, true
}

func isOpenAIReasoningDetail(detail any) bool {
	object, ok := detail.(map[string]any)
	if !ok || object == nil {
		return false
	}
	if id, present := object["id"]; present && id != nil {
		if _, ok := id.(string); !ok {
			return false
		}
	}
	if format, present := object["format"]; present && format != nil {
		if _, ok := format.(string); !ok {
			return false
		}
	}
	detailType, _ := object["type"].(string)
	switch detailType {
	case "reasoning.summary":
		_, ok := object["summary"].(string)
		return ok
	case "reasoning.encrypted":
		_, ok := object["data"].(string)
		return ok
	case "reasoning.text":
		_, ok := object["text"].(string)
		if !ok {
			return false
		}
		if signature, present := object["signature"]; present && signature != nil {
			if _, ok := signature.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func parseLegacyEncryptedReasoningDetail(signature string) (any, bool) {
	if signature == "" {
		return nil, false
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(signature), &parsed); err != nil {
		return nil, false
	}
	if !isOpenAIReasoningDetail(parsed) {
		return nil, false
	}
	if detailType, _ := parsed["type"].(string); detailType != "reasoning.encrypted" {
		return nil, false
	}
	id, _ := parsed["id"].(string)
	data, _ := parsed["data"].(string)
	if id == "" || data == "" {
		return nil, false
	}
	return parsed, true
}

func convertCompletionsTools(tools []types.Tool, compat resolvedOpenAICompletionsCompat) []map[string]any {
	result := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		grammar, err := ResolveGrammarConstrainedSampling(tool, compat.SupportsOpenAIGrammarTools)
		if err == nil && grammar != nil {
			result = append(result, map[string]any{
				"type": "custom",
				"custom": map[string]any{
					"name":        tool.Name,
					"description": tool.Description,
					"format": map[string]any{
						"type": "grammar",
						"grammar": map[string]any{
							"syntax":     grammar.Format,
							"definition": grammar.Definition,
						},
					},
				},
			})
			continue
		}
		var strict *bool
		if constrained, strictErr := ResolveJSONSchemaStrictSampling(tool, compat.SupportsStrictMode); strictErr == nil && constrained != nil {
			strict = constrained
		}
		parameters, _ := GetJSONSchemaToolParameters(tool, strict)
		functionTool := map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  parameters,
			},
		}
		if compat.SupportsStrictMode {
			value := false
			if strict != nil {
				value = *strict
			}
			functionTool["function"].(map[string]any)["strict"] = value
		}
		result = append(result, functionTool)
	}
	return result
}

func createCompletionsGrammarProperties(context *types.TranscriptContext, supportsGrammarTools bool) map[string]string {
	if context == nil {
		return map[string]string{}
	}
	properties, err := CreateGrammarToolInputProperties(utils.GetDeclaredTools(context.Messages), supportsGrammarTools)
	if err != nil {
		return map[string]string{}
	}
	return properties
}

func buildCompletionsHeaders(model *types.Model, compat resolvedOpenAICompletionsCompat, sessionID *string, context *types.TranscriptContext, options *types.ProviderRequestOptions) types.ProviderHeaders {
	headers := types.ProviderHeaders{}
	if model.Provider == types.ProviderGitHubCopilot && context != nil {
		hasImages := HasCopilotVisionInput(context.Messages)
		for key, value := range BuildCopilotDynamicHeaders(context.Messages, hasImages) {
			text := value
			headers[key] = &text
		}
	}
	if sessionID != nil && compat.SendSessionAffinityHeaders {
		if compat.SessionAffinityFormat == "openrouter" {
			value := *sessionID
			headers["x-session-id"] = &value
		} else {
			if compat.SessionAffinityFormat == "openai" {
				value := *sessionID
				headers["session_id"] = &value
			}
			value := *sessionID
			headers["x-client-request-id"] = &value
			headers["x-session-affinity"] = &value
		}
	}
	return headers
}

// completionsToolCallBlock tracks an in-flight chat-completions tool call,
// including the streaming scratch buffers that must never be persisted.
type completionsToolCallBlock struct {
	tool         *types.ToolCall
	partialArgs  *string
	customInput  *customToolInput
	contentIndex int
	streamIndex  *int
}

func completionsCustomInput(block *completionsToolCallBlock) string {
	if block == nil || block.tool == nil || block.customInput == nil {
		return ""
	}
	arguments := toolCallArguments(block.tool)
	value, _ := arguments[block.customInput.property].(string)
	return value
}

func appendCompletionsCustomInput(block *completionsToolCallBlock, nextInput string, close bool) string {
	if block == nil || block.customInput == nil {
		return ""
	}
	delta, err := AppendGrammarToolInputJSONDelta(&block.customInput.jsonBuffer, block.customInput.property, nextInput, close)
	if err != nil {
		return ""
	}
	block.tool.Arguments = jsonRaw(map[string]any{block.customInput.property: nextInput})
	if delta == nil {
		return ""
	}
	return *delta
}

func finalizeCompletionsToolCall(block *completionsToolCallBlock, output *types.AssistantMessage, stream *types.AssistantMessageEventStream) {
	if block == nil || block.tool == nil {
		return
	}
	if block.customInput != nil {
		appendCompletionsCustomInput(block, completionsCustomInput(block), true)
	} else if block.partialArgs != nil {
		block.tool.Arguments = jsonRaw(utils.ParseStreamingJSON(*block.partialArgs))
	}
	block.partialArgs = nil
	block.customInput = nil
	block.streamIndex = nil
	if block.contentIndex >= 0 && block.contentIndex < len(output.Content) {
		output.Content[block.contentIndex].ToolCall = block.tool
	}
	stream.Push(types.NewToolCallEndEvent(block.contentIndex, *block.tool, *output))
}

// processOpenAICompletionsEvents consumes chat completion chunks and drives the
// assistant message stream. It returns only fatal protocol errors; provider
// finish reasons are encoded in the assistant message.
func processOpenAICompletionsEvents(next func() (map[string]any, bool, error), output *types.AssistantMessage, stream *types.AssistantMessageEventStream, model *types.Model, compat resolvedOpenAICompletionsCompat, grammarProperties map[string]string) error {
	textBlock := (*types.TextContent)(nil)
	textIndex := -1
	thinkingBlock := (*types.ThinkingContent)(nil)
	thinkingIndex := -1
	var reasoningDetails []any
	toolCallsByIndex := map[int]*completionsToolCallBlock{}
	toolCallsByID := map[string]*completionsToolCallBlock{}
	orderedToolBlocks := []*completionsToolCallBlock{}
	hasFinishReason := false

	ensureTextBlock := func() *types.TextContent {
		if textBlock == nil {
			output.Content = append(output.Content, types.TextBlock(""))
			textBlock = output.Content[len(output.Content)-1].Text
			textIndex = len(output.Content) - 1
			stream.Push(types.NewTextStartEvent(textIndex, *output))
		}
		return textBlock
	}
	ensureThinkingBlock := func(signature string) *types.ThinkingContent {
		if thinkingBlock == nil {
			block := types.NewThinkingContent("")
			block.ThinkingSignature = &signature
			output.Content = append(output.Content, types.ContentBlock{Type: types.ContentTypeThinking, Thinking: &block})
			thinkingBlock = &block
			thinkingIndex = len(output.Content) - 1
			stream.Push(types.NewThinkingStartEvent(thinkingIndex, *output))
		}
		return thinkingBlock
	}
	ensureToolCallBlock := func(chunk map[string]any) *completionsToolCallBlock {
		streamIndex := -1
		if index, ok := intValue(chunk["index"]); ok {
			streamIndex = index
		}
		name := ""
		if function := jsonObject(chunk["function"]); function != nil {
			name, _ = stringValue(function["name"])
		}
		if custom := jsonObject(chunk["custom"]); custom != nil && name == "" {
			name, _ = stringValue(custom["name"])
		}
		var block *completionsToolCallBlock
		if streamIndex >= 0 {
			block = toolCallsByIndex[streamIndex]
		}
		id, _ := stringValue(chunk["id"])
		if block == nil && id != "" {
			block = toolCallsByID[id]
		}
		if block == nil {
			hasCustomInput := false
			customInputProperty := ""
			if custom := jsonObject(chunk["custom"]); custom != nil && jsonObject(chunk["function"]) == nil {
				hasCustomInput = true
				customInputProperty = "input"
				if property, present := grammarProperties[name]; present {
					customInputProperty = property
				}
			}
			tool := &types.ToolCall{Type: types.ContentTypeToolCall, Id: id, Name: name, Arguments: json.RawMessage("{}")}
			block = &completionsToolCallBlock{tool: tool}
			if hasCustomInput {
				block.customInput = &customToolInput{property: customInputProperty, jsonBuffer: GrammarToolInputJSONBuffer{}}
			} else {
				empty := ""
				block.partialArgs = &empty
			}
			if streamIndex >= 0 {
				indexCopy := streamIndex
				block.streamIndex = &indexCopy
				toolCallsByIndex[streamIndex] = block
			}
			if id != "" {
				toolCallsByID[id] = block
			}
			output.Content = append(output.Content, types.ToolCallBlock(*tool))
			block.contentIndex = len(output.Content) - 1
			output.Content[block.contentIndex].ToolCall = tool
			orderedToolBlocks = append(orderedToolBlocks, block)
			stream.Push(types.NewToolCallStartEvent(block.contentIndex, *output))
		}
		if streamIndex >= 0 && block.streamIndex == nil {
			indexCopy := streamIndex
			block.streamIndex = &indexCopy
			toolCallsByIndex[streamIndex] = block
		}
		if id != "" {
			toolCallsByID[id] = block
			if block.tool.Id == "" {
				block.tool.Id = id
			}
		}
		if block.tool.Name == "" && name != "" {
			block.tool.Name = name
		}
		return block
	}

	for {
		chunk, ok, err := next()
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if id, ok := stringValue(chunk["id"]); ok && output.ResponseId == nil {
			output.ResponseId = &id
		}
		if chunkModel, ok := stringValue(chunk["model"]); ok && chunkModel != "" && chunkModel != model.Id {
			if output.ResponseModel == nil {
				output.ResponseModel = &chunkModel
			}
		}
		if usage := jsonObject(chunk["usage"]); usage != nil {
			output.Usage = parseChunkUsage(usage, model)
		}
		choices, _ := chunk["choices"].([]any)
		if len(choices) == 0 {
			continue
		}
		choice := jsonObject(choices[0])
		if choice == nil {
			continue
		}
		if usage := jsonObject(choice["usage"]); usage != nil && jsonObject(chunk["usage"]) == nil {
			output.Usage = parseChunkUsage(usage, model)
		}
		if finishReason, ok := stringValue(choice["finish_reason"]); ok && finishReason != "" {
			output.RawStopReason = &finishReason
			reason, errorMessage := mapCompletionsStopReason(finishReason)
			output.StopReason = reason
			if errorMessage != nil {
				output.ErrorMessage = errorMessage
			}
			hasFinishReason = true
		}
		delta := jsonObject(choice["delta"])
		if delta == nil {
			continue
		}
		if content, ok := stringValue(delta["content"]); ok && content != "" {
			block := ensureTextBlock()
			block.Text += content
			stream.Push(types.NewTextDeltaEvent(textIndex, content, *output))
		}
		reasoningField := ""
		for _, field := range openAICompletionsReasoningFields {
			if value, ok := stringValue(delta[field]); ok && value != "" {
				reasoningField = field
				break
			}
		}
		if reasoningField != "" {
			signature := reasoningField
			if model.Provider == types.ProviderOpencodeGo && reasoningField == "reasoning" {
				signature = "reasoning_content"
			}
			block := ensureThinkingBlock(signature)
			value, _ := stringValue(delta[reasoningField])
			block.Thinking += value
			stream.Push(types.NewThinkingDeltaEvent(thinkingIndex, value, *output))
		}
		if toolCallDeltas, ok := delta["tool_calls"].([]any); ok {
			for _, entry := range toolCallDeltas {
				toolCallDelta := jsonObject(entry)
				if toolCallDelta == nil {
					continue
				}
				block := ensureToolCallBlock(toolCallDelta)
				deltaText := ""
				if function := jsonObject(toolCallDelta["function"]); function != nil {
					if arguments, ok := stringValue(function["arguments"]); ok {
						deltaText = arguments
						if block.partialArgs == nil {
							empty := ""
							block.partialArgs = &empty
						}
						*block.partialArgs += arguments
						block.tool.Arguments = jsonRaw(utils.ParseStreamingJSON(*block.partialArgs))
					}
				} else if custom := jsonObject(toolCallDelta["custom"]); custom != nil {
					if input, ok := stringValue(custom["input"]); ok {
						nextInput := completionsCustomInput(block) + input
						deltaText = appendCompletionsCustomInput(block, nextInput, false)
					}
				}
				if block.contentIndex >= 0 && block.contentIndex < len(output.Content) {
					output.Content[block.contentIndex].ToolCall = block.tool
				}
				stream.Push(types.NewToolCallDeltaEvent(block.contentIndex, deltaText, *output))
			}
		}
		if details, ok := delta["reasoning_details"].([]any); ok {
			for _, detail := range details {
				if !isOpenAIReasoningDetail(detail) {
					continue
				}
				ensureThinkingBlock("")
				reasoningDetails = appendOpenAIReasoningDetail(reasoningDetails, detail)
			}
		}
	}

	if textBlock != nil {
		stream.Push(types.NewTextEndEvent(textIndex, textBlock.Text, *output))
	}
	if thinkingBlock != nil {
		if reasoningDetails != nil {
			encoded, _ := json.Marshal(reasoningDetails)
			text := string(encoded)
			thinkingBlock.ThinkingSignature = &text
		}
		stream.Push(types.NewThinkingEndEvent(thinkingIndex, thinkingBlock.Thinking, *output))
	}
	for _, block := range orderedToolBlocks {
		finalizeCompletionsToolCall(block, output, stream)
	}

	if !hasFinishReason && !compat.SupportsFinishReason {
		hasToolCall := false
		for _, block := range output.Content {
			if block.Type == types.ContentTypeToolCall {
				hasToolCall = true
				break
			}
		}
		if hasToolCall {
			output.StopReason = types.StopReasonToolUse
		} else {
			output.StopReason = types.StopReasonStop
		}
	}
	if (compat.SupportsFinishReason && !hasFinishReason) || output.StopReason == types.StopReasonPending {
		return fmt.Errorf("Stream ended without finish_reason")
	}
	return nil
}

func appendOpenAIReasoningDetail(details []any, detail any) []any {
	object := jsonObject(detail)
	if object == nil {
		return append(details, detail)
	}
	detailType, _ := stringValue(object["type"])
	if len(details) > 0 {
		last := jsonObject(details[len(details)-1])
		if last != nil {
			lastType, _ := stringValue(last["type"])
			if detailType == "reasoning.text" && lastType == "reasoning.text" {
				text, _ := stringValue(object["text"])
				existing, _ := stringValue(last["text"])
				last["text"] = existing + text
				if _, present := last["signature"]; !present {
					if signature, ok := object["signature"]; ok {
						last["signature"] = signature
					}
				}
				fillMissingReasoningDetailFields(last, object)
				return details
			}
			if detailType == "reasoning.summary" && lastType == "reasoning.summary" {
				summary, _ := stringValue(object["summary"])
				existing, _ := stringValue(last["summary"])
				last["summary"] = existing + summary
				fillMissingReasoningDetailFields(last, object)
				return details
			}
		}
	}
	copyObject := map[string]any{}
	for key, value := range object {
		copyObject[key] = value
	}
	return append(details, copyObject)
}

func fillMissingReasoningDetailFields(target, source map[string]any) {
	if _, present := target["id"]; !present {
		if value, ok := source["id"]; ok {
			target["id"] = value
		}
	}
	if _, present := target["format"]; !present {
		if value, ok := source["format"]; ok {
			target["format"] = value
		}
	}
	if _, present := target["index"]; !present {
		if value, ok := source["index"]; ok {
			target["index"] = value
		}
	}
}

func parseChunkUsage(rawUsage map[string]any, model *types.Model) types.Usage {
	promptTokens, _ := floatValue(rawUsage["prompt_tokens"])
	completionTokens, _ := floatValue(rawUsage["completion_tokens"])
	cacheRead := 0.0
	if details := jsonObject(rawUsage["prompt_tokens_details"]); details != nil {
		if value, ok := floatValue(details["cached_tokens"]); ok {
			cacheRead = value
		}
	}
	if value, ok := floatValue(rawUsage["prompt_cache_hit_tokens"]); ok {
		cacheRead = value
	}
	if value, ok := floatValue(rawUsage["cached_tokens"]); ok {
		cacheRead = value
	}
	cacheWrite := 0.0
	if details := jsonObject(rawUsage["prompt_tokens_details"]); details != nil {
		if value, ok := floatValue(details["cache_write_tokens"]); ok {
			cacheWrite = value
		}
	}
	reasoning := 0.0
	if details := jsonObject(rawUsage["completion_tokens_details"]); details != nil {
		if value, ok := floatValue(details["reasoning_tokens"]); ok {
			reasoning = value
		}
	}
	input := promptTokens - cacheRead - cacheWrite
	if input < 0 {
		input = 0
	}
	usage := types.Usage{
		Input:       input,
		Output:      completionTokens,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		Reasoning:   &reasoning,
		TotalTokens: input + completionTokens + cacheRead + cacheWrite,
	}
	calculateCost(model, &usage)
	return usage
}

func mapCompletionsStopReason(reason string) (types.StopReason, *string) {
	switch reason {
	case "stop", "end":
		return types.StopReasonStop, nil
	case "length":
		return types.StopReasonLength, nil
	case "function_call", "tool_calls":
		return types.StopReasonToolUse, nil
	case "content_filter":
		message := "Provider finish_reason: content_filter"
		return types.StopReasonError, &message
	case "network_error":
		message := "Provider finish_reason: network_error"
		return types.StopReasonError, &message
	default:
		message := "Provider finish_reason: " + reason
		return types.StopReasonError, &message
	}
}

func terminateCompletionsStream(stream *types.AssistantMessageEventStream, output *types.AssistantMessage, err error, wasAborted bool) {
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

func completionsOptionAPIKey(options *OpenAICompletionsOptions) *string {
	if options == nil {
		return nil
	}
	return options.APIKey
}

func completionsOptionHeaders(options *OpenAICompletionsOptions) types.ProviderHeaders {
	if options == nil {
		return nil
	}
	return options.Headers
}

func completionsOptionCacheRetention(options *OpenAICompletionsOptions) *types.CacheRetention {
	if options == nil {
		return nil
	}
	return options.CacheRetention
}

func completionsOptionEnv(options *OpenAICompletionsOptions) types.ProviderEnv {
	if options == nil {
		return nil
	}
	return options.Env
}

func completionsOptionSessionID(options *OpenAICompletionsOptions) *string {
	if options == nil {
		return nil
	}
	return options.SessionId
}

func completionsOptionSignal(options *OpenAICompletionsOptions) <-chan struct{} {
	if options == nil {
		return nil
	}
	return options.Signal
}

var _ = context.Background
var _ = openAICompletionsReasoningFieldReasoning
