package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
)

func testModel(apiName types.Api, baseURL string) types.Model {
	return types.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           apiName,
		Provider:      types.ProviderId("fixture"),
		BaseUrl:       baseURL,
		Input:         []types.ModelInputModality{types.ModelInputText, types.ModelInputImage},
		ContextWindow: 8192,
		MaxTokens:     1024,
	}
}

func testTranscript() *types.TranscriptContext {
	return types.NewTranscriptContext([]types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	})
}

func drainStream(t *testing.T, stream *types.AssistantMessageEventStream) ([]string, types.AssistantMessage) {
	t.Helper()
	events := []string{}
	for {
		item, ok := <-stream.Next()
		if !ok || item.Done {
			break
		}
		events = append(events, string(item.Value.Type))
	}
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}
	return events, result
}

func sseServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			w.Header().Set("content-type", "application/json")
		} else {
			w.Header().Set("content-type", "text/event-stream")
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
}

func TestClampOpenAIPromptCacheKeyCountsCodePoints(t *testing.T) {
	key := ""
	for i := 0; i < OpenAIPromptCacheKeyMaxLength+5; i++ {
		key += "\u4f60"
	}
	clamped := ClampOpenAIPromptCacheKey(&key)
	if clamped == nil {
		t.Fatal("expected a clamped key")
	}
	if got := len([]rune(*clamped)); got != OpenAIPromptCacheKeyMaxLength {
		t.Fatalf("clamped rune length = %d, want %d", got, OpenAIPromptCacheKeyMaxLength)
	}
	if ClampOpenAIPromptCacheKey(nil) != nil {
		t.Fatal("a nil key must stay nil")
	}
}

func TestCopilotHeaders(t *testing.T) {
	messages := []types.Message{
		types.NewUserMessageVariant(types.NewUserMessage("hi", 1)),
		types.NewAssistantMessageVariant(types.NewAssistantMessage(types.ApiOpenAICompletions, types.ProviderGitHubCopilot, "m", 2)),
	}
	if got := InferCopilotInitiator(messages); got != CopilotInitiatorAgent {
		t.Fatalf("initiator = %q, want agent", got)
	}
	headers := BuildCopilotDynamicHeaders(messages, false)
	if headers["X-Initiator"] != "agent" || headers["Openai-Intent"] != "conversation-edits" {
		t.Fatalf("unexpected headers: %#v", headers)
	}
	if _, present := headers["Copilot-Vision-Request"]; present {
		t.Fatal("vision header must be omitted without images")
	}

	imageMessages := []types.Message{
		types.NewUserMessageVariant(types.NewUserMessageBlocks([]types.ContentBlock{types.ImageBlock("AAAA", "image/png")}, 1)),
	}
	if !HasCopilotVisionInput(imageMessages) {
		t.Fatal("expected vision input to be detected")
	}
	if BuildCopilotDynamicHeaders(imageMessages, true)["Copilot-Vision-Request"] != "true" {
		t.Fatal("expected the vision request header")
	}
}

