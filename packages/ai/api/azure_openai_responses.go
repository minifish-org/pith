// This file is a Go port of packages/ai/src/api/azure-openai-responses.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

const defaultAzureAPIVersion = "v1"

// azureToolCallProviders are the providers whose Responses item ids Azure keeps.
var azureToolCallProviders = map[string]bool{
	string(types.ProviderOpenAI):               true,
	string(types.ProviderOpenAICodex):          true,
	string(types.ProviderOpencode):             true,
	string(types.ProviderAzureOpenAIResponses): true,
}

// AzureOpenAIResponsesOptions are the Azure OpenAI Responses stream options.
type AzureOpenAIResponsesOptions struct {
	types.StreamOptions
	ReasoningEffort     *string
	ToolChoice          any
	ReasoningSummary    *string
	HasReasoningSummary bool
	AzureAPIVersion     *string
	AzureResourceName   *string
	AzureBaseURL        *string
	AzureDeploymentName *string
}

func parseDeploymentNameMap(value *string) map[string]string {
	result := map[string]string{}
	if value == nil {
		return result
	}
	for _, entry := range strings.Split(*value, ",") {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		result[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return result
}

func resolveAzureDeploymentName(model *types.Model, options *AzureOpenAIResponsesOptions) string {
	if options != nil && options.AzureDeploymentName != nil {
		return *options.AzureDeploymentName
	}
	mapped := parseDeploymentNameMap(utils.GetProviderEnvValue("AZURE_OPENAI_DEPLOYMENT_NAME_MAP", azureOptionEnv(options)))
	if deployment, ok := mapped[model.Id]; ok && deployment != "" {
		return deployment
	}
	return model.Id
}

func normalizeAzureBaseURL(baseURL string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("Invalid Azure OpenAI base URL: %s", baseURL)
	}
	host := strings.ToLower(parsed.Hostname())
	isAzureHost := strings.HasSuffix(host, ".openai.azure.com") ||
		strings.HasSuffix(host, ".cognitiveservices.azure.com") ||
		strings.HasSuffix(host, ".ai.azure.com")
	normalizedPath := strings.TrimRight(parsed.Path, "/")
	if isAzureHost && (normalizedPath == "" || normalizedPath == "/openai" || normalizedPath == "/openai/v1/responses") {
		parsed.Path = "/openai/v1"
		parsed.RawQuery = ""
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func buildDefaultAzureBaseURL(resourceName string) string {
	return "https://" + resourceName + ".openai.azure.com/openai/v1"
}

func resolveAzureConfig(model *types.Model, options *AzureOpenAIResponsesOptions) (string, string, error) {
	apiVersion := defaultAzureAPIVersion
	if options != nil && options.AzureAPIVersion != nil && *options.AzureAPIVersion != "" {
		apiVersion = *options.AzureAPIVersion
	} else if value := utils.GetProviderEnvValue("AZURE_OPENAI_API_VERSION", azureOptionEnv(options)); value != nil && *value != "" {
		apiVersion = *value
	}

	var baseURL *string
	if options != nil && options.AzureBaseURL != nil && strings.TrimSpace(*options.AzureBaseURL) != "" {
		value := strings.TrimSpace(*options.AzureBaseURL)
		baseURL = &value
	} else if value := utils.GetProviderEnvValue("AZURE_OPENAI_BASE_URL", azureOptionEnv(options)); value != nil && strings.TrimSpace(*value) != "" {
		trimmed := strings.TrimSpace(*value)
		baseURL = &trimmed
	}

	var resourceName *string
	if options != nil && options.AzureResourceName != nil {
		resourceName = options.AzureResourceName
	} else if value := utils.GetProviderEnvValue("AZURE_OPENAI_RESOURCE_NAME", azureOptionEnv(options)); value != nil {
		resourceName = value
	}

	resolved := baseURL
	if resolved == nil && resourceName != nil {
		value := buildDefaultAzureBaseURL(*resourceName)
		resolved = &value
	}
	if resolved == nil && model.BaseUrl != "" {
		value := model.BaseUrl
		resolved = &value
	}
	if resolved == nil {
		return "", "", fmt.Errorf("Azure OpenAI base URL is required. Set AZURE_OPENAI_BASE_URL or AZURE_OPENAI_RESOURCE_NAME, or pass azureBaseUrl, azureResourceName, or model.baseUrl.")
	}
	normalized, err := normalizeAzureBaseURL(*resolved)
	if err != nil {
		return "", "", err
	}
	return normalized, apiVersion, nil
}

// AzureOpenAIResponsesStream is the streaming entry point for the Azure OpenAI
// Responses API.
func AzureOpenAIResponsesStream(model *types.Model, context *types.TranscriptContext, options *AzureOpenAIResponsesOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	supportsMidConvo := false
	if raw := responsesCompat(model); raw != nil && raw.SupportsMidConvoSystemMessages != nil {
		supportsMidConvo = *raw.SupportsMidConvoSystemMessages
	}
	normalizedContext := resolveTranscriptContext(context, supportsMidConvo)

	go func() {
		output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
		output.StopReason = types.StopReasonPending

		deploymentName := resolveAzureDeploymentName(model, options)
		apiKey := (*string)(nil)
		if options != nil {
			apiKey = options.APIKey
		}
		if apiKey == nil || *apiKey == "" {
			terminateAzureStream(stream, &output, model, fmt.Errorf("No API key for provider: %s", model.Provider), false)
			return
		}

		baseURL, apiVersion, err := resolveAzureConfig(model, options)
		if err != nil {
			terminateAzureStream(stream, &output, model, err, false)
			return
		}
		grammarProperties := createGrammarToolInputPropertiesForContext(normalizedContext, azureSupportsGrammarTools(model))
		params := buildAzureParams(model, normalizedContext, options, deploymentName, grammarProperties)

		var reqOptions *types.ProviderRequestOptions
		if options != nil {
			reqOptions = &options.ProviderRequestOptions
			if options.OnPayload != nil {
				if next, payloadErr := options.OnPayload(params, model); payloadErr != nil {
					terminateAzureStream(stream, &output, model, payloadErr, false)
					return
				} else if nextMap, ok := next.(map[string]any); ok {
					params = nextMap
				}
			}
		}

		headers := applyProviderHeaders(model, nil, reqOptions)
		headers["content-type"] = "application/json"
		headers["api-key"] = *apiKey

		body, marshalErr := json.Marshal(params)
		if marshalErr != nil {
			terminateAzureStream(stream, &output, model, marshalErr, false)
			return
		}

		requestContext, cancel := contextForSignal(contextBackground(), azureOptionSignal(options))
		defer cancel()

		endpoint := strings.TrimRight(baseURL, "/") + "/responses?api-version=" + url.QueryEscape(apiVersion)
		response, _, requestErr := performRequestWithRetry(requestContext, reqOptions, http.MethodPost, endpoint, headers, body)
		if requestErr != nil {
			terminateAzureStream(stream, &output, model, requestErr, aborted(azureOptionSignal(options)))
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
			errQueue <- readSSE(requestContext, response.Body, azureOptionSignal(options), func(event map[string]any) error {
				eventQueue <- event
				return nil
			})
		}()
		next := func() (map[string]any, bool, error) {
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
		}

		if err := ProcessResponsesStream(next, &output, stream, model, &OpenAIResponsesStreamOptions{
			GrammarToolInputProperties: grammarProperties,
		}); err != nil {
			terminateAzureStream(stream, &output, model, err, aborted(azureOptionSignal(options)))
			return
		}

		if aborted(azureOptionSignal(options)) {
			terminateAzureStream(stream, &output, model, fmt.Errorf("Request was aborted"), true)
			return
		}
		if output.StopReason == types.StopReasonPending {
			terminateAzureStream(stream, &output, model, fmt.Errorf("Azure OpenAI Responses stream ended without a stop reason"), false)
			return
		}
		if output.StopReason == types.StopReasonAborted || output.StopReason == types.StopReasonError {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			terminateAzureStream(stream, &output, model, fmt.Errorf("%s", message), output.StopReason == types.StopReasonAborted)
			return
		}

		stream.Push(types.NewDoneEvent(output.StopReason, output))
		stream.End(&output)
	}()

	return stream
}

// AzureOpenAIResponsesStreamSimple is the unified-reasoning entry point for the
// Azure OpenAI Responses API.
func AzureOpenAIResponsesStreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	apiKey := (*string)(nil)
	if options != nil {
		apiKey = options.APIKey
	}
	if apiKey == nil || *apiKey == "" {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
			terminateAzureStream(stream, &output, model, fmt.Errorf("No API key for provider: %s", model.Provider), false)
		}()
		return stream
	}
	base := BuildBaseOptions(model, context, options, apiKey)
	typed := &AzureOpenAIResponsesOptions{StreamOptions: base}
	if options != nil {
		typed.ToolChoice = options.ToolChoice
		if options.Reasoning != nil {
			clamped := clampThinkingLevel(model, *options.Reasoning)
			if clamped != types.ThinkingOff {
				effort := string(clamped)
				typed.ReasoningEffort = &effort
			}
		}
	}
	return AzureOpenAIResponsesStream(model, context, typed)
}

func buildAzureParams(model *types.Model, context *types.TranscriptContext, options *AzureOpenAIResponsesOptions, deploymentName string, grammarProperties map[string]string) map[string]any {
	supportsAdditionalTools := false
	supportsToolSearch := false
	supportsStrict := true
	supportsGrammar := false
	if raw := responsesCompat(model); raw != nil {
		if raw.SupportsAdditionalTools != nil {
			supportsAdditionalTools = *raw.SupportsAdditionalTools
		}
		if raw.SupportsToolSearch != nil {
			supportsToolSearch = *raw.SupportsToolSearch
		}
		if raw.SupportsStrictMode != nil {
			supportsStrict = *raw.SupportsStrictMode
		}
		if raw.SupportsOpenAIGrammarTools != nil {
			supportsGrammar = *raw.SupportsOpenAIGrammarTools
		}
	}
	transcriptTools := utils.ResolveTranscriptTools(context.Messages, supportsAdditionalTools || supportsToolSearch)
	toolOptions := &ConvertResponsesToolsOptions{SupportsStrictMode: &supportsStrict, SupportsOpenAIGrammarTools: &supportsGrammar}
	includeSystemPrompt := true
	messages := ConvertResponsesMessages(model, context, azureToolCallProviders, &ConvertResponsesMessagesOptions{
		IncludeSystemPrompt:            &includeSystemPrompt,
		GrammarToolInputProperties:     grammarProperties,
		SupportsMidConvoSystemMessages: azureSupportsMidConvo(model),
		SupportsAdditionalTools:        supportsAdditionalTools,
		SupportsToolSearch:             supportsToolSearch,
		ToolOptions:                    toolOptions,
	})

	params := map[string]any{
		"model":  deploymentName,
		"input":  messages,
		"stream": true,
		"store":  false,
	}
	if options != nil {
		if clamped := ClampOpenAIPromptCacheKey(options.SessionId); clamped != nil {
			params["prompt_cache_key"] = *clamped
		}
		if options.MaxTokens != nil {
			value := *options.MaxTokens
			if value < openAIResponsesMinOutputTokens {
				value = openAIResponsesMinOutputTokens
			}
			params["max_output_tokens"] = value
		}
		if options.Temperature != nil {
			params["temperature"] = *options.Temperature
		}
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
	}
	if options != nil {
		for key, value := range options.SamplingParams {
			params[key] = value
		}
	}
	return params
}

func azureSupportsMidConvo(model *types.Model) bool {
	if raw := responsesCompat(model); raw != nil && raw.SupportsMidConvoSystemMessages != nil {
		return *raw.SupportsMidConvoSystemMessages
	}
	return false
}

func azureSupportsGrammarTools(model *types.Model) bool {
	if raw := responsesCompat(model); raw != nil && raw.SupportsOpenAIGrammarTools != nil {
		return *raw.SupportsOpenAIGrammarTools
	}
	return false
}

func terminateAzureStream(stream *types.AssistantMessageEventStream, output *types.AssistantMessage, model *types.Model, err error, wasAborted bool) {
	cleanupStreamingBlocks(output)
	if wasAborted {
		output.StopReason = types.StopReasonAborted
	} else {
		output.StopReason = types.StopReasonError
	}
	prefix := "Azure OpenAI API error"
	message := fmtProviderError(err, &prefix)
	output.ErrorMessage = &message
	stream.Push(types.NewErrorEvent(output.StopReason, *output))
	stream.End(output)
}

func azureOptionEnv(options *AzureOpenAIResponsesOptions) types.ProviderEnv {
	if options == nil {
		return nil
	}
	return options.Env
}

func azureOptionSignal(options *AzureOpenAIResponsesOptions) <-chan struct{} {
	if options == nil {
		return nil
	}
	return options.Signal
}
