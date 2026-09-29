package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

// This file translates the upstream Google Generative AI / Vertex tests into
// deterministic Go self-tests. Live credential/cloud tests become fake HTTP
// servers or direct helper calls; the limitation is that no real model call or
// GCP credential exchange runs.

const googleValidSignature = "AAAAAAAAAAAAAAAAAAAAAA=="

func googleModel(apiName types.KnownApi, provider types.ProviderId, id string) *types.Model {
	return &types.Model{
		Id:            id,
		Name:          id,
		Api:           types.Api(apiName),
		Provider:      provider,
		BaseUrl:       "https://example.invalid",
		Reasoning:     true,
		Input:         []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
		ContextWindow: 128000,
		MaxTokens:     4096,
	}
}

func googleTranscript(messages ...types.Message) *types.TranscriptContext {
	return types.NewTranscriptContext(messages)
}

func googleDrainStream(t *testing.T, stream *types.AssistantMessageEventStream) ([]types.AssistantMessageEventType, types.AssistantMessage) {
	t.Helper()
	seen := []types.AssistantMessageEventType{}
	for {
		item, ok := <-stream.Next()
		if !ok || item.Done {
			break
		}
		seen = append(seen, item.Value.Type)
	}
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}
	if result.Content == nil {
		result.Content = []types.ContentBlock{}
	}
	return seen, result
}

type googleCapture struct {
	mu       sync.Mutex
	paths    []string
	headers  []http.Header
	requests []map[string]any
}

func googleSSEServer(t *testing.T, status int, body string) (*httptest.Server, *googleCapture) {
	t.Helper()
	capture := &googleCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if len(raw) > 0 {
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.UseNumber()
			_ = decoder.Decode(&decoded)
		}
		capture.mu.Lock()
		capture.paths = append(capture.paths, r.URL.RequestURI())
		capture.headers = append(capture.headers, r.Header.Clone())
		capture.requests = append(capture.requests, decoded)
		capture.mu.Unlock()

		contentType := "text/event-stream"
		if status != 200 {
			contentType = "application/json"
		}
		w.Header().Set("content-type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, capture
}

func (c *googleCapture) lastHeader(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.headers) == 0 {
		return ""
	}
	return c.headers[len(c.headers)-1].Get(name)
}

func (c *googleCapture) lastRequest() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return nil
	}
	return c.requests[len(c.requests)-1]
}

func (c *googleCapture) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.paths)
}

func googleStreamOptions(apiKey string) types.StreamOptions {
	temperature := 0.0
	maxTokens := 16
	return types.StreamOptions{
		ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey},
		Temperature:            &temperature,
		MaxTokens:              &maxTokens,
	}
}

const googleTextSSE = "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"你好\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1,\"totalTokenCount\":3}}\n\n"

func TestGoogleGenerativeAIStreamText(t *testing.T) {
	server, capture := googleSSEServer(t, 200, googleTextSSE)
	model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("fixture"), "fixture")
	model.BaseUrl = server.URL

	transcript := googleTranscript(
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	)
	stream := GoogleGenerativeAIStream(model, transcript, &GoogleOptions{StreamOptions: googleStreamOptions("fixture-key")})
	events, result := googleDrainStream(t, stream)

	want := []types.AssistantMessageEventType{
		types.AssistantEventStart,
		types.AssistantEventTextStart,
		types.AssistantEventTextDelta,
		types.AssistantEventTextEnd,
		types.AssistantEventDone,
	}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || result.Content[0].Text.Text != "你好" {
		t.Fatalf("content = %#v", result.Content)
	}
	if result.Content[0].Text.TextSignature != nil {
		t.Fatalf("textSignature = %v, want nil", *result.Content[0].Text.TextSignature)
	}
	if result.Usage.Input != 2 || result.Usage.Output != 1 || result.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if result.Usage.Reasoning == nil || *result.Usage.Reasoning != 0 {
		t.Fatalf("usage.reasoning = %v, want 0", result.Usage.Reasoning)
	}
	if result.ResponseId != nil {
		t.Fatalf("responseId = %v, want nil", *result.ResponseId)
	}
	if capture.requestCount() != 1 {
		t.Fatalf("requests = %d", capture.requestCount())
	}
	if got := capture.paths[0]; got != "/models/fixture:streamGenerateContent?alt=sse" {
		t.Fatalf("path = %q", got)
	}
	if got := capture.lastHeader("x-goog-api-key"); got != "fixture-key" {
		t.Fatalf("x-goog-api-key = %q", got)
	}
	body := capture.lastRequest()
	if body == nil {
		t.Fatal("missing request body")
	}
	if _, ok := body["generationConfig"]; !ok {
		t.Fatalf("missing generationConfig in %v", body)
	}
	if _, ok := body["systemInstruction"]; !ok {
		t.Fatalf("missing systemInstruction in %v", body)
	}
}

func TestGoogleVertexStreamText(t *testing.T) {
	server, capture := googleSSEServer(t, 200, googleTextSSE)
	model := googleModel(types.ApiGoogleVertex, types.ProviderGoogleVertex, "fixture")
	model.BaseUrl = server.URL

	transcript := googleTranscript(
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	)
	project := "fixture"
	location := "us-central1"
	stream := GoogleVertexStream(model, transcript, &GoogleVertexOptions{
		StreamOptions: googleStreamOptions("fixture-key"),
		Project:       &project,
		Location:      &location,
	})
	_, result := googleDrainStream(t, stream)

	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if got := capture.paths[0]; got != "/v1/publishers/google/models/fixture:streamGenerateContent?alt=sse" {
		t.Fatalf("path = %q", got)
	}
}

