package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
)

func drainAssistantStream(t *testing.T, stream *types.AssistantMessageEventStream) ([]string, types.AssistantMessage) {
	t.Helper()
	events := []string{}
	for {
		item, ok := <-stream.Next()
		if !ok || item.Done {
			break
		}
		events = append(events, string(item.Value.Type))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}
	return events, result
}

func TestCloudflareBindingConstants(t *testing.T) {
	if CloudflareGatewayBindingAuthSentinel != "cloudflare-gateway-binding" {
		t.Fatalf("unexpected sentinel: %q", CloudflareGatewayBindingAuthSentinel)
	}
	if CloudflareWorkersAIBaseURL != "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1" {
		t.Fatalf("unexpected workers ai url: %q", CloudflareWorkersAIBaseURL)
	}
	if CloudflareAIGatewayCompatBaseURL != "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat" {
		t.Fatalf("unexpected compat url")
	}
	if CloudflareAIGatewayOpenAIBaseURL == "" || CloudflareAIGatewayAnthropicBaseURL == "" {
		t.Fatal("gateway passthrough urls must be set")
	}
}

func TestCreateAiBindingFetch(t *testing.T) {
	if _, err := CreateAiBindingFetch(AiBinding{}); !errors.Is(err, ErrAiBindingFetchMissing) {
		t.Fatalf("expected missing fetch error, got %v", err)
	}

	var got *types.HTTPRequest
	binding := AiBinding{
		Fetch: func(input *types.HTTPRequest) (*types.HTTPResponse, error) {
			got = input
			return &types.HTTPResponse{Status: 200, Body: []byte("ok")}, nil
		},
	}
	fetch, err := CreateAiBindingFetch(binding)
	if err != nil {
		t.Fatalf("create fetch: %v", err)
	}
	req := &types.HTTPRequest{Method: http.MethodPost, URL: "https://workers-binding.ai/ai-gateway/gateways/g/anthropic"}
	response, err := fetch(req)
	if err != nil {
		t.Fatalf("binding fetch: %v", err)
	}
	if got != req || response.Status != 200 {
		t.Fatalf("binding passthrough failed: %+v", response)
	}
	// Clearing the original binding must not affect the returned closure.
	binding.Fetch = nil
	if _, err := fetch(req); err != nil {
		t.Fatalf("bound fetch lost after mutation: %v", err)
	}
}

func mistralTestModel(baseURL string) types.Model {
	return types.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           types.ApiMistralConversations,
		Provider:      types.ProviderId("fixture"),
		BaseUrl:       baseURL,
		Input:         []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
		ContextWindow: 8192,
		MaxTokens:     1024,
	}
}

func mistralTestTranscript() *types.TranscriptContext {
	return types.NewTranscriptContext([]types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	})
}

func testStreamOptions(apiKey string, signal <-chan struct{}) types.StreamOptions {
	maxTokens := 16
	temperature := 0.0
	return types.StreamOptions{
		ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey, Signal: signal},
		Temperature:            &temperature,
		MaxTokens:              &maxTokens,
	}
}

