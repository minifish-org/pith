// Provider stream event observer self-tests.
//
// These tests exercise the parsed provider stream observer
// (`types.ProviderRequestOptions.OnProviderStreamEvent`) beyond field presence:
// direct provider-options use, simple-to-provider option forwarding,
// metadata-only events, error propagation without transport replay, body
// release on cancellation and nil-callback compatibility. They use only local
// httptest fixtures and dummy keys; no live model requests are made.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
)

func obsModel(apiName types.Api, provider types.ProviderId, baseURL string) *types.Model {
	return &types.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           apiName,
		Provider:      provider,
		BaseUrl:       baseURL,
		Input:         []types.ModelInputModality{types.ModelInputText},
		ContextWindow: 8192,
		MaxTokens:     32,
	}
}

func obsTranscript() *types.TranscriptContext {
	return types.NewTranscriptContext([]types.Message{
		types.NewUserMessageVariant(types.NewUserMessage("hello", 1)),
	})
}

// obsSSE renders JSON events as SSE frames, adding an `event:` line when the
// payload carries a `type` field (Anthropic/Pi style).
func obsSSE(events ...string) string {
	var body strings.Builder
	for _, event := range events {
		var object map[string]any
		if json.Unmarshal([]byte(event), &object) == nil {
			if kind, ok := object["type"].(string); ok {
				fmt.Fprintf(&body, "event: %s\n", kind)
			}
		}
		fmt.Fprintf(&body, "data: %s\n\n", event)
	}
	return body.String()
}

func obsOptions(apiKey string) *types.SimpleStreamOptions {
	key := apiKey
	return &types.SimpleStreamOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key},
		},
	}
}

func obsResult(t *testing.T, stream *types.AssistantMessageEventStream) types.AssistantMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("stream did not terminate: %v", err)
	}
	return result
}

func obsText(result types.AssistantMessage) string {
	var text strings.Builder
	for _, block := range result.Content {
		if block.Text != nil {
			text.WriteString(block.Text.Text)
		}
	}
	return text.String()
}

// TestProviderStreamObserverBuildBaseOptionsForwards proves BuildBaseOptions
// carries the observer (and other provider request callbacks) into the shared
// StreamOptions unchanged.
func TestProviderStreamObserverBuildBaseOptionsForwards(t *testing.T) {
	model := obsModel(types.ApiOpenAICompletions, types.ProviderOpenAI, "http://example.invalid")
	options := obsOptions("fixture-key")

	var gotModel *types.Model
	var gotMarker any
	options.OnProviderStreamEvent = func(data any, actual *types.Model) error {
		gotModel = actual
		if object, ok := data.(map[string]any); ok {
			gotMarker = object["ps_marker"]
		}
		return nil
	}

	base := BuildBaseOptions(model, obsTranscript(), options, nil)
	if base.OnProviderStreamEvent == nil {
		t.Fatal("BuildBaseOptions dropped OnProviderStreamEvent")
	}
	if err := base.OnProviderStreamEvent(map[string]any{"ps_marker": 7}, model); err != nil {
		t.Fatalf("preserved callback returned error: %v", err)
	}
	if gotModel != model {
		t.Fatalf("callback lost model identity: %v", gotModel)
	}
	if fmt.Sprint(gotMarker) != "7" {
		t.Fatalf("callback payload = %v, want 7", gotMarker)
	}

	// A nil callback must stay nil rather than becoming a non-nil closure.
	nilBase := BuildBaseOptions(model, obsTranscript(), obsOptions("fixture-key"), nil)
	if nilBase.OnProviderStreamEvent != nil {
		t.Fatal("nil observer should not be materialized")
	}
}