func TestGoogleHTTPErrorIsClassified(t *testing.T) {
	server, capture := googleSSEServer(t, 400, `{"error":{"message":"fixture denied"}}`)
	model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("fixture"), "fixture")
	model.BaseUrl = server.URL

	transcript := googleTranscript(types.NewUserMessageVariant(types.NewUserMessage("hello", 2)))
	stream := GoogleGenerativeAIStream(model, transcript, &GoogleOptions{StreamOptions: googleStreamOptions("fixture-key")})
	events, result := googleDrainStream(t, stream)

	if len(events) != 1 || events[0] != types.AssistantEventError {
		t.Fatalf("events = %v", events)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if len(result.Content) != 0 {
		t.Fatalf("content = %#v", result.Content)
	}
	if result.Usage.Input != 0 || result.Usage.Output != 0 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if capture.requestCount() != 1 {
		t.Fatalf("requests = %d", capture.requestCount())
	}
}

func TestGoogleCancelledBeforeRequest(t *testing.T) {
	server, capture := googleSSEServer(t, 200, googleTextSSE)
	model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("fixture"), "fixture")
	model.BaseUrl = server.URL

	closed := make(chan struct{})
	close(closed)
	options := googleStreamOptions("fixture-key")
	options.Signal = closed

	transcript := googleTranscript(types.NewUserMessageVariant(types.NewUserMessage("hello", 2)))
	stream := GoogleGenerativeAIStream(model, transcript, &GoogleOptions{StreamOptions: options})
	events, result := googleDrainStream(t, stream)

	if len(events) != 1 || events[0] != types.AssistantEventError {
		t.Fatalf("events = %v", events)
	}
	if result.StopReason != types.StopReasonAborted {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if capture.requestCount() != 0 {
		t.Fatalf("requests = %d, want 0", capture.requestCount())
	}
}

func TestGoogleRawStopReasons(t *testing.T) {
	cases := []struct {
		name       string
		reason     string
		toolCall   bool
		wantReason types.StopReason
	}{
		{name: "gemini error", reason: "MALFORMED_FUNCTION_CALL", wantReason: types.StopReasonError},
		{name: "safety error", reason: "SAFETY", wantReason: types.StopReasonError},
		{name: "max tokens with tool call", reason: "MAX_TOKENS", toolCall: true, wantReason: types.StopReasonLength},
		{name: "stop with tool call", reason: "STOP", toolCall: true, wantReason: types.StopReasonToolUse},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parts := `[{"text":"hello"}]`
			if tc.toolCall {
				parts = `[{"functionCall":{"name":"echo","args":{"value":"truncated"}}}]`
			}
			sse := "data: {\"candidates\":[{\"finishReason\":\"" + tc.reason + "\",\"content\":{\"role\":\"model\",\"parts\":" + parts + "}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":0,\"totalTokenCount\":1}}\n\n"
			server, _ := googleSSEServer(t, 200, sse)
			model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("fixture"), "gemini-3-pro-preview")
			model.BaseUrl = server.URL

			transcript := googleTranscript(types.NewUserMessageVariant(types.NewUserMessage("hello", 2)))
			stream := GoogleGenerativeAIStream(model, transcript, &GoogleOptions{StreamOptions: googleStreamOptions("test-api-key")})
			_, result := googleDrainStream(t, stream)

			if result.StopReason != tc.wantReason {
				t.Fatalf("stopReason = %q, want %q", result.StopReason, tc.wantReason)
			}
			if result.RawStopReason == nil || *result.RawStopReason != tc.reason {
				t.Fatalf("rawStopReason = %v, want %q", result.RawStopReason, tc.reason)
			}
			if tc.wantReason == types.StopReasonError {
				if result.ErrorMessage == nil || *result.ErrorMessage != "Provider stopped with: "+tc.reason {
					t.Fatalf("errorMessage = %v", result.ErrorMessage)
				}
			}
			if tc.toolCall {
				found := false
				for _, block := range result.Content {
					if block.Type == types.ContentTypeToolCall {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing tool call in %#v", result.Content)
				}
			}
		})
	}
}

func TestGoogleUserAgentOverride(t *testing.T) {
	server, capture := googleSSEServer(t, 200, googleTextSSE)
	model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("fixture"), "fixture")
	model.BaseUrl = server.URL
	transcript := googleTranscript(types.NewUserMessageVariant(types.NewUserMessage("hello", 2)))

	stream := GoogleGenerativeAIStream(model, transcript, &GoogleOptions{StreamOptions: googleStreamOptions("test-api-key")})
	googleDrainStream(t, stream)
	if got := capture.lastHeader("User-Agent"); got != utils.GetPiUserAgent() {
		t.Fatalf("User-Agent = %q, want %q", got, utils.GetPiUserAgent())
	}

	custom := types.ProviderHeaders{"User-Agent": stringPointer("custom-agent")}
	options := googleStreamOptions("test-api-key")
	options.Headers = custom
	stream = GoogleGenerativeAIStream(model, transcript, &GoogleOptions{StreamOptions: options})
	googleDrainStream(t, stream)
	if got := capture.lastHeader("User-Agent"); got != "custom-agent" {
		t.Fatalf("User-Agent = %q, want custom-agent", got)
	}
}