func TestMistralConversationsTextStream(t *testing.T) {
	body := "event: message\ndata: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\nevent: message\ndata: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n"
	var recordedPath string
	var recordedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedPath = r.URL.RequestURI()
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &recordedBody)
		w.Header().Set("content-type", "text/event-stream")
		w.Write([]byte(body))
	}))
	defer server.Close()

	model := mistralTestModel(server.URL)
	apiKey := "fixture-key"
	options := &MistralOptions{StreamOptions: testStreamOptions(apiKey, nil)}
	events, result := drainAssistantStream(t, MistralConversationsStream(&model, mistralTestTranscript(), options))

	if recordedPath != "/v1/chat/completions" {
		t.Fatalf("unexpected path %q", recordedPath)
	}
	if recordedBody["max_tokens"] != float64(16) {
		t.Fatalf("expected max_tokens remap, got %v", recordedBody["max_tokens"])
	}
	if len(recordedBody["messages"].([]any)) != 2 {
		t.Fatalf("unexpected messages payload")
	}
	want := []string{"start", "text_start", "text_delta", "text_end", "done"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if result.ResponseId == nil || *result.ResponseId != "r1" {
		t.Fatalf("responseId = %v", result.ResponseId)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || result.Content[0].Text.Text != "hi" {
		t.Fatalf("content = %+v", result.Content)
	}
	if result.Usage.Input != 2 || result.Usage.Output != 1 || result.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestMistralConversationsToolCallStream(t *testing.T) {
	body := "data: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call12345\",\"type\":\"function\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\\\"text\\\":\\\"hi\\\"}\"}}]},\"finish_reason\":null}]}\n\ndata: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.Write([]byte(body))
	}))
	defer server.Close()

	model := mistralTestModel(server.URL)
	apiKey := "fixture-key"
	events, result := drainAssistantStream(t, MistralConversationsStream(&model, mistralTestTranscript(), &MistralOptions{StreamOptions: testStreamOptions(apiKey, nil)}))

	want := []string{"start", "toolcall_start", "toolcall_delta", "toolcall_end", "done"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if result.StopReason != types.StopReasonToolUse {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if len(result.Content) != 1 || result.Content[0].ToolCall == nil {
		t.Fatalf("content = %+v", result.Content)
	}
	call := result.Content[0].ToolCall
	if call.Id != "call12345" || call.Name != "echo" {
		t.Fatalf("tool call = %+v", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil || args["text"] != "hi" {
		t.Fatalf("tool arguments = %s", call.Arguments)
	}
}

func TestMistralConversationsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"fixture denied","code":"bad_request"}}`))
	}))
	defer server.Close()

	model := mistralTestModel(server.URL)
	apiKey := "fixture-key"
	events, result := drainAssistantStream(t, MistralConversationsStream(&model, mistralTestTranscript(), &MistralOptions{StreamOptions: testStreamOptions(apiKey, nil)}))

	if strings.Join(events, ",") != "error" {
		t.Fatalf("events = %v", events)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if result.ErrorMessage == nil || !strings.Contains(*result.ErrorMessage, "fixture denied") {
		t.Fatalf("errorMessage = %v", result.ErrorMessage)
	}
	if len(result.Content) != 0 {
		t.Fatalf("content should be empty, got %+v", result.Content)
	}
}

func TestMistralConversationsAborted(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("content-type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	signal := make(chan struct{})
	close(signal)
	model := mistralTestModel(server.URL)
	events, result := drainAssistantStream(t, MistralConversationsStream(&model, mistralTestTranscript(), &MistralOptions{StreamOptions: testStreamOptions("fixture-key", signal)}))

	if requests != 0 {
		t.Fatalf("aborted stream must not reach the server, got %d requests", requests)
	}
	if strings.Join(events, ",") != "error" {
		t.Fatalf("events = %v", events)
	}
	if result.StopReason != types.StopReasonAborted {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
}

func TestMistralWirePayloadRemap(t *testing.T) {
	wire := toMistralWirePayload(map[string]any{
		"maxTokens":      16,
		"promptCacheKey": "session",
		"responseFormat": map[string]any{
			"jsonSchema": map[string]any{"schemaDefinition": map[string]any{"type": "object"}},
		},
		"messages": []map[string]any{{"role": "assistant", "toolCalls": []any{}, "toolCallId": "c1"}},
	})
	if wire["max_tokens"] != 16 || wire["prompt_cache_key"] != "session" {
		t.Fatalf("top-level remap failed: %+v", wire)
	}
	if _, present := wire["maxTokens"]; present {
		t.Fatal("maxTokens must be removed")
	}
	responseFormat := wire["response_format"].(map[string]any)
	jsonSchema := responseFormat["json_schema"].(map[string]any)
	if _, ok := jsonSchema["schema"]; !ok {
		t.Fatalf("nested json schema remap failed: %+v", jsonSchema)
	}
}

func TestMistralToolCallIDNormalizer(t *testing.T) {
	normalize := createMistralToolCallIDNormalizer()
	first := normalize("call_abc/def-ghi")
	if len(first) != mistralToolCallIDLength {
		t.Fatalf("normalized id length = %d (%q)", len(first), first)
	}
	if again := normalize("call_abc/def-ghi"); again != first {
		t.Fatalf("normalization must be stable: %q vs %q", again, first)
	}
	other := normalize("call_abc/def-ghj")
	if other == first {
		t.Fatal("distinct ids must not collide")
	}
}

func piMessagesTestModel(baseURL string) types.Model {
	return types.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           types.ApiPiMessages,
		Provider:      types.ProviderId("fixture"),
		BaseUrl:       baseURL,
		Input:         []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
		ContextWindow: 8192,
		MaxTokens:     1024,
	}
}

func TestPiMessagesTextStream(t *testing.T) {
	body := "event: start\ndata: {\"type\":\"start\"}\n\nevent: text_start\ndata: {\"type\":\"text_start\",\"contentIndex\":0}\n\nevent: text_delta\ndata: {\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"hi\"}\n\nevent: text_end\ndata: {\"type\":\"text_end\",\"contentIndex\":0,\"content\":\"hi\",\"contentSignature\":\"sig\"}\n\nevent: done\ndata: {\"type\":\"done\",\"reason\":\"stop\",\"responseId\":\"r1\",\"usage\":{\"input\":2,\"output\":1,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":3,\"cost\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"total\":0}}}\n\n"
	var recordedPath string
	var recordedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedPath = r.URL.RequestURI()
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &recordedBody)
		w.Header().Set("content-type", "text/event-stream")
		w.Write([]byte(body))
	}))
	defer server.Close()

	model := piMessagesTestModel(server.URL)
	events, result := drainAssistantStream(t, PiMessagesStream(&model, mistralTestTranscript(), &PiMessagesOptions{StreamOptions: testStreamOptions("fixture-key", nil)}))

	if recordedPath != "/messages" {
		t.Fatalf("path = %q", recordedPath)
	}
	contextPayload, ok := recordedBody["context"].(map[string]any)
	if !ok {
		t.Fatalf("context payload = %+v", recordedBody["context"])
	}
	if _, ok := contextPayload["messages"].([]any); !ok {
		t.Fatalf("context.messages missing: %+v", contextPayload)
	}
	want := []string{"start", "text_start", "text_delta", "text_end", "done"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if result.StopReason != types.StopReasonStop || result.ResponseId == nil || *result.ResponseId != "r1" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || result.Content[0].Text.TextSignature == nil || *result.Content[0].Text.TextSignature != "sig" {
		t.Fatalf("content = %+v", result.Content)
	}
}

func TestPiMessagesHTTPErrorDiagnostic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"fixture denied","code":"bad_request"}}`))
	}))
	defer server.Close()

	model := piMessagesTestModel(server.URL)
	events, result := drainAssistantStream(t, PiMessagesStream(&model, mistralTestTranscript(), &PiMessagesOptions{StreamOptions: testStreamOptions("fixture-key", nil)}))

	if strings.Join(events, ",") != "error" {
		t.Fatalf("events = %v", events)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Type != "pi_messages_response_failure" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	if result.Diagnostics[0].Details["status"] != float64(400) {
		t.Fatalf("diagnostic details = %+v", result.Diagnostics[0].Details)
	}
}

