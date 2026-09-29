package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"

	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
)

// This file translates the upstream Bedrock tests into deterministic Go
// self-tests. Live credential/cloud tests become fake HTTP event-stream servers
// and injected transports; the limitation is that no real model call runs.

func bedrockFrame(t *testing.T, eventType string, body any) []byte {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal fixture event: %v", err)
	}
	var headers eventstream.Headers
	headers.Set(":message-type", eventstream.StringValue("event"))
	headers.Set(":event-type", eventstream.StringValue(eventType))
	headers.Set(":content-type", eventstream.StringValue("application/json"))
	var buffer bytes.Buffer
	if err := eventstream.NewEncoder().Encode(&buffer, eventstream.Message{Headers: headers, Payload: payload}); err != nil {
		t.Fatalf("encode fixture event: %v", err)
	}
	return buffer.Bytes()
}

func bedrockEventBytes(t *testing.T, events []struct {
	name string
	body any
}) []byte {
	t.Helper()
	var out []byte
	for _, event := range events {
		out = append(out, bedrockFrame(t, event.name, event.body)...)
	}
	return out
}

type bedrockCapture struct {
	mu      sync.Mutex
	headers []http.Header
	bodies  []map[string]any
}

func (c *bedrockCapture) lastHeader(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.headers) == 0 {
		return ""
	}
	return c.headers[len(c.headers)-1].Get(name)
}

func (c *bedrockCapture) lastBody() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.bodies) == 0 {
		return nil
	}
	return c.bodies[len(c.bodies)-1]
}

func newBedrockServer(t *testing.T, status int, response []byte) (*httptest.Server, *bedrockCapture) {
	t.Helper()
	capture := &bedrockCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &decoded)
		}
		capture.mu.Lock()
		capture.headers = append(capture.headers, r.Header.Clone())
		capture.bodies = append(capture.bodies, decoded)
		capture.mu.Unlock()

		contentType := "application/vnd.amazon.eventstream"
		if status != 200 {
			contentType = "application/json"
		}
		w.Header().Set("content-type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)
	return server, capture
}

func bedrockModel(serverURL string, mutate ...func(*types.Model)) *types.Model {
	model := &types.Model{
		Id:            "us.anthropic.claude-opus-4-8",
		Name:          "Claude Opus 4.8",
		Api:           types.ApiBedrockConverseStream,
		Provider:      types.ProviderAmazonBedrock,
		BaseUrl:       serverURL,
		Reasoning:     true,
		Input:         []types.ModelInputModality{types.ModelInputText},
		ContextWindow: 200000,
		MaxTokens:     32000,
		Cost: types.ModelCost{
			ModelCostRates: types.ModelCostRates{Input: 15, Output: 75, CacheRead: 1.5, CacheWrite: 18.75},
		},
	}
	for _, apply := range mutate {
		apply(model)
	}
	return model
}

func bedrockOptions(apiKey string) *api.BedrockOptions {
	region := "us-east-1"
	maxTokens := 16
	temperature := 0.0
	return &api.BedrockOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &apiKey},
			CacheRetention:         cacheRetentionPointer(types.CacheRetentionNone),
			MaxTokens:              &maxTokens,
			Temperature:            &temperature,
		},
		BearerToken: &apiKey,
		Region:      &region,
	}
}

func cacheRetentionPointer(value types.CacheRetention) *types.CacheRetention { return &value }

func drainBedrockStream(t *testing.T, stream *types.AssistantMessageEventStream) ([]types.AssistantMessageEventType, types.AssistantMessage) {
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

func bedrockTranscript() *types.TranscriptContext {
	return types.NewTranscriptContext([]types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage("system", 1)),
		types.NewUserMessageVariant(types.NewUserMessage("hello", 2)),
	})
}

// =============================================================================
// bedrock-raw-stop-reason.test.ts
// =============================================================================

func TestBedrockRawStopReasonSuccess(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
	server, _ := newBedrockServer(t, 200, response)

	_, result := drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), bedrockOptions("fixture-key")))

	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stopReason = %q, want stop", result.StopReason)
	}
	if result.RawStopReason == nil || *result.RawStopReason != "end_turn" {
		t.Fatalf("rawStopReason = %v, want end_turn", result.RawStopReason)
	}
	if result.ErrorMessage != nil {
		t.Fatalf("errorMessage = %v, want nil", result.ErrorMessage)
	}
}

