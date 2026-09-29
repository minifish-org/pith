package api_test

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

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// This file translates the upstream Anthropic Messages tests into deterministic
// Go self-tests. Live credential/cloud tests become fake HTTP servers or
// injected payload captures; the limitation is that no real model call runs.

type anthropicRecordedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

type anthropicCapture struct {
	mu       sync.Mutex
	requests []anthropicRecordedRequest
	headers  []http.Header
}

func (c *anthropicCapture) lastBody() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requests) == 0 {
		return nil
	}
	return c.requests[len(c.requests)-1].Body
}

func (c *anthropicCapture) lastHeader(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.headers) == 0 {
		return ""
	}
	return c.headers[len(c.headers)-1].Get(name)
}

func (c *anthropicCapture) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.requests)
}

func newAnthropicServer(t *testing.T, status int, body string) (*httptest.Server, *anthropicCapture) {
	t.Helper()
	capture := &anthropicCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &decoded)
		}
		capture.mu.Lock()
		capture.requests = append(capture.requests, anthropicRecordedRequest{Method: r.Method, Path: r.URL.RequestURI(), Body: decoded})
		capture.headers = append(capture.headers, r.Header.Clone())
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

func newAnthropicFragmentServer(t *testing.T, status int, body string) (*httptest.Server, *anthropicCapture) {
	t.Helper()
	capture := &anthropicCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &decoded)
		}
		capture.mu.Lock()
		capture.requests = append(capture.requests, anthropicRecordedRequest{Method: r.Method, Path: r.URL.RequestURI(), Body: decoded})
		capture.mu.Unlock()

		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(status)
		flusher, _ := w.(http.Flusher)
		for i := 0; i < len(body); i += 3 {
			end := i + 3
			if end > len(body) {
				end = len(body)
			}
			_, _ = w.Write([]byte(body[i:end]))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, capture
}

func anthropicModel(serverURL string, mutate ...func(*types.Model)) *types.Model {
	model := &types.Model{
		Id:            "claude-opus-4-8",
		Name:          "Claude Opus 4.8",
		Api:           types.ApiAnthropicMessages,
		Provider:      types.ProviderId("test-anthropic"),
		BaseUrl:       serverURL,
		Reasoning:     true,
		Input:         []types.ModelInputModality{types.ModelInputText},
		ContextWindow: 200000,
		MaxTokens:     32000,
		Cost: types.ModelCost{
			ModelCostRates: types.ModelCostRates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
		},
	}
	for _, apply := range mutate {
		apply(model)
	}
	return model
}

func anthropicOptions(apiKey string) types.StreamOptions {
	return types.StreamOptions{
		ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey},
	}
}

func drainAnthropicStream(t *testing.T, stream *types.AssistantMessageEventStream) ([]types.AssistantMessageEventType, types.AssistantMessage) {
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
	return seen, result
}

func anthropicTranscript(messages ...types.Message) *types.TranscriptContext {
	return types.NewTranscriptContext(messages)
}

const anthropicFixtureSSE = "event: message_start\n" +
	"data: {\"type\":\"message_start\",\"message\":{\"id\":\"r1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n" +
	"\n" +
	"event: content_block_start\n" +
	"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
	"\n" +
	"event: content_block_delta\n" +
	"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"你好\"}}\n" +
	"\n" +
	"event: content_block_stop\n" +
	"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
	"\n" +
	"event: message_delta\n" +
	"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n" +
	"\n" +
	"event: message_stop\n" +
	"data: {\"type\":\"message_stop\"}\n" +
	"\n"

func assertAnthropicFixtureEvents(t *testing.T, events []types.AssistantMessageEventType) {
	t.Helper()
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
}

