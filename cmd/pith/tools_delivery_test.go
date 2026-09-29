// End-to-end self-test for the native pith CLI against a local fake Chat
// Completions service. It exercises the complete delivery scenario: the tool
// loop, session persistence and restoration, and credential hygiene.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCLIToolLoopAndSessionRestore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, state := newDeliveryFixtureServer(t)
	defer server.Close()

	session := filepath.Join(dir, "session.jsonl")
	args := []string{
		"--base-url", server.URL + "/v1",
		"--model", "fixture",
		"--api-key", "fixture-secret",
		"--cwd", dir,
		"--session", session,
		"--prompt", "Complete the local task",
	}

	var stdout bytes.Buffer
	if err := run(args, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("first run: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "all-done") {
		t.Fatalf("missing assistant text: %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "fixture-secret") {
		t.Fatal("credential leaked to stdout")
	}
	content, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil || string(content) != "gamma\n" {
		t.Fatalf("write/edit not performed: %q %v", content, err)
	}
	if _, err := os.Stat(session); err != nil {
		t.Fatalf("session file missing: %v", err)
	}

	stdout.Reset()
	if err := run(args, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("second run: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "all-done") {
		t.Fatalf("missing assistant text on restore: %q", stdout.String())
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

func TestCLIReadsAPIKeyFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, state := newDeliveryFixtureServer(t)
	defer server.Close()
	t.Setenv(apiKeyEnvVar, "fixture-secret")

	args := []string{
		"--base-url", server.URL + "/v1",
		"--model", "fixture",
		"--cwd", dir,
		"--prompt", "Complete the local task",
	}
	var stdout bytes.Buffer
	if err := run(args, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("run: %v\n%s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "all-done") {
		t.Fatalf("missing assistant text: %q", stdout.String())
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.errors) > 0 {
		t.Fatalf("fixture server errors: %v", state.errors)
	}
}

type deliveryState struct {
	mu      sync.Mutex
	round   int
	resumed bool
	errors  []string
}

func newDeliveryFixtureServer(t *testing.T) (*httptest.Server, *deliveryState) {
	t.Helper()
	state := &deliveryState{}
	names := []string{"read", "write", "edit", "bash"}
	args := []any{
		map[string]any{"path": "input.txt"},
		map[string]any{"path": "output.txt", "content": "beta\n"},
		map[string]any{"path": "output.txt", "oldText": "beta", "newText": "gamma"},
		map[string]any{"command": "printf 'shell-ok'"},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			state.errors = append(state.errors, "wrong model endpoint")
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
	})
	server := httptest.NewServer(handler)
	return server, state
}