func TestBedrockRawStopReasonProviderError(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"messageStop", map[string]any{"stopReason": "guardrail_intervened"}},
	})
	server, _ := newBedrockServer(t, 200, response)

	seen, result := drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), bedrockOptions("fixture-key")))

	if result.StopReason != types.StopReasonError {
		t.Fatalf("stopReason = %q, want error", result.StopReason)
	}
	if result.RawStopReason == nil || *result.RawStopReason != "guardrail_intervened" {
		t.Fatalf("rawStopReason = %v, want guardrail_intervened", result.RawStopReason)
	}
	if result.ErrorMessage == nil || *result.ErrorMessage != "Provider stopped with: guardrail_intervened" {
		t.Fatalf("errorMessage = %v", result.ErrorMessage)
	}
	if len(seen) == 0 || seen[len(seen)-1] != types.AssistantEventError {
		t.Fatalf("events = %v, want terminal error", seen)
	}
}

// =============================================================================
// bedrock-cache-write-1h-cost.test.ts
// =============================================================================

func TestBedrockCacheWrite1hCost(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"metadata", map[string]any{
			"usage": map[string]any{
				"inputTokens":           100,
				"outputTokens":          5,
				"totalTokens":           1000105,
				"cacheWriteInputTokens": 1000000,
				"cacheDetails": []map[string]any{
					{"ttl": "1h", "inputTokens": 150000},
					{"ttl": "5m", "inputTokens": 600000},
					{"ttl": "1h", "inputTokens": 250000},
				},
			},
		}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
	server, _ := newBedrockServer(t, 200, response)

	model := bedrockModel(server.URL)
	_, result := drainBedrockStream(t, api.BedrockConverseStream(model, bedrockTranscript(), bedrockOptions("fixture-key")))

	if result.Usage.CacheWrite != 1000000 {
		t.Fatalf("cacheWrite = %v, want 1000000", result.Usage.CacheWrite)
	}
	if result.Usage.CacheWrite1h == nil || *result.Usage.CacheWrite1h != 400000 {
		t.Fatalf("cacheWrite1h = %v, want 400000", result.Usage.CacheWrite1h)
	}
	expected := (600000*model.Cost.CacheWrite + 400000*model.Cost.Input*2) / 1000000
	if math.Abs(result.Usage.Cost.CacheWrite-expected) > 1e-9 {
		t.Fatalf("cacheWrite cost = %v, want %v", result.Usage.Cost.CacheWrite, expected)
	}
}

// =============================================================================
// bedrock-custom-headers.test.ts
// =============================================================================

func TestBedrockCustomHeadersApplied(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
	server, capture := newBedrockServer(t, 200, response)

	options := bedrockOptions("fixture-key")
	options.Headers = types.ProviderHeaders{"x-custom": stringPointer("v")}
	drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), options))

	if got := capture.lastHeader("x-custom"); got != "v" {
		t.Fatalf("x-custom = %q, want v", got)
	}
	if got := capture.lastHeader("Authorization"); got != "Bearer fixture-key" {
		t.Fatalf("Authorization = %q, want bearer token", got)
	}
}

func TestBedrockCustomHeadersSkipReserved(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
	server, capture := newBedrockServer(t, 200, response)

	options := bedrockOptions("fixture-key")
	options.Headers = types.ProviderHeaders{
		"authorization": stringPointer("evil"),
		"x-amz-date":    stringPointer("evil"),
		"x-allowed":     stringPointer("ok"),
		"Authorization": stringPointer("evil2"),
		"X-Amz-Date":    stringPointer("evil2"),
		"HOST":          stringPointer("evil3"),
	}
	drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), options))

	if got := capture.lastHeader("x-allowed"); got != "ok" {
		t.Fatalf("x-allowed = %q, want ok", got)
	}
	if got := capture.lastHeader("Authorization"); got != "Bearer fixture-key" {
		t.Fatalf("Authorization = %q, reserved header must be preserved", got)
	}
	if got := capture.lastHeader("X-Amz-Date"); got != "" {
		t.Fatalf("X-Amz-Date = %q, reserved header must be skipped", got)
	}
}