func TestAnthropicMessagesTextStreaming(t *testing.T) {
	server, capture := newAnthropicServer(t, 200, anthropicFixtureSSE)
	model := anthropicModel(server.URL)
	stream := api.AnthropicMessagesStream(model, anthropicTranscript(
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	), &api.AnthropicOptions{StreamOptions: anthropicOptions("fixture-key")})
	events, result := drainAnthropicStream(t, stream)

	assertAnthropicFixtureEvents(t, events)
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if result.ResponseId == nil || *result.ResponseId != "r1" {
		t.Fatalf("responseId = %v", result.ResponseId)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || result.Content[0].Text.Text != "你好" {
		t.Fatalf("content = %+v", result.Content)
	}
	if result.Usage.Input != 2 || result.Usage.Output != 1 || result.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Usage.CacheWrite1h == nil || *result.Usage.CacheWrite1h != 0 {
		t.Fatalf("cacheWrite1h = %v", result.Usage.CacheWrite1h)
	}

	if capture.requestCount() == 0 {
		t.Fatal("no request captured")
	}
	if capture.requests[0].Path != "/v1/messages?beta=true" {
		t.Fatalf("path = %q", capture.requests[0].Path)
	}
	body := capture.lastBody()
	if body["stream"] != true {
		t.Fatalf("stream = %v", body["stream"])
	}
}

func TestAnthropicMessagesFragmentedStreaming(t *testing.T) {
	server, _ := newAnthropicFragmentServer(t, 200, anthropicFixtureSSE)
	model := anthropicModel(server.URL)
	stream := api.AnthropicMessagesStream(model, anthropicTranscript(
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	), &api.AnthropicOptions{StreamOptions: anthropicOptions("fixture-key")})
	events, result := drainAnthropicStream(t, stream)

	assertAnthropicFixtureEvents(t, events)
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || result.Content[0].Text.Text != "你好" {
		t.Fatalf("content = %+v", result.Content)
	}
}

func TestAnthropicMessagesHTTPError(t *testing.T) {
	server, capture := newAnthropicServer(t, 400, `{"error":{"message":"fixture denied","type":"invalid_request_error","code":"bad_request"}}`)
	model := anthropicModel(server.URL)
	stream := api.AnthropicMessagesStream(model, anthropicTranscript(
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	), &api.AnthropicOptions{StreamOptions: anthropicOptions("fixture-key")})
	events, result := drainAnthropicStream(t, stream)

	if len(events) != 1 || events[0] != types.AssistantEventError {
		t.Fatalf("events = %v", events)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if len(result.Content) != 0 {
		t.Fatalf("content = %+v", result.Content)
	}
	if result.Usage.CacheWrite1h != nil {
		t.Fatalf("cacheWrite1h should be absent, got %v", *result.Usage.CacheWrite1h)
	}
	if capture.requestCount() == 0 {
		t.Fatal("request was not sent")
	}
}

func TestAnthropicMessagesCancelled(t *testing.T) {
	server, capture := newAnthropicServer(t, 200, anthropicFixtureSSE)
	model := anthropicModel(server.URL)
	closed := make(chan struct{})
	close(closed)
	options := anthropicOptions("fixture-key")
	options.Signal = closed
	stream := api.AnthropicMessagesStream(model, anthropicTranscript(
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	), &api.AnthropicOptions{StreamOptions: options})
	events, result := drainAnthropicStream(t, stream)

	if len(events) != 1 || events[0] != types.AssistantEventError {
		t.Fatalf("events = %v", events)
	}
	if result.StopReason != types.StopReasonAborted {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if capture.requestCount() != 0 {
		t.Fatalf("expected no request, got %d", capture.requestCount())
	}
}

func TestAnthropicCacheWrite1hCost(t *testing.T) {
	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":100,\"output_tokens\":0,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":1000000,\"cache_creation\":{\"ephemeral_5m_input_tokens\":600000,\"ephemeral_1h_input_tokens\":400000}}}}\n" +
		"\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":100,\"output_tokens\":5,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":1000000}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	server, _ := newAnthropicServer(t, 200, sse)
	model := anthropicModel(server.URL)
	stream := api.AnthropicMessagesStream(model, anthropicTranscript(
		types.NewUserMessageVariant(types.NewUserMessage("hi", 1)),
	), &api.AnthropicOptions{StreamOptions: anthropicOptions("fixture-key")})
	_, result := drainAnthropicStream(t, stream)

	if result.Usage.CacheWrite != 1000000 {
		t.Fatalf("cacheWrite = %v", result.Usage.CacheWrite)
	}
	if result.Usage.CacheWrite1h == nil || *result.Usage.CacheWrite1h != 400000 {
		t.Fatalf("cacheWrite1h = %v", result.Usage.CacheWrite1h)
	}
	if diff := result.Usage.Cost.CacheWrite - 7.75; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cacheWrite cost = %v, want 7.75", result.Usage.Cost.CacheWrite)
	}
}

// =============================================================================
// Payload capture helpers
// =============================================================================

func captureAnthropicPayload(t *testing.T, model *types.Model, transcript *types.TranscriptContext, options *api.AnthropicOptions) map[string]any {
	t.Helper()
	var captured map[string]any
	options.OnPayload = func(payload any, _ *types.Model) (any, error) {
		if object, ok := payload.(map[string]any); ok {
			captured = object
		}
		return nil, errors.New("payload captured")
	}
	stream := api.AnthropicMessagesStream(model, transcript, options)
	drainAnthropicStream(t, stream)
	return captured
}

func captureAnthropicSimplePayload(t *testing.T, model *types.Model, transcript *types.TranscriptContext, options *types.SimpleStreamOptions) map[string]any {
	t.Helper()
	if options.APIKey == nil {
		key := "test-key"
		options.APIKey = &key
	}
	var captured map[string]any
	options.OnPayload = func(payload any, _ *types.Model) (any, error) {
		if object, ok := payload.(map[string]any); ok {
			captured = object
		}
		return nil, errors.New("payload captured")
	}
	stream := api.AnthropicMessagesStreamSimple(model, transcript, options)
	drainAnthropicStream(t, stream)
	return captured
}

func assistantContentFromPayload(payload map[string]any) []any {
	messages, _ := payload["messages"].([]map[string]any)
	for _, message := range messages {
		if message["role"] == "assistant" {
			content, _ := message["content"].([]any)
			return content
		}
	}
	return nil
}

func thinkingAssistant(provider types.ProviderId, modelId, thinking, signature string) types.Message {
	block := types.ContentBlock{Type: types.ContentTypeThinking, Thinking: &types.ThinkingContent{
		Type:              types.ContentTypeThinking,
		Thinking:          thinking,
		ThinkingSignature: &signature,
	}}
	return types.NewAssistantMessageVariant(types.AssistantMessage{
		Role:       types.AssistantMessageRole,
		Content:    []types.ContentBlock{block},
		Api:        types.ApiAnthropicMessages,
		Provider:   provider,
		Model:      modelId,
		Usage:      types.Usage{},
		StopReason: types.StopReasonStop,
		Timestamp:  1,
	})
}

// =============================================================================
// Empty thinking signature compatibility
// =============================================================================

func TestAnthropicEmptyThinkingSignatureCompat(t *testing.T) {
	fireworks := types.ProviderId("xiaomi-token-plan-ams")
	transcript := anthropicTranscript(
		types.NewUserMessageVariant(types.NewUserMessage("first", 1)),
		thinkingAssistant(fireworks, "mimo-v2.5-pro", "internal reasoning", ""),
		types.NewUserMessageVariant(types.NewUserMessage("second", 2)),
	)

	t.Run("converts empty-signature thinking to text by default", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL, func(m *types.Model) {
			m.Provider = fireworks
			m.Id = "mimo-v2.5-pro"
			m.Name = "MiMo-V2.5-Pro"
			m.MaxTokens = 1024
		})
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{})
		content := assistantContentFromPayload(payload)
		if len(content) != 1 {
			t.Fatalf("content = %#v", content)
		}
		block, _ := content[0].(map[string]any)
		if block["type"] != "text" || block["text"] != "internal reasoning" {
			t.Fatalf("block = %#v", block)
		}
	})

	t.Run("preserves empty-signature thinking when allowEmptySignature is enabled", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL, func(m *types.Model) {
			m.Provider = fireworks
			m.Id = "mimo-v2.5-pro"
			m.Name = "MiMo-V2.5-Pro"
			m.MaxTokens = 1024
			m.Compat.AnthropicMessages = &types.AnthropicMessagesCompat{AllowEmptySignature: boolPointer(true)}
		})
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{})
		content := assistantContentFromPayload(payload)
		if len(content) != 1 {
			t.Fatalf("content = %#v", content)
		}
		block, _ := content[0].(map[string]any)
		if block["type"] != "thinking" || block["thinking"] != "internal reasoning" || block["signature"] != "" {
			t.Fatalf("block = %#v", block)
		}
	})
}