func TestPiMessagesAborted(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("content-type", "text/event-stream")
		w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	signal := make(chan struct{})
	close(signal)
	model := piMessagesTestModel(server.URL)
	events, result := drainAssistantStream(t, PiMessagesStream(&model, mistralTestTranscript(), &PiMessagesOptions{StreamOptions: testStreamOptions("fixture-key", signal)}))

	if requests != 0 {
		t.Fatalf("aborted stream must not reach the server, got %d requests", requests)
	}
	if strings.Join(events, ",") != "error" {
		t.Fatalf("events = %v", events)
	}
	if result.StopReason != types.StopReasonAborted {
		t.Fatalf("stopReason = %q", result.StopReason)
	}
}

func TestPiMessagesResolveCacheRetention(t *testing.T) {
	long := types.CacheRetentionLong
	env := types.ProviderEnv{"PI_CACHE_RETENTION": strPtr("long")}
	if resolved := resolvePiCacheRetention(nil, env); resolved == nil || *resolved != types.CacheRetentionLong {
		t.Fatalf("env opt-in not mapped: %v", resolved)
	}
	none := types.CacheRetentionNone
	if resolved := resolvePiCacheRetention(&none, env); resolved == nil || *resolved != none {
		t.Fatalf("explicit retention must win: %v", resolved)
	}
	_ = long
}

func strPtr(value string) *string { return &value }

func openRouterImagesTestModel(baseURL string) types.ImagesModel {
	return types.ImagesModel{
		Model: types.Model{
			Id:        "openrouter/test",
			Name:      "Test",
			Api:       types.ApiOpenRouterImages,
			Provider:  types.ProviderId("openrouter"),
			BaseUrl:   baseURL,
			Input:     []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
			Cost:      types.ModelCost{ModelCostRates: types.ModelCostRates{Input: 1, Output: 2, CacheRead: 0.5, CacheWrite: 0.25}},
			MaxTokens: 1024,
		},
		Output: []types.ImagesModelOutputModality{types.ImagesModelOutputImage, types.ImagesModelOutputText},
	}
}

