package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/minifish-org/pith/packages/ai/types"
)

// The large encrypted reasoning lives in the terminal event, as it can in a
// real Codex response. Earlier events are small enough for the default 32 KiB
// WebSocket read limit, so the old adapter fails after streaming has begun.
func codexLargeResponseEvents(responseID string, encrypted string, toolCall bool) [][]byte {
	reasoning := map[string]any{"type": "reasoning", "id": "rs-" + responseID, "summary": []any{}}
	var item map[string]any
	if toolCall {
		item = map[string]any{"type": "function_call", "id": "fc-" + responseID, "call_id": "call-" + responseID, "name": "fixture_tool", "arguments": `{"value":1}`}
	} else {
		item = map[string]any{"type": "message", "id": "msg-" + responseID, "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "done", "annotations": []any{}}}}
	}
	encryptedReasoning := map[string]any{"type": "reasoning", "id": reasoning["id"], "summary": []any{}, "encrypted_content": encrypted}
	events := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": responseID, "status": "in_progress"}},
		{"type": "response.output_item.added", "output_index": 0, "item": reasoning},
		{"type": "response.output_item.done", "output_index": 0, "item": reasoning},
		{"type": "response.output_item.added", "output_index": 1, "item": item},
		{"type": "response.output_item.done", "output_index": 1, "item": item},
		{"type": "response.completed", "response": map[string]any{
			"id": responseID, "status": "completed", "output": []any{encryptedReasoning, item},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15, "output_tokens_details": map[string]any{"reasoning_tokens": 3}},
		}},
	}
	encoded := make([][]byte, 0, len(events))
	for _, event := range events {
		data, _ := json.Marshal(event)
		encoded = append(encoded, data)
	}
	return encoded
}

type codexWebSocketFixture struct {
	server      *httptest.Server
	connections atomic.Int32
	fallbacks   atomic.Int32
	requests    chan map[string]any
}