func TestBedrockSimpleForwardsHeaders(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
	server, capture := newBedrockServer(t, 200, response)

	apiKey := "fixture-key"
	options := &types.SimpleStreamOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{
				APIKey:  &apiKey,
				Headers: types.ProviderHeaders{"x-custom": stringPointer("v")},
			},
			CacheRetention: cacheRetentionPointer(types.CacheRetentionNone),
		},
	}
	drainBedrockStream(t, api.BedrockConverseStreamSimple(bedrockModel(server.URL), bedrockTranscript(), options))

	if got := capture.lastHeader("x-custom"); got != "v" {
		t.Fatalf("x-custom = %q, want v", got)
	}
}

// =============================================================================
// Protocol translation self-tests
// =============================================================================

func TestBedrockTextProtocol(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{}}},
		{"contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "你好"}}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
		{"metadata", map[string]any{"usage": map[string]any{"inputTokens": 2, "outputTokens": 1, "totalTokens": 3}}},
	})
	server, capture := newBedrockServer(t, 200, response)

	seen, result := drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), bedrockOptions("fixture-key")))

	wantEvents := []types.AssistantMessageEventType{
		types.AssistantEventStart,
		types.AssistantEventTextStart,
		types.AssistantEventTextDelta,
		types.AssistantEventTextEnd,
		types.AssistantEventDone,
	}
	if len(seen) != len(wantEvents) {
		t.Fatalf("events = %v, want %v", seen, wantEvents)
	}
	for i := range wantEvents {
		if seen[i] != wantEvents[i] {
			t.Fatalf("events = %v, want %v", seen, wantEvents)
		}
	}
	if len(result.Content) != 1 || result.Content[0].Type != types.ContentTypeText || result.Content[0].Text == nil || result.Content[0].Text.Text != "你好" {
		t.Fatalf("content = %+v", result.Content)
	}
	if result.Usage.Input != 2 || result.Usage.Output != 1 || result.Usage.TotalTokens != 3 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stopReason = %q", result.StopReason)
	}

	body := capture.lastBody()
	if body == nil {
		t.Fatal("missing request body")
	}
	messages, _ := body["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %+v", body["messages"])
	}
	firstMessage, _ := messages[0].(map[string]any)
	if firstMessage["role"] != "user" {
		t.Fatalf("first message role = %v", firstMessage["role"])
	}
	system, _ := body["system"].([]any)
	if len(system) != 1 {
		t.Fatalf("system = %+v", body["system"])
	}
	inference, _ := body["inferenceConfig"].(map[string]any)
	if inference["maxTokens"] != float64(16) {
		t.Fatalf("inferenceConfig = %+v", inference)
	}
}

func TestBedrockHTTPErrorClassifiesError(t *testing.T) {
	server, _ := newBedrockServer(t, 400, []byte(`{"message":"fixture denied"}`))

	seen, result := drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), bedrockOptions("fixture-key")))

	if len(seen) != 1 || seen[0] != types.AssistantEventError {
		t.Fatalf("events = %v, want error", seen)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("stopReason = %q, want error", result.StopReason)
	}
	if result.ErrorMessage == nil || *result.ErrorMessage == "" {
		t.Fatalf("errorMessage = %v", result.ErrorMessage)
	}
}

func TestBedrockCancelledBeforeSend(t *testing.T) {
	server, capture := newBedrockServer(t, 200, nil)

	closed := make(chan struct{})
	close(closed)
	options := bedrockOptions("fixture-key")
	options.Signal = closed

	seen, result := drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), options))

	if len(seen) != 1 || seen[0] != types.AssistantEventError {
		t.Fatalf("events = %v, want error", seen)
	}
	if result.StopReason != types.StopReasonAborted {
		t.Fatalf("stopReason = %q, want aborted", result.StopReason)
	}
	capture.mu.Lock()
	count := len(capture.headers)
	capture.mu.Unlock()
	if count != 0 {
		t.Fatalf("expected no HTTP request when the signal is already aborted, got %d", count)
	}
}

// =============================================================================
// Reasoning and tool-stream translation
// =============================================================================

func TestBedrockAdaptiveThinkingRequest(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
	server, capture := newBedrockServer(t, 200, response)

	options := bedrockOptions("fixture-key")
	reasoning := types.ThinkingHigh
	display := api.BedrockThinkingDisplaySummarized
	options.Reasoning = &reasoning
	options.ThinkingDisplay = &display
	drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), options))

	body := capture.lastBody()
	fields, _ := body["additionalModelRequestFields"].(map[string]any)
	thinking, _ := fields["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %+v, want adaptive", thinking)
	}
	if thinking["display"] != "summarized" {
		t.Fatalf("display = %v, want summarized", thinking["display"])
	}
	outputConfig, _ := fields["output_config"].(map[string]any)
	if outputConfig["effort"] != "high" {
		t.Fatalf("effort = %v, want high", outputConfig["effort"])
	}
}