func boolPointer(value bool) *bool { return &value }

// =============================================================================
// Eager tool input streaming
// =============================================================================

func TestAnthropicEagerToolInputCompat(t *testing.T) {
	tool := types.NewTool("lookup", "Look up a value", json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`))
	transcript := func() *types.TranscriptContext {
		return types.NewTranscriptContext([]types.Message{
			types.NewSystemMessageVariant(types.SystemMessage{
				Role:       types.SystemMessageRole,
				Content:    types.SystemContentText(""),
				ToolsAdded: []types.Tool{tool},
				Timestamp:  1,
			}),
			types.NewUserMessageVariant(types.NewUserMessage("Use the tool", 2)),
		})
	}

	t.Run("sends per-tool eager input streaming by default", func(t *testing.T) {
		server, capture := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL)
		stream := api.AnthropicMessagesStream(model, transcript(), &api.AnthropicOptions{StreamOptions: anthropicOptions("test-key")})
		drainAnthropicStream(t, stream)
		tools, _ := capture.lastBody()["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tools = %#v", tools)
		}
		first, _ := tools[0].(map[string]any)
		if first["eager_input_streaming"] != true {
			t.Fatalf("eager_input_streaming = %#v", first["eager_input_streaming"])
		}
		if beta := capture.lastHeader("anthropic-beta"); strings.Contains(beta, "fine-grained") {
			t.Fatalf("unexpected legacy beta: %q", beta)
		}
	})

	t.Run("uses the legacy beta when eager tool input streaming is disabled", func(t *testing.T) {
		server, capture := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL, func(m *types.Model) {
			m.Compat.AnthropicMessages = &types.AnthropicMessagesCompat{SupportsEagerToolInputStreaming: boolPointer(false)}
		})
		stream := api.AnthropicMessagesStream(model, transcript(), &api.AnthropicOptions{StreamOptions: anthropicOptions("test-key")})
		drainAnthropicStream(t, stream)
		tools, _ := capture.lastBody()["tools"].([]any)
		first, _ := tools[0].(map[string]any)
		if _, present := first["eager_input_streaming"]; present {
			t.Fatalf("eager_input_streaming should be absent")
		}
		if beta := capture.lastHeader("anthropic-beta"); beta != "fine-grained-tool-streaming-2025-05-14" {
			t.Fatalf("beta = %q", beta)
		}
	})
}

// =============================================================================
// Thinking payloads
// =============================================================================

func TestAnthropicThinkingDisablePayload(t *testing.T) {
	transcript := anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("Hello", 1)))

	t.Run("sends disabled for budget-based models", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL)
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{})
		thinking, _ := payload["thinking"].(map[string]any)
		if thinking["type"] != "disabled" {
			t.Fatalf("thinking = %#v", thinking)
		}
		if _, present := payload["output_config"]; present {
			t.Fatalf("output_config should be absent")
		}
	})

	t.Run("omits disabled when off maps to null", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL, func(m *types.Model) {
			m.ThinkingLevelMap = types.ThinkingLevelMap{types.ThinkingOff: nil}
		})
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{})
		if _, present := payload["thinking"]; present {
			t.Fatalf("thinking should be absent, got %#v", payload["thinking"])
		}
	})

	t.Run("uses adaptive thinking with effort when reasoning is enabled", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL, func(m *types.Model) {
			m.Compat.AnthropicMessages = &types.AnthropicMessagesCompat{ForceAdaptiveThinking: boolPointer(true)}
		})
		reasoning := types.ThinkingHigh
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{Reasoning: &reasoning})
		thinking, _ := payload["thinking"].(map[string]any)
		if thinking["type"] != "adaptive" || thinking["display"] != "summarized" {
			t.Fatalf("thinking = %#v", thinking)
		}
		outputConfig, _ := payload["output_config"].(map[string]any)
		if outputConfig["effort"] != "high" {
			t.Fatalf("output_config = %#v", outputConfig)
		}
	})
}

func TestAnthropicForceAdaptiveThinking(t *testing.T) {
	transcript := anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("Hello", 1)))

	t.Run("sends legacy thinking for custom model ids by default", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL, func(m *types.Model) {
			m.Id = "vendor--claude-opus-latest"
			m.BaseUrl = server.URL
		})
		reasoning := types.ThinkingMedium
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{Reasoning: &reasoning})
		thinking, _ := payload["thinking"].(map[string]any)
		if thinking["type"] != "enabled" {
			t.Fatalf("thinking = %#v", thinking)
		}
		if _, present := payload["output_config"]; present {
			t.Fatalf("output_config should be absent")
		}
	})

	t.Run("sends adaptive thinking with forceAdaptiveThinking", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL, func(m *types.Model) {
			m.Id = "vendor--claude-opus-latest"
			m.Compat.AnthropicMessages = &types.AnthropicMessagesCompat{ForceAdaptiveThinking: boolPointer(true)}
		})
		reasoning := types.ThinkingMedium
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{Reasoning: &reasoning})
		thinking, _ := payload["thinking"].(map[string]any)
		if thinking["type"] != "adaptive" {
			t.Fatalf("thinking = %#v", thinking)
		}
		outputConfig, _ := payload["output_config"].(map[string]any)
		if outputConfig["effort"] != "medium" {
			t.Fatalf("output_config = %#v", outputConfig)
		}
	})
}

func TestAnthropicTemperatureCompat(t *testing.T) {
	transcript := anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("Hello", 1)))

	t.Run("keeps temperature when supported", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL)
		temperature := 0.0
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{StreamOptions: types.StreamOptions{Temperature: &temperature}})
		if value, present := payload["temperature"]; !present || value != 0.0 {
			t.Fatalf("temperature = %#v", value)
		}
	})

	t.Run("omits temperature when disabled", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL, func(m *types.Model) {
			m.Compat.AnthropicMessages = &types.AnthropicMessagesCompat{SupportsTemperature: boolPointer(false)}
		})
		temperature := 0.0
		payload := captureAnthropicSimplePayload(t, model, transcript, &types.SimpleStreamOptions{StreamOptions: types.StreamOptions{Temperature: &temperature}})
		if _, present := payload["temperature"]; present {
			t.Fatalf("temperature should be absent")
		}
	})
}

// =============================================================================
// SSE edge cases
// =============================================================================

func TestAnthropicMalformedToolJSON(t *testing.T) {
	partial := "{\"path\":\"A\\H\",\"text\":\"col1\tcol2\"}"
	delta := map[string]any{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
	}
	deltaJSON, _ := json.Marshal(delta)

	sse := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":12,\"output_tokens\":0,\"cache_read_input_tokens\":0,\"cache_creation_input_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_test\",\"name\":\"edit\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: " + string(deltaJSON) + "\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":12,\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	server, _ := newAnthropicServer(t, 200, sse)
	model := anthropicModel(server.URL)
	tool := types.NewTool("edit", "Edit a file.", json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"text":{"type":"string"}}}`))
	transcript := types.NewTranscriptContext([]types.Message{
		types.NewSystemMessageVariant(types.SystemMessage{Role: types.SystemMessageRole, Content: types.SystemContentText(""), ToolsAdded: []types.Tool{tool}, Timestamp: 1}),
		types.NewUserMessageVariant(types.NewUserMessage("Use the edit tool.", 2)),
	})
	stream := api.AnthropicMessagesStream(model, transcript, &api.AnthropicOptions{StreamOptions: anthropicOptions("test-key")})
	_, result := drainAnthropicStream(t, stream)

	if result.StopReason != types.StopReasonToolUse {
		t.Fatalf("stop reason = %q (%v)", result.StopReason, result.ErrorMessage)
	}
	if result.ErrorMessage != nil {
		t.Fatalf("errorMessage = %v", *result.ErrorMessage)
	}
	var toolCall *types.ToolCall
	for i := range result.Content {
		if result.Content[i].Type == types.ContentTypeToolCall && result.Content[i].ToolCall != nil {
			toolCall = result.Content[i].ToolCall
		}
	}
	if toolCall == nil {
		t.Fatalf("no tool call in %#v", result.Content)
	}
	var arguments map[string]any
	if err := json.Unmarshal(toolCall.Arguments, &arguments); err != nil {
		t.Fatalf("arguments: %v", err)
	}
	if arguments["path"] != `A\H` {
		t.Fatalf("path = %#v", arguments["path"])
	}
	if arguments["text"] != "col1\tcol2" {
		t.Fatalf("text = %#v", arguments["text"])
	}
}