func TestGoogleRetry(t *testing.T) {
	t.Run("retries a headers-less retryable error", func(t *testing.T) {
		calls := 0
		fn := func() (string, error) {
			calls++
			if calls == 1 {
				status := 429
				return "", &utils.ProviderError{Err: errors.New("got status: 429"), Status: &status}
			}
			return "ok", nil
		}
		maxRetries := 1
		result, err := RetryGoogleRequest(context.Background(), fn, &types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{MaxRetries: &maxRetries},
		})
		if err != nil {
			t.Fatalf("RetryGoogleRequest: %v", err)
		}
		if result != "ok" || calls != 2 {
			t.Fatalf("result = %q, calls = %d", result, calls)
		}
	})

	t.Run("does not retry when maxRetries is unset", func(t *testing.T) {
		calls := 0
		fn := func() (string, error) {
			calls++
			status := 429
			return "", &utils.ProviderError{Err: errors.New("got status: 429"), Status: &status}
		}
		if _, err := RetryGoogleRequest(context.Background(), fn, nil); err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})

	t.Run("does not retry a non-retryable status", func(t *testing.T) {
		calls := 0
		fn := func() (string, error) {
			calls++
			status := 400
			return "", &utils.ProviderError{Err: errors.New("got status: 400"), Status: &status}
		}
		maxRetries := 2
		if _, err := RetryGoogleRequest(context.Background(), fn, &types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{MaxRetries: &maxRetries},
		}); err == nil {
			t.Fatal("expected error")
		}
		if calls != 1 {
			t.Fatalf("calls = %d, want 1", calls)
		}
	})
}

func TestGoogleThinkingSignatureHelpers(t *testing.T) {
	if !IsThinkingPart(GooglePart{Thought: boolPointer(true)}) {
		t.Fatal("thought=true should be thinking")
	}
	if IsThinkingPart(GooglePart{Thought: boolPointer(false), ThoughtSignature: stringPointer("sig")}) {
		t.Fatal("thoughtSignature alone must not be thinking")
	}
	if IsThinkingPart(GooglePart{ThoughtSignature: stringPointer("sig")}) {
		t.Fatal("missing thought must not be thinking")
	}

	if got := RetainThoughtSignature(nil, stringPointer("sig-1")); got == nil || *got != "sig-1" {
		t.Fatalf("retain first = %v", got)
	}
	existing := stringPointer("sig-1")
	if got := RetainThoughtSignature(existing, nil); got != existing {
		t.Fatalf("retain nil = %v", got)
	}
	if got := RetainThoughtSignature(existing, stringPointer("")); got != existing {
		t.Fatalf("retain empty = %v", got)
	}
	if got := RetainThoughtSignature(existing, stringPointer("sig-2")); got == nil || *got != "sig-2" {
		t.Fatalf("retain update = %v", got)
	}
}

func TestGoogleThinkingLevels(t *testing.T) {
	model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("test-google"), "gemini-3.7-flash")
	for _, level := range []types.ThinkingLevel{types.ThinkingMinimal, types.ThinkingLow, types.ThinkingMedium, types.ThinkingHigh} {
		resolved, err := ResolveGoogleThinkingLevel(model, level)
		if err != nil {
			t.Fatalf("resolve %s: %v", level, err)
		}
		if string(resolved) != string(level) {
			t.Fatalf("resolve %s = %s", level, resolved)
		}
	}

	for _, mapped := range []string{"minimal", "low", "medium", "high", "MINIMAL", "LOW", "MEDIUM", "HIGH"} {
		mappedValue := mapped
		model.ThinkingLevelMap = types.ThinkingLevelMap{"high": &mappedValue, "xhigh": &mappedValue, "max": &mappedValue}
		resolvedLow := strings.ToLower(mapped)
		for _, level := range []types.ThinkingLevel{types.ThinkingHigh, types.ThinkingXHigh, types.ThinkingMax} {
			resolved, err := ResolveGoogleThinkingLevel(model, level)
			if err != nil {
				t.Fatalf("mapped %s/%s: %v", mapped, level, err)
			}
			if string(resolved) != resolvedLow {
				t.Fatalf("mapped %s/%s = %s, want %s", mapped, level, resolved, resolvedLow)
			}
		}
	}

	invalid := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("test-google"), "gemini-3.7-flash")
	extreme := "extreme"
	invalid.ThinkingLevelMap = types.ThinkingLevelMap{"xhigh": &extreme}
	if _, err := ResolveGoogleThinkingLevel(invalid, types.ThinkingXHigh); err == nil ||
		err.Error() != "Unsupported Google thinking level mapping for test-google/gemini-3.7-flash: xhigh -> extreme" {
		t.Fatalf("invalid error = %v", err)
	}

	empty := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("test-google"), "gemini-3.7-flash")
	if _, err := ResolveGoogleThinkingLevel(empty, types.ThinkingMax); err == nil ||
		err.Error() != "Unsupported Google thinking level mapping for test-google/gemini-3.7-flash: max -> undefined" {
		t.Fatalf("undefined error = %v", err)
	}

	if !UsesGoogleThinkingLevel(googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-3.1-pro-preview")) {
		t.Fatal("gemini-3 pro should use thinking level")
	}
	if UsesGoogleThinkingLevel(googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-2.5-flash")) {
		t.Fatal("gemini-2.5 should use thinking budget")
	}
	if !UsesGoogleThinkingLevel(googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemma-4-27b")) {
		t.Fatal("gemma 4 should use thinking level")
	}
}