func TestOpenAICompletionsStreamCapturedSSE(t *testing.T) {
	body := "event: message\n" +
		"data: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"event: message\n" +
		"data: {\"id\":\"r1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	server := sseServer(t, body, 200)
	defer server.Close()

	model := testModel(types.ApiOpenAICompletions, server.URL)
	apiKey := "fixture-key"
	maxTokens := 16
	temperature := 0.0
	transport := types.TransportSSE
	stream := OpenAICompletionsStream(&model, testTranscript(), &OpenAICompletionsOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey},
			MaxTokens:              &maxTokens,
			Temperature:            &temperature,
			Transport:              &transport,
		},
	})
	events, result := drainStream(t, stream)
	wantEvents := []string{"start", "text_start", "text_delta", "text_end", "done"}
	if len(events) != len(wantEvents) {
		t.Fatalf("events = %v, want %v", events, wantEvents)
	}
	for i := range wantEvents {
		if events[i] != wantEvents[i] {
			t.Fatalf("events = %v, want %v", events, wantEvents)
		}
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stopReason = %q, want stop", result.StopReason)
	}
	if result.ResponseId == nil || *result.ResponseId != "r1" {
		t.Fatalf("responseId = %v, want r1", result.ResponseId)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || result.Content[0].Text.Text != "hi" {
		t.Fatalf("content = %#v", result.Content)
	}
	if result.Usage.Input != 2 || result.Usage.Output != 1 || result.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestOpenAIResponsesStreamCapturedSSE(t *testing.T) {
	body := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\",\"model\":\"fixture\",\"output\":[],\"status\":\"in_progress\"}}\n\n" +
		"event: response.output_item.added\n" +
		"data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"id\":\"msg1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"in_progress\",\"content\":[]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"msg1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\",\"annotations\":[]}]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"model\":\"fixture\",\"status\":\"completed\",\"output\":[{\"id\":\"msg1\",\"type\":\"message\",\"role\":\"assistant\",\"status\":\"completed\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"
	server := sseServer(t, body, 200)
	defer server.Close()

	model := testModel(types.ApiOpenAIResponses, server.URL)
	apiKey := "fixture-key"
	maxTokens := 16
	transport := types.TransportSSE
	stream := OpenAIResponsesStream(&model, testTranscript(), &OpenAIResponsesOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey},
			MaxTokens:              &maxTokens,
			Transport:              &transport,
		},
	})
	events, result := drainStream(t, stream)
	wantEvents := []string{"start", "text_start", "text_delta", "text_end", "done"}
	for i := range wantEvents {
		if i >= len(events) || events[i] != wantEvents[i] {
			t.Fatalf("events = %v, want %v", events, wantEvents)
		}
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stopReason = %q, want stop", result.StopReason)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil {
		t.Fatalf("content = %#v", result.Content)
	}
	if result.Content[0].Text.Text != "hi" {
		t.Fatalf("text = %q", result.Content[0].Text.Text)
	}
	if result.Content[0].Text.TextSignature == nil {
		t.Fatal("expected a text signature")
	}
	if result.Usage.Input != 2 || result.Usage.Output != 1 {
		t.Fatalf("usage = %#v", result.Usage)
	}
}

func TestOpenAICompletionsHTTPErrorIsClassified(t *testing.T) {
	server := sseServer(t, `{"error":{"message":"denied"}}`, 400)
	defer server.Close()

	model := testModel(types.ApiOpenAICompletions, server.URL)
	apiKey := "fixture-key"
	stream := OpenAICompletionsStream(&model, testTranscript(), &OpenAICompletionsOptions{
		StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey}},
	})
	events, result := drainStream(t, stream)
	if len(events) != 1 || events[0] != "error" {
		t.Fatalf("events = %v, want [error]", events)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("stopReason = %q, want error", result.StopReason)
	}
	if len(result.Content) != 0 {
		t.Fatalf("content = %#v, want empty", result.Content)
	}
}

func TestConvertResponsesMessagesShape(t *testing.T) {
	model := testModel(types.ApiOpenAIResponses, "http://localhost")
	includeSystem := true
	messages := ConvertResponsesMessages(&model, testTranscript(), openAIToolCallProviders, &ConvertResponsesMessagesOptions{IncludeSystemPrompt: &includeSystem})
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0]["role"] != "system" || messages[0]["content"] != "system" {
		t.Fatalf("system item = %#v", messages[0])
	}
	content, ok := messages[1]["content"].([]map[string]any)
	if !ok || len(content) != 1 || content[0]["type"] != "input_text" || content[0]["text"] != "hello" {
		t.Fatalf("user item = %#v", messages[1])
	}
}

func TestConvertMessagesUserText(t *testing.T) {
	model := testModel(types.ApiOpenAICompletions, "http://localhost")
	compat := getOpenAICompletionsCompat(&model)
	messages := ConvertMessages(&model, testTranscript(), compat, nil)
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	if messages[0]["role"] != "system" || messages[0]["content"] != "system" {
		t.Fatalf("system = %#v", messages[0])
	}
	if messages[1]["role"] != "user" || messages[1]["content"] != "hello" {
		t.Fatalf("user = %#v", messages[1])
	}
	encoded, err := json.Marshal(messages)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("marshal messages: %v", err)
	}
}