func TestAnthropicStopReasonDetails(t *testing.T) {
	t.Run("preserves refusal explanations", func(t *testing.T) {
		explanation := "This request triggered restrictions."
		sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_refusal\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
			"event: message_delta\ndata: " + mustJSON(map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "refusal", "stop_details": map[string]any{"type": "refusal", "category": "cyber", "explanation": explanation}},
		}) + "\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		server, _ := newAnthropicServer(t, 200, sse)
		model := anthropicModel(server.URL)
		stream := api.AnthropicMessagesStream(model, anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("blocked", 1))), &api.AnthropicOptions{StreamOptions: anthropicOptions("test-key")})
		_, result := drainAnthropicStream(t, stream)
		if result.StopReason != types.StopReasonError {
			t.Fatalf("stop reason = %q", result.StopReason)
		}
		if result.RawStopReason == nil || *result.RawStopReason != "refusal" {
			t.Fatalf("rawStopReason = %v", result.RawStopReason)
		}
		if result.ErrorMessage == nil || *result.ErrorMessage != explanation {
			t.Fatalf("errorMessage = %v", result.ErrorMessage)
		}
	})

	t.Run("preserves sensitive stop reasons", func(t *testing.T) {
		sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_sensitive\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"sensitive\"}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		server, _ := newAnthropicServer(t, 200, sse)
		model := anthropicModel(server.URL)
		stream := api.AnthropicMessagesStream(model, anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("blocked", 1))), &api.AnthropicOptions{StreamOptions: anthropicOptions("test-key")})
		_, result := drainAnthropicStream(t, stream)
		if result.StopReason != types.StopReasonError {
			t.Fatalf("stop reason = %q", result.StopReason)
		}
		if result.ErrorMessage == nil || *result.ErrorMessage != "Provider stopped with: sensitive" {
			t.Fatalf("errorMessage = %v", result.ErrorMessage)
		}
	})
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// =============================================================================
// Payload replacement and beta headers
// =============================================================================