func TestGoogleDisabledThinkingConfig(t *testing.T) {
	gemini25 := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-2.5-flash")
	config, err := GetDisabledGoogleThinkingConfig(gemini25)
	if err != nil {
		t.Fatalf("disabled config: %v", err)
	}
	if config.ThinkingBudget == nil || *config.ThinkingBudget != 0 {
		t.Fatalf("gemini 2.5 config = %#v", config)
	}

	low := "low"
	medium := "medium"
	high := "high"
	gemini3 := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-3.8-flash")
	gemini3.ThinkingLevelMap = types.ThinkingLevelMap{
		"off":     nil,
		"minimal": nil,
		"low":     &low,
		"medium":  &medium,
		"high":    &high,
		"xhigh":   nil,
		"max":     nil,
	}
	config, err = GetDisabledGoogleThinkingConfig(gemini3)
	if err != nil {
		t.Fatalf("disabled gemini3: %v", err)
	}
	if config.ThinkingLevel == nil || *config.ThinkingLevel != GoogleSdkThinkingLevelLow {
		t.Fatalf("gemini3 disabled config = %#v", config)
	}
}

type googleCapturedPayload struct {
	request *GoogleGenerateContentRequest
}

func googleCapturePayload(t *testing.T, streamFactory func(onPayload func(any, *types.Model) (any, error)) *types.AssistantMessageEventStream) *GoogleGenerateContentRequest {
	t.Helper()
	captured := &googleCapturedPayload{}
	stream := streamFactory(func(payload any, model *types.Model) (any, error) {
		if request, ok := payload.(*GoogleGenerateContentRequest); ok {
			captured.request = request
		}
		return nil, errors.New("payload captured")
	})
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if result.ErrorMessage == nil || !strings.Contains(*result.ErrorMessage, "payload captured") {
		t.Fatalf("errorMessage = %v", result.ErrorMessage)
	}
	if captured.request == nil {
		t.Fatal("payload was not captured")
	}
	return captured.request
}

func TestGoogleSimplePayloadThinking(t *testing.T) {
	apiKey := "test"
	context := googleTranscript(types.NewUserMessageVariant(types.NewUserMessage("Hello", 0)))

	baseOptions := func() types.SimpleStreamOptions {
		return types.SimpleStreamOptions{StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey},
		}}
	}

	low := "low"
	medium := "medium"
	high := "high"

	t.Run("gemini 3 lowest supported when reasoning omitted", func(t *testing.T) {
		model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("test-google"), "gemini-3.8-flash")
		model.ThinkingLevelMap = types.ThinkingLevelMap{"off": nil, "minimal": nil, "low": &low, "medium": &medium, "high": &high, "xhigh": nil, "max": nil}
		options := baseOptions()
		request := googleCapturePayload(t, func(onPayload func(any, *types.Model) (any, error)) *types.AssistantMessageEventStream {
			options.OnPayload = onPayload
			return GoogleGenerativeAIStreamSimple(model, context, &options)
		})
		if request.GenerationConfig == nil || request.GenerationConfig.ThinkingConfig == nil {
			t.Fatalf("thinking config missing: %#v", request)
		}
		thinking := request.GenerationConfig.ThinkingConfig
		if thinking.ThinkingLevel == nil || *thinking.ThinkingLevel != GoogleSdkThinkingLevelLow {
			t.Fatalf("thinking = %#v", thinking)
		}
		if thinking.IncludeThoughts != nil {
			t.Fatalf("includeThoughts = %v, want absent", *thinking.IncludeThoughts)
		}
	})

	t.Run("gemini 3.1 pro preserves medium", func(t *testing.T) {
		model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("test-google"), "gemini-3.1-pro-preview")
		model.ThinkingLevelMap = types.ThinkingLevelMap{"low": &low, "medium": &medium, "high": &high}
		options := baseOptions()
		reasoning := types.ThinkingMedium
		options.Reasoning = &reasoning
		request := googleCapturePayload(t, func(onPayload func(any, *types.Model) (any, error)) *types.AssistantMessageEventStream {
			options.OnPayload = onPayload
			return GoogleGenerativeAIStreamSimple(model, context, &options)
		})
		thinking := request.GenerationConfig.ThinkingConfig
		if thinking == nil || thinking.IncludeThoughts == nil || !*thinking.IncludeThoughts {
			t.Fatalf("thinking = %#v", thinking)
		}
		if thinking.ThinkingLevel == nil || *thinking.ThinkingLevel != GoogleSdkThinkingLevelMedium {
			t.Fatalf("thinkingLevel = %v", thinking.ThinkingLevel)
		}
	})

	t.Run("gemini 2.5 disables thinking when reasoning omitted", func(t *testing.T) {
		model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("test-google"), "gemini-2.5-flash")
		options := baseOptions()
		request := googleCapturePayload(t, func(onPayload func(any, *types.Model) (any, error)) *types.AssistantMessageEventStream {
			options.OnPayload = onPayload
			return GoogleGenerativeAIStreamSimple(model, context, &options)
		})
		thinking := request.GenerationConfig.ThinkingConfig
		if thinking == nil || thinking.ThinkingBudget == nil || *thinking.ThinkingBudget != 0 {
			t.Fatalf("thinking = %#v", thinking)
		}
	})

	t.Run("mapped token budgets", func(t *testing.T) {
		model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("test-google"), "gemini-2.5-flash")
		highMapping := "high"
		model.ThinkingLevelMap = types.ThinkingLevelMap{"xhigh": &highMapping}
		options := baseOptions()
		reasoning := types.ThinkingXHigh
		options.Reasoning = &reasoning
		budget := 1234
		options.ThinkingBudgets = &types.ThinkingBudgets{High: &budget}
		request := googleCapturePayload(t, func(onPayload func(any, *types.Model) (any, error)) *types.AssistantMessageEventStream {
			options.OnPayload = onPayload
			return GoogleGenerativeAIStreamSimple(model, context, &options)
		})
		thinking := request.GenerationConfig.ThinkingConfig
		if thinking == nil || thinking.ThinkingBudget == nil || *thinking.ThinkingBudget != 1234 {
			t.Fatalf("thinking = %#v", thinking)
		}
	})

	t.Run("vertex extended level", func(t *testing.T) {
		model := googleModel(types.ApiGoogleVertex, types.ProviderGoogleVertex, "gemini-3.7-flash")
		highMapping := "high"
		model.ThinkingLevelMap = types.ThinkingLevelMap{"xhigh": &highMapping}
		options := baseOptions()
		reasoning := types.ThinkingXHigh
		options.Reasoning = &reasoning
		request := googleCapturePayload(t, func(onPayload func(any, *types.Model) (any, error)) *types.AssistantMessageEventStream {
			options.OnPayload = onPayload
			return GoogleVertexStreamSimple(model, context, &options)
		})
		thinking := request.GenerationConfig.ThinkingConfig
		if thinking == nil || thinking.ThinkingLevel == nil || *thinking.ThinkingLevel != GoogleSdkThinkingLevelHigh {
			t.Fatalf("thinking = %#v", thinking)
		}
	})

	t.Run("vertex mapped token budget", func(t *testing.T) {
		model := googleModel(types.ApiGoogleVertex, types.ProviderGoogleVertex, "gemini-2.5-flash")
		highMapping := "high"
		model.ThinkingLevelMap = types.ThinkingLevelMap{"max": &highMapping}
		options := baseOptions()
		reasoning := types.ThinkingMax
		options.Reasoning = &reasoning
		budget := 4321
		options.ThinkingBudgets = &types.ThinkingBudgets{High: &budget}
		request := googleCapturePayload(t, func(onPayload func(any, *types.Model) (any, error)) *types.AssistantMessageEventStream {
			options.OnPayload = onPayload
			return GoogleVertexStreamSimple(model, context, &options)
		})
		thinking := request.GenerationConfig.ThinkingConfig
		if thinking == nil || thinking.ThinkingBudget == nil || *thinking.ThinkingBudget != 4321 {
			t.Fatalf("thinking = %#v", thinking)
		}
	})
}

