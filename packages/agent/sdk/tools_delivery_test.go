// Self-tests for the tools-delivery batch: SDK runner and session persistence.
//
// The upstream packages/agent/test/harness/types.test.ts fixes the assembled
// agent surface; the Go port exercises the parts that are runtime-observable:
// the built-in tool registration exposed by the SDK, the read/write/edit/bash
// loop against a local Chat Completions service and JSON-lines session
// persistence.
package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestLoadSaveSessionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")

	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatalf("load missing: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("expected empty history, got %d", len(loaded))
	}

	writeMessages := decodeMessages(t, `{"role":"user","content":"hello","timestamp":1}`, `{"role":"assistant","api":"openai-completions","provider":"fixture","model":"fixture","content":[{"type":"text","text":"hi"}],"usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2}`)
	if err := SaveSession(path, writeMessages); err != nil {
		t.Fatalf("save: %v", err)
	}
	reloaded, err := LoadSession(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded) != 2 || reloaded[0].Role != "user" || reloaded[1].Assistant == nil {
		t.Fatalf("unexpected reloaded history %#v", reloaded)
	}
}

func TestRunToolLoopAndSessionRestore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, state := newFixtureServer(t)
	defer server.Close()

	sessionPath := filepath.Join(dir, "session.jsonl")
	options := RunOptions{
		Context:     context.Background(),
		BaseURL:     server.URL + "/v1",
		Model:       "fixture",
		APIKey:      "fixture-secret",
		Cwd:         dir,
		SessionPath: sessionPath,
		Prompt:      "Complete the local task",
	}
	var output bytes.Buffer
	options.Stdout = &output
	if err := Run(options); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if !strings.Contains(output.String(), "all-done") {
		t.Fatalf("missing assistant text: %q", output.String())
	}

	content, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil || string(content) != "gamma\n" {
		t.Fatalf("write/edit not performed: %q %v", content, err)
	}

	output.Reset()
	if err := Run(options); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !strings.Contains(output.String(), "all-done") {
		t.Fatalf("missing assistant text on restore: %q", output.String())
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.round != 6 {
		t.Fatalf("expected 6 requests, got %d", state.round)
	}
	if !state.resumed {
		t.Fatal("session history was not restored")
	}
	if len(state.errors) > 0 {
		t.Fatalf("fixture server errors: %v", state.errors)
	}
}

func decodeMessages(t *testing.T, lines ...string) []aitypes.Message {
	t.Helper()
	messages := make([]aitypes.Message, 0, len(lines))
	for _, line := range lines {
		var message aitypes.Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	return messages
}

type fixtureState struct {
	mu      sync.Mutex
	round   int
	resumed bool
	errors  []string
}

func newFixtureServer(t *testing.T) (*httptest.Server, *fixtureState) {
	t.Helper()
	state := &fixtureState{}
	names := []string{"read", "write", "edit", "bash"}
	args := []any{
		map[string]any{"path": "input.txt"},
		map[string]any{"path": "output.txt", "content": "beta\n"},
		map[string]any{"path": "output.txt", "oldText": "beta", "newText": "gamma"},
		map[string]any{"command": "printf 'shell-ok'"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			state.errors = append(state.errors, "wrong endpoint")
		}
		if r.Header.Get("Authorization") != "Bearer fixture-secret" {
			state.errors = append(state.errors, "missing bearer key")
		}
		var request struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			state.errors = append(state.errors, err.Error())
		}
		declared := map[string]bool{}
		for _, tool := range request.Tools {
			declared[tool.Function.Name] = true
		}
		for _, name := range names {
			if !declared[name] {
				state.errors = append(state.errors, "tool missing: "+name)
			}
		}
		count := 0
		for _, message := range request.Messages {
			if message.Role == "tool" {
				count++
			}
		}
		if state.round < 5 && count != state.round {
			state.errors = append(state.errors, fmt.Sprintf("tool conversation lost at %d: %d", state.round, count))
		}
		if state.round >= 5 {
			state.resumed = count >= 4
			if !state.resumed {
				state.errors = append(state.errors, "session history not restored")
			}
		}
		delta := map[string]any{"role": "assistant", "content": "all-done"}
		reason := "stop"
		if state.round < 4 {
			encoded, _ := json.Marshal(args[state.round])
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
				"index": 0,
				"id":    fmt.Sprintf("call-%d", state.round),
				"type":  "function",
				"function": map[string]any{
					"name":      names[state.round],
					"arguments": string(encoded),
				},
			}}}
			reason = "tool_calls"
		}
		state.round++
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(payload any, finish any) {
			encoded, _ := json.Marshal(map[string]any{
				"id":      "fixture",
				"object":  "chat.completion.chunk",
				"model":   "fixture",
				"choices": []any{map[string]any{"index": 0, "delta": payload, "finish_reason": finish}},
			})
			fmt.Fprintf(w, "data: %s\n\n", encoded)
		}
		chunk(delta, nil)
		chunk(map[string]any{}, reason)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	return server, state
}