func TestAnthropicOnPayloadForcesStreaming(t *testing.T) {
	server, capture := newAnthropicServer(t, 200, anthropicFixtureSSE)
	model := anthropicModel(server.URL)
	options := anthropicOptions("test-key")
	options.OnPayload = func(payload any, _ *types.Model) (any, error) {
		object, _ := payload.(map[string]any)
		replacement := map[string]any{}
		for key, value := range object {
			replacement[key] = value
		}
		replacement["stream"] = false
		return replacement, nil
	}
	stream := api.AnthropicMessagesStream(model, anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("Hello", 1))), &api.AnthropicOptions{StreamOptions: options})
	drainAnthropicStream(t, stream)
	if capture.lastBody()["stream"] != true {
		t.Fatalf("stream = %#v", capture.lastBody()["stream"])
	}
}

func TestAnthropicBetaHeaderHandling(t *testing.T) {
	t.Run("replaces the beta header", func(t *testing.T) {
		server, capture := newAnthropicServer(t, 200, anthropicFixtureSSE)
		model := anthropicModel(server.URL)
		options := anthropicOptions("test-key")
		options.Headers = types.ProviderHeaders{"anthropic-beta": stringPointer("custom-beta")}
		stream := api.AnthropicMessagesStream(model, anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("Hello", 1))), &api.AnthropicOptions{StreamOptions: options})
		drainAnthropicStream(t, stream)
		if beta := capture.lastHeader("anthropic-beta"); beta != "custom-beta" {
			t.Fatalf("beta = %q", beta)
		}
	})

	t.Run("suppresses the beta header", func(t *testing.T) {
		server, capture := newAnthropicServer(t, 200, anthropicFixtureSSE)
		model := anthropicModel(server.URL)
		options := anthropicOptions("test-key")
		options.Headers = types.ProviderHeaders{"anthropic-beta": nil}
		stream := api.AnthropicMessagesStream(model, anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("Hello", 1))), &api.AnthropicOptions{StreamOptions: options})
		drainAnthropicStream(t, stream)
		if beta := capture.lastHeader("anthropic-beta"); beta != "" {
			t.Fatalf("beta = %q", beta)
		}
	})

	t.Run("adds interleaved thinking by default", func(t *testing.T) {
		server, capture := newAnthropicServer(t, 200, anthropicFixtureSSE)
		model := anthropicModel(server.URL)
		enabled := true
		stream := api.AnthropicMessagesStream(model, anthropicTranscript(types.NewUserMessageVariant(types.NewUserMessage("Hello", 1))), &api.AnthropicOptions{
			StreamOptions:   anthropicOptions("test-key"),
			ThinkingEnabled: &enabled,
		})
		drainAnthropicStream(t, stream)
		if beta := capture.lastHeader("anthropic-beta"); !strings.Contains(beta, "interleaved-thinking-2025-05-14") {
			t.Fatalf("beta = %q", beta)
		}
	})
}