func TestRequiresToolCallID(t *testing.T) {
	cases := []struct {
		modelID string
		want    bool
	}{
		{"gemini-2.5-flash", false},
		{"gemini-3.6-flash", true},
		{"claude-sonnet-4-5", true},
		{"gpt-oss-120b", true},
	}
	for _, tc := range cases {
		if got := RequiresToolCallID(tc.modelID); got != tc.want {
			t.Fatalf("RequiresToolCallID(%q) = %v, want %v", tc.modelID, got, tc.want)
		}
	}
}

func googleToolCallBlock(id, name string, args string) types.ContentBlock {
	return types.ToolCallBlock(types.ToolCall{
		Type:      types.ContentTypeToolCall,
		Id:        id,
		Name:      name,
		Arguments: json.RawMessage(args),
	})
}

func googleToolContext(model *types.Model, thoughtSignature *string) *types.TranscriptContext {
	call1 := types.ToolCall{Type: types.ContentTypeToolCall, Id: "call_1", Name: "bash", Arguments: json.RawMessage(`{"command":"echo hi"}`)}
	if thoughtSignature != nil {
		call1.ThoughtSignature = thoughtSignature
	}
	call2 := types.ToolCall{Type: types.ContentTypeToolCall, Id: "call_2", Name: "bash", Arguments: json.RawMessage(`{"command":"ls -la"}`)}
	assistant := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
	assistant.StopReason = types.StopReasonToolUse
	assistant.Content = []types.ContentBlock{types.ToolCallBlock(call1), types.ToolCallBlock(call2)}
	result1 := types.NewToolResultMessage("call_1", "bash", []types.ContentBlock{types.TextBlock("hi")}, false, 0)
	result2 := types.NewToolResultMessage("call_2", "bash", []types.ContentBlock{types.TextBlock("files")}, false, 0)
	return googleTranscript(
		types.NewUserMessageVariant(types.NewUserMessage("Hi", 0)),
		types.NewAssistantMessageVariant(assistant),
		types.NewToolResultMessageVariant(result1),
		types.NewToolResultMessageVariant(result2),
	)
}