func TestOpenRouterImagesGenerate(t *testing.T) {
	responseBody := `{"id":"gen-1","usage":{"prompt_tokens":10,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3,"cache_write_tokens":1}},"choices":[{"message":{"content":"a red circle","images":[{"image_url":{"url":"data:image/png;base64,QUJD"}},{"image_url":"https://example.com/ignored.png"}]}}]}`
	var recordedPath string
	var recordedBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recordedPath = r.URL.RequestURI()
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &recordedBody)
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(responseBody))
	}))
	defer server.Close()

	model := openRouterImagesTestModel(server.URL)
	apiKey := "fixture-key"
	context := &types.ImagesContext{Input: []types.ContentBlock{types.TextBlock("draw a circle")}}
	output, err := GenerateOpenRouterImages(&model, context, &types.ImagesOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if recordedPath != "/chat/completions" {
		t.Fatalf("path = %q", recordedPath)
	}
	if recordedBody["stream"] != false {
		t.Fatalf("stream must be false: %+v", recordedBody["stream"])
	}
	if output.StopReason != types.ImagesStopReasonStop || output.ResponseId == nil || *output.ResponseId != "gen-1" {
		t.Fatalf("output = %+v", output)
	}
	if len(output.Output) != 2 {
		t.Fatalf("output blocks = %+v", output.Output)
	}
	if output.Output[0].Text == nil || output.Output[0].Text.Text != "a red circle" {
		t.Fatalf("text block = %+v", output.Output[0])
	}
	if output.Output[1].Image == nil || output.Output[1].Image.Data != "QUJD" || output.Output[1].Image.MimeType != "image/png" {
		t.Fatalf("image block = %+v", output.Output[1])
	}
	if output.Usage == nil {
		t.Fatal("usage missing")
	}
	if output.Usage.Input != 7 || output.Usage.CacheRead != 2 || output.Usage.CacheWrite != 1 || output.Usage.TotalTokens != 14 {
		t.Fatalf("usage = %+v", output.Usage)
	}
	if output.Usage.Cost.Input == 0 || output.Usage.Cost.Total == 0 {
		t.Fatalf("cost = %+v", output.Usage.Cost)
	}
}

func TestOpenRouterImagesHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer server.Close()

	model := openRouterImagesTestModel(server.URL)
	apiKey := "fixture-key"
	context := &types.ImagesContext{Input: []types.ContentBlock{types.TextBlock("draw")}}
	output, err := GenerateOpenRouterImages(&model, context, &types.ImagesOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey}})
	if err != nil {
		t.Fatalf("generate should encode failure, got %v", err)
	}
	if output.StopReason != types.ImagesStopReasonError {
		t.Fatalf("stopReason = %q", output.StopReason)
	}
	if output.ErrorMessage == nil || !strings.Contains(*output.ErrorMessage, "rate limited") {
		t.Fatalf("errorMessage = %v", output.ErrorMessage)
	}
	if len(output.Output) != 0 {
		t.Fatalf("output should be empty: %+v", output.Output)
	}
}

func TestOpenRouterImagesMissingKey(t *testing.T) {
	model := openRouterImagesTestModel("http://127.0.0.1:1")
	context := &types.ImagesContext{Input: []types.ContentBlock{types.TextBlock("draw")}}
	output, err := GenerateOpenRouterImages(&model, context, nil)
	if err != nil {
		t.Fatalf("generate should encode failure, got %v", err)
	}
	if output.StopReason != types.ImagesStopReasonError || output.ErrorMessage == nil {
		t.Fatalf("output = %+v", output)
	}
}

func TestLazyOtherProtocolApis(t *testing.T) {
	if MistralConversationsApi() == nil || PiMessagesApi() == nil || OpenRouterImagesApi() == nil {
		t.Fatal("lazy API factories must return non-nil implementations")
	}
	if _, err := MistralConversationsApi().FetchDeferred(nil, types.DeferredHandle{}, nil); err == nil {
		t.Fatal("mistral must not support deferred responses")
	}
	if err := PiMessagesApi().CancelDeferred(nil, types.DeferredHandle{}, nil); err == nil {
		t.Fatal("pi-messages must not support deferred cancellation")
	}
}
