package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	awsevents "github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/coder/websocket"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Reflection keeps the frozen judge compilable on the old public API, so a
// missing feature is a behavioral failure rather than a compiler failure.
func psObserve(t *testing.T, options any, callback func(any, *types.Model) error) {
	t.Helper()
	field := reflect.ValueOf(options).Elem().FieldByName("OnProviderStreamEvent")
	if !field.IsValid() || !field.CanSet() {
		t.Fatal("missing exported OnProviderStreamEvent callback")
	}
	value := reflect.ValueOf(callback)
	if !value.Type().AssignableTo(field.Type()) {
		t.Fatalf("callback signature: got %v, want %v", field.Type(), value.Type())
	}
	field.Set(value)
}

func psResult(t *testing.T, stream *types.AssistantMessageEventStream) types.AssistantMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("stream did not terminate: %v", err)
	}
	return result
}

func psSSE(events ...string) string {
	var body strings.Builder
	for _, event := range events {
		var object map[string]any
		if json.Unmarshal([]byte(event), &object) == nil {
			if kind, ok := object["type"].(string); ok {
				body.WriteString("event: " + kind + "\n")
			}
		}
		body.WriteString("data: " + event + "\n\n")
	}
	return body.String()
}

var psResponses = []string{
	`{"type":"response.created","ps_marker":1,"response":{"id":"r1","model":"fixture","output":[],"status":"in_progress"}}`,
	`{"type":"response.output_item.added","ps_marker":2,"output_index":0,"item":{"id":"msg1","type":"message","role":"assistant","status":"in_progress","content":[]}}`,
	`{"type":"response.output_text.delta","ps_marker":3,"output_index":0,"content_index":0,"delta":"hello"}`,
	`{"type":"response.output_item.done","ps_marker":4,"output_index":0,"item":{"id":"msg1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}}`,
	`{"type":"response.completed","ps_marker":5,"response":{"id":"r1","model":"fixture","status":"completed","output":[{"id":"msg1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
}

func psBedrock(t *testing.T) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, event := range []struct{ name, body string }{
		{"messageStart", `{"role":"assistant"}`},
		{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"hello"}}`},
		{"contentBlockStop", `{"contentBlockIndex":0}`},
		{"messageStop", `{"stopReason":"end_turn"}`},
		{"metadata", `{"usage":{"inputTokens":2,"outputTokens":1,"totalTokens":3},"metrics":{"latencyMs":17}}`},
	} {
		var headers awsevents.Headers
		headers.Set(":message-type", awsevents.StringValue("event"))
		headers.Set(":event-type", awsevents.StringValue(event.name))
		headers.Set(":content-type", awsevents.StringValue("application/json"))
		if err := awsevents.NewEncoder().Encode(&out, awsevents.Message{Headers: headers, Payload: []byte(event.body)}); err != nil {
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

type psProvider struct {
	name     string
	api      types.Api
	provider types.ProviderId
	body     []byte
	count    int
	marker   bool
}

func psProviders(t *testing.T) []psProvider {
	chat := psSSE(`{"ps_marker":1,"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}`, `{"ps_marker":2,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`, "[DONE]")
	anthropic := psSSE(
		`{"type":"message_start","ps_marker":1,"message":{"id":"r1","role":"assistant","content":[],"model":"fixture","usage":{"input_tokens":2,"output_tokens":0}}}`,
		`{"type":"content_block_start","ps_marker":2,"index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","ps_marker":3,"index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"content_block_stop","ps_marker":4,"index":0}`,
		`{"type":"message_delta","ps_marker":5,"delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop","ps_marker":6}`)
	google := psSSE(`{"candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]}}],"responseId":"g1"}`, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
	pi := psSSE(`{"type":"start","ps_marker":1}`, `{"type":"text_start","ps_marker":2,"contentIndex":0}`, `{"type":"text_delta","ps_marker":3,"contentIndex":0,"delta":"hello"}`, `{"type":"text_end","ps_marker":4,"contentIndex":0,"content":"hello"}`, `{"type":"done","ps_marker":5,"reason":"stop","usage":{"input":2,"output":1,"totalTokens":3,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}`)
	return []psProvider{
		{"completions", types.ApiOpenAICompletions, types.ProviderOpenAI, []byte(chat), 2, true},
		{"openrouter", types.ApiOpenAICompletions, types.ProviderOpenRouter, []byte(chat), 2, true},
		{"anthropic", types.ApiAnthropicMessages, types.ProviderAnthropic, []byte(anthropic), 6, true},
		{"responses", types.ApiOpenAIResponses, types.ProviderOpenAI, []byte(psSSE(psResponses...)), 5, true},
		{"azure", types.ApiAzureOpenAIResponses, types.ProviderAzureOpenAIResponses, []byte(psSSE(psResponses...)), 5, true},
		{"codex", types.ApiOpenAICodexResponses, types.ProviderOpenAICodex, []byte(psSSE(psResponses...)), 5, true},
		{"google", types.ApiGoogleGenerativeAI, types.ProviderGoogle, []byte(google), 2, false},
		{"vertex", types.ApiGoogleVertex, types.ProviderGoogleVertex, []byte(google), 2, false},
		{"mistral", types.ApiMistralConversations, types.ProviderId("fixture"), []byte(chat), 2, true},
		{"pi", types.ApiPiMessages, types.ProviderId("fixture"), []byte(pi), 5, true},
		{"bedrock", types.ApiBedrockConverseStream, types.ProviderAmazonBedrock, psBedrock(t), 5, false},
	}
}

func psModel(p psProvider, url string) *types.Model {
	return &types.Model{Id: "fixture", Name: "Fixture", Api: p.api, Provider: p.provider, BaseUrl: url, Input: []types.ModelInputModality{types.ModelInputText}, ContextWindow: 8192, MaxTokens: 32}
}

func psStart(p psProvider, model *types.Model, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	transcript := types.NewTranscriptContext([]types.Message{types.NewUserMessageVariant(types.NewUserMessage("hello", 1))})
	switch p.name {
	case "completions", "openrouter":
		return api.OpenAICompletionsStreamSimple(model, transcript, options)
	case "anthropic":
		return api.AnthropicMessagesStreamSimple(model, transcript, options)
	case "responses":
		return api.OpenAIResponsesStreamSimple(model, transcript, options)
	case "azure":
		return api.AzureOpenAIResponsesStreamSimple(model, transcript, options)
	case "codex":
		return api.OpenAICodexResponsesStreamSimple(model, transcript, options)
	case "google":
		return api.GoogleGenerativeAIStreamSimple(model, transcript, options)
	case "vertex":
		return api.GoogleVertexStreamSimple(model, transcript, options)
	case "mistral":
		return api.MistralConversationsStreamSimple(model, transcript, options)
	case "pi":
		return api.PiMessagesStreamSimple(model, transcript, options)
	case "bedrock":
		return api.BedrockConverseStreamSimple(model, transcript, options)
	}
	panic("unknown test provider")
}

func psOptions(p psProvider) *types.SimpleStreamOptions {
	key := "fixture-key"
	if p.name == "codex" {
		key = "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account"}}`)) + ".fixture"
	}
	sse := types.TransportSSE
	region := "us-east-1"
	retries := 2
	return &types.SimpleStreamOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key, MaxRetries: &retries, Env: types.ProviderEnv{"AWS_REGION": &region, "AWS_BEARER_TOKEN_BEDROCK": &key}}, Transport: &sse}}
}

func TestPortsmithJudgeProviderEvents(t *testing.T) {
	for _, provider := range psProviders(t) {
		t.Run(provider.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if provider.name == "bedrock" {
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = w.Write(provider.body)
			}))
			defer server.Close()
			model := psModel(provider, server.URL)
			// A no-observer positive control proves the frozen protocol fixture is
			// accepted by the old provider, without weakening feature assertions.
			baseline := psResult(t, psStart(provider, model, psOptions(provider)))
			if baseline.StopReason != types.StopReasonStop {
				t.Fatalf("fixture rejected: %+v", baseline)
			}
			options := psOptions(provider)
			var seen []string
			psObserve(t, options, func(data any, actual *types.Model) error {
				if actual == nil || actual.Id != model.Id || actual.Provider != model.Provider || actual.Api != model.Api {
					return errors.New("observer lost model identity")
				}
				raw, err := json.Marshal(data)
				if err != nil {
					return err
				}
				seen = append(seen, string(raw))
				if provider.marker {
					var object map[string]any
					if err = json.Unmarshal(raw, &object); err != nil {
						return err
					}
					if fmt.Sprint(object["ps_marker"]) != fmt.Sprint(len(seen)) {
						return fmt.Errorf("provider field/order lost: %s", raw)
					}
				}
				return nil
			})
			result := psResult(t, psStart(provider, model, options))
			if result.StopReason != types.StopReasonStop || len(seen) != provider.count {
				t.Fatalf("observer result=%+v events=%v", result, seen)
			}
			raw, _ := json.Marshal(result)
			if strings.Contains(string(raw), "ps_marker") {
				t.Fatal("raw event leaked into normalized transcript")
			}
			calls := 0
			failed := psOptions(provider)
			psObserve(t, failed, func(any, *types.Model) error { calls++; return errors.New("observer sentinel") })
			before := requests.Load()
			outcome := psResult(t, psStart(provider, model, failed))
			if outcome.StopReason != types.StopReasonError || outcome.ErrorMessage == nil || !strings.Contains(*outcome.ErrorMessage, "observer sentinel") || calls != 1 || requests.Load()-before != 1 {
				t.Fatalf("callback failure was swallowed/replayed: result=%+v calls=%d requests=%d", outcome, calls, requests.Load()-before)
			}
		})
	}
}

func TestPortsmithJudgeProviderObserverBackpressure(t *testing.T) {
	p := psProviders(t)[0]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(p.body)
	}))
	defer server.Close()
	options := psOptions(p)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	psObserve(t, options, func(any, *types.Model) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	})
	stream := psStart(p, psModel(p, server.URL), options)
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("observer was not called")
	}
	// A blocked callback must prevent both subsequent callbacks and completion.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err := stream.Result(ctx)
	cancel()
	if err == nil || calls.Load() != 1 {
		t.Fatal("observer was detached from stream consumption")
	}
	close(release)
	released = true
	if result := psResult(t, stream); result.StopReason != types.StopReasonStop || calls.Load() != 2 {
		t.Fatalf("failed to resume: %+v", result)
	}
}

func TestPortsmithJudgeCodexObserverFailureDoesNotFallback(t *testing.T) {
	var wsRequests, httpRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			httpRequests.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte(psSSE(psResponses...)))
			return
		}
		wsRequests.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			return
		}
		for _, event := range psResponses {
			if err = conn.Write(ctx, websocket.MessageText, []byte(event)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	p := psProvider{name: "codex", api: types.ApiOpenAICodexResponses, provider: types.ProviderOpenAICodex}
	options := psOptions(p)
	transport := types.TransportAuto
	options.Transport = &transport
	psObserve(t, options, func(any, *types.Model) error { return errors.New("observer sentinel") })
	result := psResult(t, psStart(p, psModel(p, server.URL), options))
	if result.StopReason != types.StopReasonError || result.ErrorMessage == nil || !strings.Contains(*result.ErrorMessage, "observer sentinel") || wsRequests.Load() != 1 || httpRequests.Load() != 0 {
		t.Fatalf("observer failure triggered transport fallback: result=%+v ws=%d http=%d", result, wsRequests.Load(), httpRequests.Load())
	}
}

func TestPortsmithJudgeProviderFixturesPositiveControl(t *testing.T) {
	for _, provider := range psProviders(t) {
		t.Run(provider.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if provider.name == "bedrock" {
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				_, _ = w.Write(provider.body)
			}))
			defer server.Close()
			result := psResult(t, psStart(provider, psModel(provider, server.URL), psOptions(provider)))
			if result.StopReason != types.StopReasonStop {
				t.Fatalf("invalid frozen protocol fixture: %+v", result)
			}
		})
	}
}

func psErrorText(result types.AssistantMessage) string {
	if result.ErrorMessage != nil {
		return *result.ErrorMessage
	}
	return ""
}

func TestPortsmithJudgeCopilotEffortMetadata(t *testing.T) {
	raw, err := os.ReadFile("../catalog/data/github-copilot.json")
	if err != nil {
		t.Fatal(err)
	}
	var providers map[string]map[string]map[string]any
	if err = json.Unmarshal(raw, &providers); err != nil {
		t.Fatal(err)
	}
	actual := providers["anthropic-messages"]["claude-opus-5.5"]["thinkingLevelMap"]
	expected := map[string]any{"off": nil, "minimal": nil, "low": "low", "medium": "medium", "high": "high", "xhigh": "xhigh", "max": "max"}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("catalog effort override: %v", actual)
	}
}

func TestPortsmithJudgeCodexWebsocketFixturePositiveControl(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Error("positive control unexpectedly fell back to HTTP")
			http.Error(w, "unexpected fallback", 500)
			return
		}
		requests.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if _, _, err = conn.Read(ctx); err != nil {
			return
		}
		for _, event := range psResponses {
			if err = conn.Write(ctx, websocket.MessageText, []byte(event)); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	p := psProvider{name: "codex", api: types.ApiOpenAICodexResponses, provider: types.ProviderOpenAICodex}
	options := psOptions(p)
	transport := types.TransportWebsocket
	options.Transport = &transport
	result := psResult(t, psStart(p, psModel(p, server.URL), options))
	if result.StopReason != types.StopReasonStop || requests.Load() != 1 {
		t.Fatalf("invalid websocket fixture: %+v err=%s requests=%d", result, psErrorText(result), requests.Load())
	}
}