func TestGoogleConvertMessagesGemini3ToolIDs(t *testing.T) {
	for _, tc := range []struct {
		apiName  types.KnownApi
		provider types.ProviderId
		id       string
	}{
		{types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-3-pro-preview"},
		{types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-3.6-flash"},
		{types.ApiGoogleVertex, types.ProviderGoogleVertex, "gemini-3-pro-preview"},
	} {
		model := googleModel(tc.apiName, tc.provider, tc.id)
		contents := GoogleConvertMessages(model, googleToolContext(model, nil))
		functionCallIDs := []string{}
		functionResponseIDs := []string{}
		for _, content := range contents {
			for _, part := range content.Parts {
				if part.FunctionCall != nil && part.FunctionCall.Id != nil {
					functionCallIDs = append(functionCallIDs, *part.FunctionCall.Id)
				}
				if part.FunctionResponse != nil && part.FunctionResponse.Id != nil {
					functionResponseIDs = append(functionResponseIDs, *part.FunctionResponse.Id)
				}
			}
		}
		if strings.Join(functionCallIDs, ",") != "call_1,call_2" {
			t.Fatalf("%s function call ids = %v", tc.id, functionCallIDs)
		}
		if strings.Join(functionResponseIDs, ",") != "call_1,call_2" {
			t.Fatalf("%s function response ids = %v", tc.id, functionResponseIDs)
		}
	}
}

func TestGoogleConvertMessagesNoSignatureWithoutThought(t *testing.T) {
	model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-3-pro-preview")
	other := "other-model"
	contents := GoogleConvertMessages(model, googleToolContext(googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), other), nil))
	serialized, _ := json.Marshal(contents)
	if strings.Contains(string(serialized), "skip_thought_signature_validator") {
		t.Fatalf("unexpected signature marker in %s", serialized)
	}
	for _, content := range contents {
		if content.Role != "model" {
			continue
		}
		for _, part := range content.Parts {
			if part.FunctionCall != nil && part.ThoughtSignature != nil {
				t.Fatalf("unexpected thought signature")
			}
		}
	}
}

func TestGoogleConvertMessagesSignedEmptyBlocks(t *testing.T) {
	model := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-3-pro-preview")

	signedThinking := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
	signedThinking.Content = []types.ContentBlock{
		types.ThinkingBlockSigned("", googleValidSignature),
		googleToolCallBlock("call_1", "bash", `{"command":"ls"}`),
	}
	contents := GoogleConvertMessages(model, googleTranscript(
		types.NewUserMessageVariant(types.NewUserMessage("Hi", 0)),
		types.NewAssistantMessageVariant(signedThinking),
	))
	signed := 0
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.ThoughtSignature != nil && *part.ThoughtSignature == googleValidSignature {
				signed++
				if part.Thought == nil || !*part.Thought {
					t.Fatalf("signed thinking part must have thought=true")
				}
			}
		}
	}
	if signed != 1 {
		t.Fatalf("signed thinking parts = %d, want 1", signed)
	}

	signedText := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
	signedText.Content = []types.ContentBlock{
		types.TextBlockSigned("", googleValidSignature),
		googleToolCallBlock("call_1", "bash", `{"command":"ls"}`),
	}
	contents = GoogleConvertMessages(model, googleTranscript(
		types.NewUserMessageVariant(types.NewUserMessage("Hi", 0)),
		types.NewAssistantMessageVariant(signedText),
	))
	signed = 0
	for _, content := range contents {
		for _, part := range content.Parts {
			if part.ThoughtSignature != nil && *part.ThoughtSignature == googleValidSignature {
				signed++
			}
		}
	}
	if signed != 1 {
		t.Fatalf("signed text parts = %d, want 1", signed)
	}

	unsigned := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
	unsigned.Content = []types.ContentBlock{
		types.ThinkingBlock(""),
		types.TextBlock("   "),
		googleToolCallBlock("call_1", "bash", `{"command":"ls"}`),
	}
	contents = GoogleConvertMessages(model, googleTranscript(
		types.NewUserMessageVariant(types.NewUserMessage("Hi", 0)),
		types.NewAssistantMessageVariant(unsigned),
	))
	modelTurnParts := 0
	for _, content := range contents {
		if content.Role == "model" {
			modelTurnParts = len(content.Parts)
		}
	}
	if modelTurnParts != 1 {
		t.Fatalf("unsigned model turn parts = %d, want 1", modelTurnParts)
	}

	crossModel := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-3-pro-preview")
	cross := types.NewAssistantMessage(model.Api, model.Provider, "other-model", 0)
	cross.Content = []types.ContentBlock{
		types.ThinkingBlockSigned("", googleValidSignature),
		types.TextBlockSigned("", googleValidSignature),
		googleToolCallBlock("call_1", "bash", `{"command":"ls"}`),
	}
	contents = GoogleConvertMessages(crossModel, googleTranscript(
		types.NewUserMessageVariant(types.NewUserMessage("Hi", 0)),
		types.NewAssistantMessageVariant(cross),
	))
	serialized, _ := json.Marshal(contents)
	if strings.Contains(string(serialized), googleValidSignature) {
		t.Fatalf("cross-model signature leaked: %s", serialized)
	}
}

func googleImageToolContext(model *types.Model) *types.TranscriptContext {
	assistant := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
	assistant.StopReason = types.StopReasonToolUse
	assistant.Content = []types.ContentBlock{
		googleToolCallBlock("call_a", "read", `{"path":"a.txt"}`),
		googleToolCallBlock("call_img", "read", `{"path":"image.png"}`),
		googleToolCallBlock("call_b", "read", `{"path":"b.txt"}`),
	}
	resultA := types.NewToolResultMessage("call_a", "read", []types.ContentBlock{types.TextBlock("alpha text")}, false, 0)
	resultImg := types.NewToolResultMessage("call_img", "read", []types.ContentBlock{types.ImageBlock("abc", "image/png")}, false, 0)
	resultB := types.NewToolResultMessage("call_b", "read", []types.ContentBlock{types.TextBlock("beta text")}, false, 0)
	return googleTranscript(
		types.NewUserMessageVariant(types.NewUserMessage("read the files", 0)),
		types.NewAssistantMessageVariant(assistant),
		types.NewToolResultMessageVariant(resultA),
		types.NewToolResultMessageVariant(resultImg),
		types.NewToolResultMessageVariant(resultB),
	)
}