func TestBedrockToolCallStream(t *testing.T) {
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockStart", map[string]any{"contentBlockIndex": 0, "start": map[string]any{"toolUse": map[string]any{"toolUseId": "call-1", "name": "echo"}}}},
		{"contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": "{\"n\":"}}}},
		{"contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"toolUse": map[string]any{"input": "1}"}}}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "tool_use"}},
	})
	server, _ := newBedrockServer(t, 200, response)

	seen, result := drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), bedrockOptions("fixture-key")))

	for _, expected := range []types.AssistantMessageEventType{types.AssistantEventToolCallStart, types.AssistantEventToolCallDelta, types.AssistantEventToolCallEnd} {
		found := false
		for _, event := range seen {
			if event == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("events = %v, missing %s", seen, expected)
		}
	}
	if result.StopReason != types.StopReasonToolUse {
		t.Fatalf("stopReason = %q, want toolUse", result.StopReason)
	}
	if len(result.Content) != 1 || result.Content[0].Type != types.ContentTypeToolCall || result.Content[0].ToolCall == nil {
		t.Fatalf("content = %+v", result.Content)
	}
	call := result.Content[0].ToolCall
	if call.Id != "call-1" || call.Name != "echo" {
		t.Fatalf("tool call = %+v", call)
	}
	var arguments map[string]any
	if err := json.Unmarshal(call.Arguments, &arguments); err != nil {
		t.Fatalf("arguments = %s: %v", call.Arguments, err)
	}
	if arguments["n"] != float64(1) {
		t.Fatalf("arguments = %v", arguments)
	}
}

func TestBedrockThinkingAndRedaction(t *testing.T) {
	redacted := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03, 0x04})
	response := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"text": "思考"}}}},
		{"contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"signature": "sig"}}}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
	server, _ := newBedrockServer(t, 200, response)

	seen, result := drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(server.URL), bedrockTranscript(), bedrockOptions("fixture-key")))

	for _, expected := range []types.AssistantMessageEventType{types.AssistantEventThinkingStart, types.AssistantEventThinkingDelta, types.AssistantEventThinkingEnd} {
		found := false
		for _, event := range seen {
			if event == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("events = %v, missing %s", seen, expected)
		}
	}
	if len(result.Content) != 1 || result.Content[0].Type != types.ContentTypeThinking || result.Content[0].Thinking == nil {
		t.Fatalf("content = %+v", result.Content)
	}
	thinking := result.Content[0].Thinking
	if thinking.Thinking != "思考" {
		t.Fatalf("thinking = %q", thinking.Thinking)
	}
	if thinking.ThinkingSignature == nil || *thinking.ThinkingSignature != "sig" {
		t.Fatalf("signature = %v", thinking.ThinkingSignature)
	}

	// A redacted reasoning delta stores the opaque bytes as base64 in the
	// thinking signature and marks the block redacted.
	redactedResponse := bedrockEventBytes(t, []struct {
		name string
		body any
	}{
		{"messageStart", map[string]any{"role": "assistant"}},
		{"contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"reasoningContent": map[string]any{"redactedContent": redacted}}}},
		{"contentBlockStop", map[string]any{"contentBlockIndex": 0}},
		{"messageStop", map[string]any{"stopReason": "end_turn"}},
	})
	redactedServer, _ := newBedrockServer(t, 200, redactedResponse)
	_, redactedResult := drainBedrockStream(t, api.BedrockConverseStream(bedrockModel(redactedServer.URL), bedrockTranscript(), bedrockOptions("fixture-key")))
	if len(redactedResult.Content) != 1 || redactedResult.Content[0].Thinking == nil {
		t.Fatalf("content = %+v", redactedResult.Content)
	}
	redactedThinking := redactedResult.Content[0].Thinking
	if redactedThinking.Redacted == nil || !*redactedThinking.Redacted {
		t.Fatalf("redacted = %v", redactedThinking.Redacted)
	}
	if redactedThinking.ThinkingSignature == nil || *redactedThinking.ThinkingSignature != redacted {
		t.Fatalf("signature = %v, want %s", redactedThinking.ThinkingSignature, redacted)
	}
}
