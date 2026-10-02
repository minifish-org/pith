package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/minifish-org/pith/packages/ai/api"
	"github.com/minifish-org/pith/packages/ai/types"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPortsmithJudgePiV1MistralEmptyDeltas(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range []string{`{"content":""}`, `{"content":[{"type":"thinking","thinking":[{"type":"text","text":"one"}]}]}`, `{"content":""}`, `{"content":[{"type":"text","text":""}]}`, `{"content":[{"type":"thinking","thinking":[{"type":"text","text":" two"}]},{"type":"text","text":"answer"}]}`} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":%s,\"finish_reason\":null}]}\n\n", delta)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	key := "fake"
	model := types.Model{Id: "non-whitelisted", Provider: types.ProviderMistral, Api: types.ApiMistralConversations, BaseUrl: server.URL, MaxTokens: 16}
	stream := api.MistralConversationsStream(&model, &types.TranscriptContext{}, &api.MistralOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key}}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msg, err := stream.Result(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != types.StopReasonStop {
		t.Fatalf("fixture protocol failed: %+v", msg)
	}
	raw, _ := json.Marshal(msg.Content)
	var blocks []map[string]any
	_ = json.Unmarshal(raw, &blocks)
	if len(blocks) != 2 || blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "one two" || blocks[1]["type"] != "text" || blocks[1]["text"] != "answer" {
		t.Fatalf("empty deltas opened/split blocks: %s", raw)
	}
}

func TestPortsmithJudgePiV1MistralMappedEffort(t *testing.T) {
	payloads := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
		}
		payloads <- p
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	high := "medium"
	key := "fake"
	model := types.Model{Id: "non-whitelisted", Provider: types.ProviderMistral, Api: types.ApiMistralConversations, BaseUrl: server.URL, Reasoning: true, MaxTokens: 16, ThinkingLevelMap: types.ThinkingLevelMap{types.ThinkingHigh: &high}}
	reasoning := types.ThinkingHigh
	stream := api.MistralConversationsStreamSimple(&model, &types.TranscriptContext{}, &types.SimpleStreamOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key}}, Reasoning: &reasoning})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msg, err := stream.Result(ctx)
	if err != nil || msg.StopReason != types.StopReasonStop {
		t.Fatalf("fixture failed %+v %v", msg, err)
	}
	p := <-payloads
	if p["reasoning_effort"] != "medium" {
		t.Fatalf("model's mapped effort ignored: %v", p)
	}
}