// TestProviderStreamObserverSimpleEntryForwarding proves every simple entry
// point forwards the observer to its provider-specific options and invokes it
// once per successfully parsed event.
func TestProviderStreamObserverSimpleEntryForwarding(t *testing.T) {
	chat := obsSSE(
		`{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	anthropic := obsSSE(
		`{"type":"message_start","message":{"id":"r1","role":"assistant","content":[],"model":"fixture","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`{"type":"message_stop"}`,
	)
	responses := obsSSE(
		`{"type":"response.created","response":{"id":"r1","model":"fixture","output":[],"status":"in_progress"}}`,
		`{"type":"response.completed","response":{"id":"r1","model":"fixture","status":"completed","output":[]}}`,
	)
	google := obsSSE(
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}],"responseId":"g1"}`,
		`{"candidates":[{"finishReason":"STOP"}]}`,
	)
	pi := obsSSE(
		`{"type":"start"}`,
		`{"type":"done","reason":"stop"}`,
	)

	cases := []struct {
		name     string
		api      types.Api
		provider types.ProviderId
		body     string
		want     int
		start    func(*types.Model, *types.TranscriptContext, *types.SimpleStreamOptions) *types.AssistantMessageEventStream
	}{
		{"completions", types.ApiOpenAICompletions, types.ProviderOpenAI, chat, 2, OpenAICompletionsStreamSimple},
		{"anthropic", types.ApiAnthropicMessages, types.ProviderAnthropic, anthropic, 3, AnthropicMessagesStreamSimple},
		{"responses", types.ApiOpenAIResponses, types.ProviderOpenAI, responses, 2, OpenAIResponsesStreamSimple},
		{"google", types.ApiGoogleGenerativeAI, types.ProviderGoogle, google, 2, GoogleGenerativeAIStreamSimple},
		{"mistral", types.ApiMistralConversations, types.ProviderId("fixture"), chat, 2, MistralConversationsStreamSimple},
		{"pi", types.ApiPiMessages, types.ProviderId("fixture"), pi, 2, PiMessagesStreamSimple},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := sseServer(t, tc.body, 200)
			defer server.Close()
			model := obsModel(tc.api, tc.provider, server.URL)

			options := obsOptions("fixture-key")
			seen := 0
			options.OnProviderStreamEvent = func(_ any, actual *types.Model) error {
				if actual != model {
					return errors.New("observer lost model identity")
				}
				seen++
				return nil
			}

			result := obsResult(t, tc.start(model, obsTranscript(), options))
			if result.StopReason != types.StopReasonStop {
				t.Fatalf("stop reason = %v (%v)", result.StopReason, result.ErrorMessage)
			}
			if seen != tc.want {
				t.Fatalf("observer calls = %d, want %d", seen, tc.want)
			}
		})
	}
}

// TestProviderStreamObserverDirectOptionsPreservesRawFields uses provider
// options directly (not via the simple entry point) and proves unknown JSON
// metadata survives into the observer and never leaks into the transcript.
func TestProviderStreamObserverDirectOptionsPreservesRawFields(t *testing.T) {
	body := obsSSE(
		`{"ps_marker":1,"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"ps_marker":2,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	server := sseServer(t, body, 200)
	defer server.Close()
	model := obsModel(types.ApiOpenAICompletions, types.ProviderOpenAI, server.URL)

	key := "fixture-key"
	var markers []string
	options := &OpenAICompletionsOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{
				APIKey: &key,
				OnProviderStreamEvent: func(data any, _ *types.Model) error {
					raw, err := json.Marshal(data)
					if err != nil {
						return err
					}
					var object map[string]any
					if err := json.Unmarshal(raw, &object); err != nil {
						return err
					}
					markers = append(markers, fmt.Sprint(object["ps_marker"]))
					return nil
				},
			},
		},
	}

	result := obsResult(t, OpenAICompletionsStream(model, obsTranscript(), options))
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stop reason = %v (%v)", result.StopReason, result.ErrorMessage)
	}
	if strings.Join(markers, ",") != "1,2" {
		t.Fatalf("observed raw markers = %v, want [1 2]", markers)
	}
	if obsText(result) != "hi" {
		t.Fatalf("normalized text = %q, want %q", obsText(result), "hi")
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "ps_marker") {
		t.Fatal("raw provider fields leaked into the normalized transcript")
	}
}