func stringPointer(value string) *string { return &value }

// =============================================================================
// OAuth tool-name normalization
// =============================================================================

func TestAnthropicOAuthToolNameNormalization(t *testing.T) {
	t.Run("sends canonical casing outbound", func(t *testing.T) {
		server, capture := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL)
		tool := types.NewTool("todowrite", "Write a todo item", json.RawMessage(`{"type":"object","properties":{"task":{"type":"string"}},"required":["task"]}`))
		transcript := types.NewTranscriptContext([]types.Message{
			types.NewSystemMessageVariant(types.SystemMessage{Role: types.SystemMessageRole, Content: types.SystemContentText(""), ToolsAdded: []types.Tool{tool}, Timestamp: 1}),
			types.NewUserMessageVariant(types.NewUserMessage("Add a todo", 2)),
		})
		stream := api.AnthropicMessagesStream(model, transcript, &api.AnthropicOptions{StreamOptions: anthropicOptions("sk-ant-oat-test")})
		drainAnthropicStream(t, stream)
		tools, _ := capture.lastBody()["tools"].([]any)
		first, _ := tools[0].(map[string]any)
		if first["name"] != "TodoWrite" {
			t.Fatalf("tool name = %#v", first["name"])
		}
	})

	t.Run("restores original casing inbound", func(t *testing.T) {
		sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"model\":\"claude-opus-4-8\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"TodoWrite\",\"input\":{\"task\":\"buy milk\"}}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		server, _ := newAnthropicServer(t, 200, sse)
		model := anthropicModel(server.URL)
		tool := types.NewTool("todowrite", "Write a todo item", json.RawMessage(`{"type":"object","properties":{"task":{"type":"string"}},"required":["task"]}`))
		transcript := types.NewTranscriptContext([]types.Message{
			types.NewSystemMessageVariant(types.SystemMessage{Role: types.SystemMessageRole, Content: types.SystemContentText(""), ToolsAdded: []types.Tool{tool}, Timestamp: 1}),
			types.NewUserMessageVariant(types.NewUserMessage("Add a todo", 2)),
		})
		stream := api.AnthropicMessagesStream(model, transcript, &api.AnthropicOptions{StreamOptions: anthropicOptions("sk-ant-oat-test")})
		_, result := drainAnthropicStream(t, stream)
		if len(result.Content) != 1 || result.Content[0].ToolCall == nil || result.Content[0].ToolCall.Name != "todowrite" {
			t.Fatalf("content = %#v", result.Content)
		}
	})
}