func TestGoogleConvertMessagesImageToolResultRouting(t *testing.T) {
	gemini2 := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-2.5-flash")
	contents := GoogleConvertMessages(gemini2, googleImageToolContext(gemini2))
	if len(contents) != 5 {
		t.Fatalf("gemini 2.5 contents = %d, want 5", len(contents))
	}
	if contents[3].Parts[0].Text == nil || *contents[3].Parts[0].Text != "Tool result image:" {
		t.Fatalf("gemini 2.5 image part = %#v", contents[3].Parts[0])
	}
	if len(contents[3].Parts) < 2 || contents[3].Parts[1].InlineData == nil {
		t.Fatalf("gemini 2.5 image inline data missing")
	}

	gemini3 := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-3-pro-preview")
	contents = GoogleConvertMessages(gemini3, googleImageToolContext(gemini3))
	if len(contents) != 3 {
		t.Fatalf("gemini 3 contents = %d, want 3", len(contents))
	}
	toolTurn := contents[2]
	if len(toolTurn.Parts) != 3 {
		t.Fatalf("gemini 3 tool turn parts = %d, want 3", len(toolTurn.Parts))
	}
	imageResponse := toolTurn.Parts[1].FunctionResponse
	if imageResponse == nil || len(imageResponse.Parts) != 1 || imageResponse.Parts[0].InlineData == nil {
		t.Fatalf("gemini 3 image response = %#v", imageResponse)
	}
}

func TestGoogleConvertTools(t *testing.T) {
	parameters := json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","$id":"urn:bash-tool","$comment":"demo","$defs":{"x":{"type":"string"}},"definitions":{"y":{"type":"number"}},"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
	tools := []types.Tool{types.NewTool("test_tool", "A test tool", parameters)}

	groups, err := GoogleConvertTools(tools, true, true)
	if err != nil {
		t.Fatalf("convertTools: %v", err)
	}
	if len(groups) != 1 || len(groups[0].FunctionDeclarations) != 1 {
		t.Fatalf("groups = %#v", groups)
	}
	legacy := groups[0].FunctionDeclarations[0]
	if len(legacy.Parameters) == 0 || len(legacy.ParametersJSONSchema) != 0 {
		t.Fatalf("legacy declaration = %#v", legacy)
	}
	var legacyMap map[string]any
	if err := json.Unmarshal(legacy.Parameters, &legacyMap); err != nil {
		t.Fatalf("legacy parameters: %v", err)
	}
	for _, key := range []string{"$schema", "$id", "$comment", "$defs", "definitions"} {
		if _, ok := legacyMap[key]; ok {
			t.Fatalf("legacy parameters retained %s: %v", key, legacyMap)
		}
	}
	if legacyMap["type"] != "object" {
		t.Fatalf("legacy parameters = %v", legacyMap)
	}

	groups, err = GoogleConvertTools(tools, false, true)
	if err != nil {
		t.Fatalf("convertTools json schema: %v", err)
	}
	modern := groups[0].FunctionDeclarations[0]
	var modernMap map[string]any
	if err := json.Unmarshal(modern.ParametersJSONSchema, &modernMap); err != nil {
		t.Fatalf("modern parameters: %v", err)
	}
	if modernMap["$schema"] != "http://json-schema.org/draft-07/schema#" {
		t.Fatalf("modern parameters lost $schema: %v", modernMap)
	}

	if empty, err := GoogleConvertTools(nil, false, true); err != nil || empty != nil {
		t.Fatalf("empty tools = %#v, %v", empty, err)
	}
}

func TestGoogleStrictToolSampling(t *testing.T) {
	if !SupportsGoogleStrictToolSampling("gemini-3.1-pro-preview") {
		t.Fatal("gemini 3 should support strict sampling")
	}
	if SupportsGoogleStrictToolSampling("gemini-2.5-pro") {
		t.Fatal("gemini 2.5 should not support strict sampling")
	}

	tool := types.NewTool("test_tool", "A test tool", json.RawMessage(`{"type":"object","properties":{}}`))
	strict := types.ConstrainedStrictRequire
	sampling := types.NewJSONSchemaSampling(strict)
	tool.ConstrainedSampling = &sampling

	mode, err := ResolveGoogleFunctionCallingMode([]types.Tool{tool}, nil, true)
	if err != nil {
		t.Fatalf("resolve mode: %v", err)
	}
	if mode == nil || *mode != GoogleFunctionCallingValidated {
		t.Fatalf("mode = %v", mode)
	}

	if _, err := ResolveGoogleFunctionCallingMode([]types.Tool{tool}, nil, false); err == nil ||
		!strings.Contains(err.Error(), `Tool "test_tool" requires JSON-schema constrained sampling`) {
		t.Fatalf("strict unsupported error = %v", err)
	}

	auto := "auto"
	mode, err = ResolveGoogleFunctionCallingMode(nil, &auto, true)
	if err != nil {
		t.Fatalf("resolve auto: %v", err)
	}
	if mode == nil || *mode != GoogleFunctionCallingAuto {
		t.Fatalf("auto mode = %v", mode)
	}
	if MapGoogleToolChoice("any") != GoogleFunctionCallingAny {
		t.Fatal("map any")
	}
	if MapGoogleToolChoice("unknown") != GoogleFunctionCallingAuto {
		t.Fatal("map default")
	}
}

