// This file is a Go port of packages/ai/src/api/google-generative-ai.ts from Pi
// at revision f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// GoogleToolChoice is the Google tool-selection hint.
type GoogleToolChoice string

// Google tool choices.
const (
	GoogleToolChoiceAuto GoogleToolChoice = "auto"
	GoogleToolChoiceNone GoogleToolChoice = "none"
	GoogleToolChoiceAny  GoogleToolChoice = "any"
)

// GoogleThinkingOptions controls Gemini thinking for a request. Enabled is
// required upstream; BudgetTokens is -1 for dynamic and 0 to disable.
type GoogleThinkingOptions struct {
	Enabled      bool
	BudgetTokens *int
	Level        *GoogleApiThinkingLevel
}

// GoogleOptions are the Google Generative AI stream options.
type GoogleOptions struct {
	types.StreamOptions
	ToolChoice *GoogleToolChoice
	Thinking   *GoogleThinkingOptions
}

// GoogleGenerativeAIStream is the Google Generative AI streaming entry point.
func GoogleGenerativeAIStream(model *types.Model, context *types.TranscriptContext, options *GoogleOptions) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	normalizedContext := utils.CollapseSystemMessages(*context)

	go func() {
		output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
		output.StopReason = types.StopReasonPending

		var signal <-chan struct{}
		if options != nil {
			signal = options.Signal
		}
		fail := func(err error) { failGoogle(stream, &output, err, aborted(signal)) }

		if options != nil && options.Fetch != nil {
			fail(fmt.Errorf("Custom fetch is not supported by the Google Generative AI adapter"))
			return
		}
		if options == nil || options.APIKey == nil || *options.APIKey == "" {
			fail(fmt.Errorf("No API key for provider: %s", model.Provider))
			return
		}

		buildInput := googleBuildInput{Temperature: options.Temperature, MaxTokens: options.MaxTokens, Signal: signal, Thinking: options.Thinking}
		if options.ToolChoice != nil {
			choice := string(*options.ToolChoice)
			buildInput.ToolChoice = &choice
		}

		request, err := buildGoogleGenerateRequest(model, &normalizedContext, buildInput)
		if err != nil {
			fail(err)
			return
		}

		if options.OnPayload != nil {
			next, payloadErr := options.OnPayload(request, model)
			if payloadErr != nil {
				fail(payloadErr)
				return
			}
			if next != nil {
				switch replacement := next.(type) {
				case *GoogleGenerateContentRequest:
					request = replacement
				case GoogleGenerateContentRequest:
					request = &replacement
				default:
					fail(fmt.Errorf("Unsupported Google onPayload replacement type %T", next))
					return
				}
			}
		}

		requestContext, cancel := contextForSignal(contextBackground(), signal)
		defer cancel()

		headers := googleRequestHeaders(model, options.Headers, options.APIKey)
		body, err := json.Marshal(request)
		if err != nil {
			fail(err)
			return
		}

		var requestOptions *types.ProviderRequestOptions
		if options != nil {
			requestOptions = &options.ProviderRequestOptions
		}
		response, err := googlePerformRequest(requestContext, requestOptions, http.MethodPost, googleGenerativeAIURL(model), headers, body)
		if err != nil {
			fail(err)
			return
		}
		if response == nil {
			fail(fmt.Errorf("Google Generative AI request returned no response"))
			return
		}
		defer response.Body.Close()

		if options.OnResponse != nil {
			options.OnResponse(types.ProviderResponse{Status: response.StatusCode, Headers: utils.HeadersToRecord(response.Header)}, model)
		}

		stream.Push(types.NewStartEvent(output))
		if err := consumeGoogleStream(requestContext, model, stream, &output, response.Body, signal, googleOptionObserver(options)); err != nil {
			fail(err)
			return
		}
		finalizeGoogleStream(stream, &output, signal, "Google stream ended without a finish reason")
	}()

	return stream
}