func newCodexWebSocketFixture(t *testing.T, encrypted string, turns int) *codexWebSocketFixture {
	t.Helper()
	fixture := &codexWebSocketFixture{requests: make(chan map[string]any, turns)}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/codex/responses" || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			fixture.fallbacks.Add(1)
			http.Error(w, "unexpected HTTP fallback", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		fixture.connections.Add(1)
		defer conn.CloseNow()
		// The fixture must also accept replayed reasoning and large tool output.
		conn.SetReadLimit(16 << 20)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		for turn := 0; turn < turns; turn++ {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var request map[string]any
			if json.Unmarshal(data, &request) != nil {
				return
			}
			fixture.requests <- request
			responseID := fmt.Sprintf("r%d", turn+1)
			for _, event := range codexLargeResponseEvents(responseID, encrypted, turns > 1 && turn == 0) {
				if conn.Write(ctx, websocket.MessageText, event) != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func codexWebSocketFixtureResult(t *testing.T, model *types.Model, transcript *types.TranscriptContext, sessionID *string, limits ...int64) types.AssistantMessage {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account"}}`)) + ".fixture"
	var readLimit *int64
	if len(limits) > 0 {
		readLimit = &limits[0]
	}
	result, err := OpenAICodexResponsesStream(model, transcript, &OpenAICodexResponsesOptions{
		StreamOptions: types.StreamOptions{
			ProviderRequestOptions:   types.ProviderRequestOptions{APIKey: &key, Signal: ctx.Done()},
			SessionId:                sessionID,
			WebsocketMaxMessageBytes: readLimit,
		},
	}).Result(ctx)
	if err != nil {
		t.Fatalf("Codex stream did not terminate: %v", err)
	}
	return result
}

func assertCodexLargeResponse(t *testing.T, result types.AssistantMessage, responseID, encrypted string, stop types.StopReason) {
	t.Helper()
	if result.StopReason != stop {
		message := ""
		if result.ErrorMessage != nil {
			message = *result.ErrorMessage
		}
		t.Fatalf("stop reason = %q, want %q: %s", result.StopReason, stop, message)
	}
	if result.ResponseId == nil || *result.ResponseId != responseID {
		t.Fatalf("response ID = %v, want %q", result.ResponseId, responseID)
	}
	if result.Usage.Input != 10 || result.Usage.Output != 5 || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	if len(result.Content) != 2 || result.Content[0].Thinking == nil || result.Content[0].Thinking.ThinkingSignature == nil {
		t.Fatal("missing reasoning signature")
	}
	var signature map[string]any
	if err := json.Unmarshal([]byte(*result.Content[0].Thinking.ThinkingSignature), &signature); err != nil {
		t.Fatal(err)
	}
	if signature["encrypted_content"] != encrypted {
		t.Fatal("terminal event did not preserve the complete encrypted reasoning")
	}
}

func TestOpenAICodexWebSocketLargeMessages(t *testing.T) {
	for _, withSession := range []bool{false, true} {
		t.Run(fmt.Sprintf("session=%t", withSession), func(t *testing.T) {
			encrypted := strings.Repeat("e", 16<<20)
			turns := 1
			var sessionID *string
			if withSession {
				turns = 2
				id := t.Name()
				sessionID = &id
			}
			fixture := newCodexWebSocketFixture(t, encrypted, turns)
			if sessionID != nil {
				t.Cleanup(func() { CloseOpenAICodexWebSocketSessions(sessionID); ResetOpenAICodexWebSocketDebugStats(sessionID) })
			}
			model := testModel(types.ApiOpenAICodexResponses, fixture.server.URL)
			model.Provider = types.ProviderOpenAICodex
			transcript := testTranscript()
			stop := types.StopReasonStop
			if withSession {
				stop = types.StopReasonToolUse
			}
			first := codexWebSocketFixtureResult(t, &model, transcript, sessionID)
			assertCodexLargeResponse(t, first, "r1", encrypted, stop)
			firstRequest := <-fixture.requests
			if firstRequest["type"] != "response.create" || firstRequest["previous_response_id"] != nil {
				t.Fatal("first request must send full context")
			}
			if withSession {
				tool := first.Content[1].ToolCall
				if tool == nil {
					t.Fatal("missing tool call")
				}
				transcript.Messages = append(transcript.Messages,
					types.NewAssistantMessageVariant(first),
					types.NewToolResultMessageVariant(types.NewToolResultMessage(tool.Id, tool.Name, []types.ContentBlock{types.TextBlock(strings.Repeat("output", 16<<10))}, false, 3)),
				)
				second := codexWebSocketFixtureResult(t, &model, transcript, sessionID)
				assertCodexLargeResponse(t, second, "r2", encrypted, types.StopReasonStop)
				request := <-fixture.requests
				input, _ := request["input"].([]any)
				if request["previous_response_id"] != "r1" || len(input) != 1 {
					t.Fatal("tool continuation must reuse previous response with one delta item")
				}
				if item, _ := input[0].(map[string]any); item["type"] != "function_call_output" {
					t.Fatal("continuation delta must contain the tool result")
				}
				stats := GetOpenAICodexWebSocketDebugStats(*sessionID)
				if stats == nil || stats.ConnectionsCreated != 1 || stats.ConnectionsReused != 1 || stats.DeltaRequests != 1 {
					t.Fatalf("connection reuse stats = %#v", stats)
				}
			}
			if fixture.connections.Load() != 1 || fixture.fallbacks.Load() != 0 {
				t.Fatalf("connections=%d fallback requests=%d", fixture.connections.Load(), fixture.fallbacks.Load())
			}
		})
	}
}

func TestOpenAICodexWebSocketRejectsOversizedMessage(t *testing.T) {
	// Use a small host override to verify finite rejection without allocating a
	// 128 MiB frame. Large default messages and reused connections are above.
	fixture := newCodexWebSocketFixture(t, strings.Repeat("e", 64<<10), 1)
	model := testModel(types.ApiOpenAICodexResponses, fixture.server.URL)
	model.Provider = types.ProviderOpenAICodex
	result := codexWebSocketFixtureResult(t, &model, testTranscript(), nil, 64<<10)
	if result.StopReason != types.StopReasonError || result.ErrorMessage == nil || !strings.Contains(*result.ErrorMessage, "message too big") {
		t.Fatalf("oversized message must fail, stop=%q error=%v", result.StopReason, result.ErrorMessage)
	}
	if fixture.connections.Load() != 1 || fixture.fallbacks.Load() != 0 {
		t.Fatalf("oversized message unexpectedly retried: connections=%d fallbacks=%d", fixture.connections.Load(), fixture.fallbacks.Load())
	}
}