func TestGoogleStopReasonMapping(t *testing.T) {
	reason, err := MapGoogleStopReason(GoogleFinishReasonStop)
	if err != nil || reason != types.StopReasonStop {
		t.Fatalf("STOP = %v, %v", reason, err)
	}
	reason, err = MapGoogleStopReason(GoogleFinishReasonMaxTokens)
	if err != nil || reason != types.StopReasonLength {
		t.Fatalf("MAX_TOKENS = %v, %v", reason, err)
	}
	reason, err = MapGoogleStopReason(GoogleFinishReasonSafety)
	if err != nil || reason != types.StopReasonError {
		t.Fatalf("SAFETY = %v, %v", reason, err)
	}
	if _, err := MapGoogleStopReason(GoogleFinishReason("BOGUS")); err == nil {
		t.Fatal("expected unhandled reason error")
	}
	if MapGoogleStopReasonString("STOP") != types.StopReasonStop {
		t.Fatal("string STOP")
	}
	if MapGoogleStopReasonString("MAX_TOKENS") != types.StopReasonLength {
		t.Fatal("string MAX_TOKENS")
	}
	if MapGoogleStopReasonString("BOGUS") != types.StopReasonError {
		t.Fatal("string default")
	}
}

func TestGoogleVertexAPIKeyResolution(t *testing.T) {
	if resolveGoogleVertexAPIKey(nil) != nil {
		t.Fatal("nil options should resolve no key")
	}
	marker := googleVertexCredentialsMarker
	if resolveGoogleVertexAPIKey(&GoogleVertexOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &marker}}}) != nil {
		t.Fatal("marker key should resolve no key")
	}
	placeholder := "<authenticated>"
	if resolveGoogleVertexAPIKey(&GoogleVertexOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &placeholder}}}) != nil {
		t.Fatal("placeholder key should resolve no key")
	}
	blank := "   "
	if resolveGoogleVertexAPIKey(&GoogleVertexOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &blank}}}) != nil {
		t.Fatal("blank key should resolve no key")
	}
	realKey := "  AIzaSyExampleRealisticLookingApiKey123456  "
	resolved := resolveGoogleVertexAPIKey(&GoogleVertexOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &realKey}}})
	if resolved == nil || *resolved != "AIzaSyExampleRealisticLookingApiKey123456" {
		t.Fatalf("real key = %v", resolved)
	}

	if resolveGoogleVertexCustomBaseURL("  https://proxy.example.com  ") != "https://proxy.example.com" {
		t.Fatal("custom base url trim")
	}
	if resolveGoogleVertexCustomBaseURL("https://{location}-aiplatform.googleapis.com") != "" {
		t.Fatal("location placeholder must be ignored")
	}
	if !googleVertexBaseURLIncludesAPIVersion("https://proxy.example.com/v1/projects/test") {
		t.Fatal("version detection")
	}
	if googleVertexBaseURLIncludesAPIVersion("https://proxy.example.com") {
		t.Fatal("no version")
	}
}

func TestGoogleVertexProjectAndLocationErrors(t *testing.T) {
	options := &GoogleVertexOptions{}
	if _, err := resolveGoogleVertexProject(options); err == nil {
		t.Fatal("expected project error")
	}
	if _, err := resolveGoogleVertexLocation(options); err == nil {
		t.Fatal("expected location error")
	}
	project := "fixture"
	location := "us-central1"
	options.Project = &project
	options.Location = &location
	if got, err := resolveGoogleVertexProject(options); err != nil || got != "fixture" {
		t.Fatalf("project = %q, %v", got, err)
	}
	if got, err := resolveGoogleVertexLocation(options); err != nil || got != "us-central1" {
		t.Fatalf("location = %q, %v", got, err)
	}
}

func TestGoogleLazyFactories(t *testing.T) {
	for _, factory := range []func() types.ProviderStreams{GoogleGenerativeAIApi, GoogleVertexApi} {
		streams := factory()
		if streams == nil {
			t.Fatal("nil provider streams")
		}
	}
}

func TestGoogleBudgetHelpers(t *testing.T) {
	pro := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-2.5-pro")
	if got := googleGenerativeAIBudget(pro, "high", nil); got != 32768 {
		t.Fatalf("pro high = %d", got)
	}
	flash := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-2.5-flash")
	if got := googleGenerativeAIBudget(flash, "minimal", nil); got != 128 {
		t.Fatalf("flash minimal = %d", got)
	}
	lite := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-2.5-flash-lite")
	if got := googleGenerativeAIBudget(lite, "minimal", nil); got != 512 {
		t.Fatalf("lite minimal = %d", got)
	}
	vertex := googleModel(types.ApiGoogleVertex, types.ProviderGoogleVertex, "gemini-2.5-pro")
	if got := googleVertexBudget(vertex, "high", nil); got != 32768 {
		t.Fatalf("vertex pro high = %d", got)
	}
	unknown := googleModel(types.ApiGoogleGenerativeAI, types.ProviderId("google"), "gemini-4-unknown")
	if got := googleGenerativeAIBudget(unknown, "high", nil); got != -1 {
		t.Fatalf("unknown budget = %d", got)
	}
}

var _ = time.Now