// GoogleGenerativeAIStreamSimple is the unified-reasoning entry point for the
// Google Generative AI API.
func GoogleGenerativeAIStreamSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	if options == nil || options.APIKey == nil || *options.APIKey == "" {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
			failGoogle(stream, &output, fmt.Errorf("No API key for provider: %s", model.Provider), false)
		}()
		return stream
	}

	base := BuildBaseOptions(model, context, options, options.APIKey)
	typed := &GoogleOptions{StreamOptions: base}
	if options.ToolChoice != nil {
		choice := GoogleToolChoice(*options.ToolChoice)
		typed.ToolChoice = &choice
	}

	if options.Reasoning == nil {
		typed.Thinking = &GoogleThinkingOptions{Enabled: false}
		return GoogleGenerativeAIStream(model, context, typed)
	}

	clampedReasoning := clampThinkingLevel(model, *options.Reasoning)
	if clampedReasoning == types.ThinkingOff {
		typed.Thinking = &GoogleThinkingOptions{Enabled: false}
		return GoogleGenerativeAIStream(model, context, typed)
	}

	resolvedLevel, err := ResolveGoogleThinkingLevel(model, clampedReasoning)
	if err != nil {
		stream := types.NewAssistantMessageEventStream()
		go func() {
			output := types.NewAssistantMessage(model.Api, model.Provider, model.Id, nowMillis())
			failGoogle(stream, &output, err, false)
		}()
		return stream
	}

	if UsesGoogleThinkingLevel(model) {
		level := ToGoogleThinkingLevel(resolvedLevel)
		typed.Thinking = &GoogleThinkingOptions{Enabled: true, Level: &level}
		return GoogleGenerativeAIStream(model, context, typed)
	}

	budget := googleGenerativeAIBudget(model, resolvedLevel, options.ThinkingBudgets)
	typed.Thinking = &GoogleThinkingOptions{Enabled: true, BudgetTokens: &budget}
	return GoogleGenerativeAIStream(model, context, typed)
}

func googleGenerativeAIURL(model *types.Model) string {
	base := strings.TrimRight(model.BaseUrl, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	return base + "/models/" + model.Id + ":streamGenerateContent?alt=sse"
}

// googleRequestHeaders merges the default User-Agent, model headers and caller
// headers, then attaches the API key header. A nil caller header removes a
// lower-priority header with the same name.
func googleRequestHeaders(model *types.Model, optionsHeaders types.ProviderHeaders, apiKey *string) map[string]string {
	headers := map[string]string{"User-Agent": utils.GetPiUserAgent()}
	for key, value := range model.Headers {
		headers[key] = value
	}
	for key, value := range optionsHeaders {
		if value == nil {
			delete(headers, key)
			continue
		}
		headers[key] = *value
	}
	if apiKey != nil && *apiKey != "" {
		headers["x-goog-api-key"] = *apiKey
	}
	headers["content-type"] = "application/json"
	return headers
}

func googleGenerativeAIBudget(model *types.Model, level ResolvedGoogleThinkingLevel, customBudgets *types.ThinkingBudgets) int {
	if customBudgets != nil {
		switch level {
		case "minimal":
			if customBudgets.Minimal != nil {
				return *customBudgets.Minimal
			}
		case "low":
			if customBudgets.Low != nil {
				return *customBudgets.Low
			}
		case "medium":
			if customBudgets.Medium != nil {
				return *customBudgets.Medium
			}
		case "high":
			if customBudgets.High != nil {
				return *customBudgets.High
			}
		}
	}

	switch {
	case strings.Contains(model.Id, "2.5-pro"):
		return map[ResolvedGoogleThinkingLevel]int{"minimal": 128, "low": 2048, "medium": 8192, "high": 32768}[level]
	case strings.Contains(model.Id, "2.5-flash-lite"):
		return map[ResolvedGoogleThinkingLevel]int{"minimal": 512, "low": 2048, "medium": 8192, "high": 24576}[level]
	case strings.Contains(model.Id, "2.5-flash"):
		return map[ResolvedGoogleThinkingLevel]int{"minimal": 128, "low": 2048, "medium": 8192, "high": 24576}[level]
	default:
		return -1
	}
}

// googleOptionObserver returns the optional provider-stream-event observer.
func googleOptionObserver(options *GoogleOptions) func(data any, model *types.Model) error {
	if options == nil {
		return nil
	}
	return options.OnProviderStreamEvent
}
