package durable_delivery_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/harness"
	"github.com/minifish-org/pith/packages/durable/storage/jsonl"
	"github.com/minifish-org/pith/packages/durable/storage/memory"
	"github.com/minifish-org/pith/packages/durable/storage/sqlite"
	durabletesting "github.com/minifish-org/pith/packages/durable/testing"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPortsmithJudgeDurablePithProviderIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer offline-fixture" {
			t.Errorf("production request wiring: %s %q", r.URL.Path, r.Header.Get("Authorization"))
			w.WriteHeader(400)
			return
		}
		var body map[string]any
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		if body["model"] != "fixture-model" || body["stream"] != true {
			t.Errorf("production model request: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"durable reply\"},\"finish_reason\":null}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := &types.Model{Id: "fixture-model", Name: "Fixture", Api: types.ApiOpenAICompletions, Provider: "fixture-compatible", BaseUrl: server.URL + "/v1", ContextWindow: 1000000, MaxTokens: 4096, Input: []types.ModelInputModality{types.ModelInputText}}
	key := "offline-fixture"
	runner := harness.NewPithModelRunner(func(_ context.Context, r harness.ModelRef) (*types.Model, error) {
		if r.ModelID != model.Id {
			return nil, fmt.Errorf("wrong model %s", r.ModelID)
		}
		return model, nil
	}, &types.SimpleStreamOptions{StreamOptions: types.StreamOptions{ProviderRequestOptions: types.ProviderRequestOptions{APIKey: &key}}})
	h, e := harness.Open(ctx, memory.New(), harness.Options{Models: runner, Settings: func() harness.Settings {
		return harness.Settings{Retry: harness.RetryPolicy{Enabled: false}, Compaction: harness.CompactionPolicy{Enabled: false}}
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer h.Close(context.Background())
	c, e := h.Root(ctx, harness.RootOptions{Agent: json.RawMessage(`{"model":{"provider":"fixture-compatible","modelId":"fixture-model"}}`)})
	if e != nil {
		t.Fatal(e)
	}
	draft := harness.SubmissionDraft{Type: "input", Content: types.UserContentText("hello"), RequestID: "delivery-request"}
	s, e := c.Submit(ctx, draft)
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.Wait(ctx)
	if e != nil || result == nil || result.Status != "done" {
		t.Fatalf("provider-backed submission: %#v %v", result, e)
	}
	again, e := c.Submit(ctx, draft)
	if e != nil || again.ID() != s.ID() {
		t.Fatalf("durable submission dedup: %v", e)
	}
	view, e := c.Context(ctx)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(view.Messages)
	if !strings.Contains(string(b), "durable reply") || calls.Load() != 1 {
		t.Fatalf("real response/dedup: %s calls=%d", b, calls.Load())
	}
}

func TestPortsmithJudgeDurableStorageConformanceDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	factory := map[string]func() (durable.Storage, error){
		"memory": func() (durable.Storage, error) { return memory.New(), nil },
		"jsonl":  func() (durable.Storage, error) { return jsonl.Open(ctx, t.TempDir(), jsonl.Options{Fsync: true}) },
		"sqlite": func() (durable.Storage, error) {
			return sqlite.Open(ctx, filepath.Join(t.TempDir(), "state.sqlite"), sqlite.Options{})
		},
	}
	var expected []string
	b, e := os.ReadFile(filepath.Join("testdata", "storage-case-names.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &expected); e != nil {
		t.Fatal(e)
	}
	for name, create := range factory {
		t.Run(name, func(t *testing.T) {
			count := 0
			cases := durabletesting.CreateStorageConformance(func(ctx context.Context, use func(durable.Storage) error) error {
				count++
				s, e := create()
				if e != nil {
					return e
				}
				defer s.Close(context.Background())
				return use(s)
			})
			got := map[string]bool{}
			for _, c := range cases {
				if got[c.Name] || c.Run == nil {
					t.Fatalf("duplicate/empty conformance case %q", c.Name)
				}
				got[c.Name] = true
				if e := c.Run(ctx); e != nil {
					t.Fatalf("%s: %v", c.Name, e)
				}
			}
			for _, n := range expected {
				if !got[n] {
					t.Fatalf("missing source conformance case: %q", n)
				}
			}
			if count != len(cases) {
				t.Fatalf("provider calls %d != cases %d", count, len(cases))
			}
		})
	}
}