// TestProviderStreamObserverMetadataOnlyEvent proves a chunk with no choices
// (usage only) still reaches the observer before normalization.
func TestProviderStreamObserverMetadataOnlyEvent(t *testing.T) {
	body := obsSSE(
		`{"ps_meta":"only","usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	server := sseServer(t, body, 200)
	defer server.Close()
	model := obsModel(types.ApiOpenAICompletions, types.ProviderOpenAI, server.URL)

	options := obsOptions("fixture-key")
	seen := 0
	sawMetadata := false
	options.OnProviderStreamEvent = func(data any, _ *types.Model) error {
		seen++
		if object, ok := data.(map[string]any); ok && object["ps_meta"] == "only" {
			sawMetadata = true
		}
		return nil
	}

	result := obsResult(t, OpenAICompletionsStreamSimple(model, obsTranscript(), options))
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("stop reason = %v (%v)", result.StopReason, result.ErrorMessage)
	}
	if seen != 2 || !sawMetadata {
		t.Fatalf("seen=%d sawMetadata=%v, want 2 and true", seen, sawMetadata)
	}
	if result.Usage.Input != 2 || result.Usage.Output != 1 {
		t.Fatalf("metadata event was not normalized: %+v", result.Usage)
	}
}

// TestProviderStreamObserverErrorStopsStreamNoReplay proves an observer error
// terminates the stream with the original text, stops observing subsequent
// events, does not normalize the failing event and never retries the request.
func TestProviderStreamObserverErrorStopsStreamNoReplay(t *testing.T) {
	body := obsSSE(
		`{"ps_marker":1,"choices":[{"index":0,"delta":{"content":"alpha"},"finish_reason":null}]}`,
		`{"ps_marker":2,"choices":[{"index":0,"delta":{"content":"bravo"},"finish_reason":null}]}`,
		`{"ps_marker":3,"choices":[{"index":0,"delta":{"content":"charlie"},"finish_reason":"stop"}]}`,
	)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("content-type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	model := obsModel(types.ApiOpenAICompletions, types.ProviderOpenAI, server.URL)

	options := obsOptions("fixture-key")
	calls := 0
	options.OnProviderStreamEvent = func(_ any, _ *types.Model) error {
		calls++
		if calls == 2 {
			return errors.New("observer sentinel")
		}
		return nil
	}

	result := obsResult(t, OpenAICompletionsStreamSimple(model, obsTranscript(), options))
	if result.StopReason != types.StopReasonError {
		t.Fatalf("stop reason = %v, want error", result.StopReason)
	}
	if result.ErrorMessage == nil || !strings.Contains(*result.ErrorMessage, "observer sentinel") {
		t.Fatalf("error text lost: %v", result.ErrorMessage)
	}
	if calls != 2 {
		t.Fatalf("observer calls = %d, want 2 (stop after failure)", calls)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1 (observer error must not retry)", requests.Load())
	}
	if text := obsText(result); text != "alpha" {
		t.Fatalf("normalized text = %q, want %q", text, "alpha")
	}
}

// TestProviderStreamObserverCancellationReleasesStream proves a blocked
// observer does not prevent the underlying response body from being released
// when the request is cancelled.
func TestProviderStreamObserverCancellationReleasesStream(t *testing.T) {
	observed := make(chan struct{})
	release := make(chan struct{})
	bodyReleased := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"finish_reason\":null}]}\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(bodyReleased)
	}))
	defer server.Close()

	model := obsModel(types.ApiOpenAICompletions, types.ProviderOpenAI, server.URL)
	key := "fixture-key"
	signal := make(chan struct{})
	options := &types.SimpleStreamOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key, Signal: signal},
		},
	}
	options.OnProviderStreamEvent = func(any, *types.Model) error {
		close(observed)
		<-release
		return nil
	}

	stream := OpenAICompletionsStreamSimple(model, obsTranscript(), options)
	select {
	case <-observed:
	case <-time.After(5 * time.Second):
		t.Fatal("observer was not called")
	}

	// Cancel while the observer is blocked, then unblock it.
	close(signal)
	close(release)

	result := obsResult(t, stream)
	if result.StopReason != types.StopReasonAborted {
		t.Fatalf("stop reason = %v (%v), want aborted", result.StopReason, result.ErrorMessage)
	}
	select {
	case <-bodyReleased:
	case <-time.After(5 * time.Second):
		t.Fatal("response body was not released on cancellation")
	}
}

// TestProviderStreamObserverNilCompatibility proves that omitting the observer
// preserves the previous stream behavior.
func TestProviderStreamObserverNilCompatibility(t *testing.T) {
	body := obsSSE(
		`{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)
	server := sseServer(t, body, 200)
	defer server.Close()
	model := obsModel(types.ApiOpenAICompletions, types.ProviderOpenAI, server.URL)

	options := obsOptions("fixture-key")
	result := obsResult(t, OpenAICompletionsStreamSimple(model, obsTranscript(), options))
	if result.StopReason != types.StopReasonStop || obsText(result) != "hi" {
		t.Fatalf("nil-observer stream changed: %+v", result)
	}
}