// =============================================================================
// Cache retention
// =============================================================================

func TestAnthropicCacheRetention(t *testing.T) {
	transcript := anthropicTranscript(
		types.NewSystemMessageVariant(types.NewSystemMessage("You are a helpful assistant.", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("Hello", 2)),
	)

	t.Run("defaults to short retention", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL)
		payload := captureAnthropicPayload(t, model, transcript, &api.AnthropicOptions{StreamOptions: anthropicOptions("test-key")})
		system, _ := payload["system"].([]any)
		first, _ := system[0].(map[string]any)
		control, _ := first["cache_control"].(map[string]any)
		if control["type"] != "ephemeral" {
			t.Fatalf("cache_control = %#v", control)
		}
		if _, present := control["ttl"]; present {
			t.Fatalf("ttl should be absent, got %#v", control["ttl"])
		}
	})

	t.Run("uses 1h ttl for long retention", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL)
		long := types.CacheRetentionLong
		payload := captureAnthropicPayload(t, model, transcript, &api.AnthropicOptions{StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{APIKey: stringPointer("test-key")},
			CacheRetention:         &long,
		}})
		system, _ := payload["system"].([]any)
		first, _ := system[0].(map[string]any)
		control, _ := first["cache_control"].(map[string]any)
		if control["ttl"] != "1h" {
			t.Fatalf("cache_control = %#v", control)
		}
	})

	t.Run("omits cache control when retention is none", func(t *testing.T) {
		server, _ := newAnthropicServer(t, 200, "")
		model := anthropicModel(server.URL)
		none := types.CacheRetentionNone
		payload := captureAnthropicPayload(t, model, transcript, &api.AnthropicOptions{StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{APIKey: stringPointer("test-key")},
			CacheRetention:         &none,
		}})
		system, _ := payload["system"].([]any)
		first, _ := system[0].(map[string]any)
		if _, present := first["cache_control"]; present {
			t.Fatalf("cache_control should be absent")
		}
	})
}
