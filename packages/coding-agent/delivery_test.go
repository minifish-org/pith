// Delivery self-tests for the embedded SDK.
//
// These tests exercise the frozen delivery contract offline. They never call a
// paid model, spawn Node or require a Pi binary. The HTTP test uses a local
// httptest server and the real Pith OpenAI Chat Completions provider resolved
// from the model's API with no injected StreamFn. The remaining tests use
// scripted fake streams.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
package codingagent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// deliveryHTTPModel builds the model served by a local Chat Completions fake.
func deliveryHTTPModel(baseURL string) *aitypes.Model {
	return &aitypes.Model{
		Id:            "delivery-test",
		Name:          "delivery-test",
		Api:           aitypes.ApiOpenAICompletions,
		Provider:      aitypes.ProviderOpenAI,
		BaseUrl:       baseURL + "/v1",
		ContextWindow: 100000,
		MaxTokens:     4096,
	}
}

// TestDeliveryHTTPChatCompletionsWithoutStreamFn proves the SDK resolves the
// native provider from the model API and runs a real tool loop against a local
// Chat Completions server without an injected StreamFn.
func TestDeliveryHTTPChatCompletionsWithoutStreamFn(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer delivery-key" {
			t.Errorf("missing credential header: %q", r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("invalid request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			fmt.Fprint(w, "data: {\"id\":\"one\",\"object\":\"chat.completion.chunk\",\"model\":\"delivery-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"v1\",\"type\":\"function\",\"function\":{\"name\":\"verify_candidate\",\"arguments\":\"{}\"}}]},\"finish_reason\":null}]}\n\n"+
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		encoded, _ := json.Marshal(body)
		if !strings.Contains(string(encoded), "offline-verification-passed") {
			t.Errorf("tool result was not sent back to the provider")
		}
		fmt.Fprint(w, "data: {\"id\":\"two\",\"object\":\"chat.completion.chunk\",\"model\":\"delivery-test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"finished\"},\"finish_reason\":null}]}\n\n"+
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{{
		Name:        "verify_candidate",
		Description: "verify the candidate offline",
		Parameters:  json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("offline-verification-passed")}}, nil
		},
	}}, []string{"verify_candidate"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}

	session, err := CreateAgentSession(SessionOptions{
		Cwd:   t.TempDir(),
		Tools: registry,
		Model: ModelOptions{
			Model:  deliveryHTTPModel(server.URL),
			APIKey: func(context.Context, string) (string, error) { return "delivery-key", nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := session.Prompt(ctx, "verify the candidate")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.StopReason != aitypes.StopReasonStop {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if !containsSessionText(result.Messages, "offline-verification-passed") {
		t.Fatal("tool result missing from the persisted transcript")
	}
}

// TestDeliveryVerifyCandidateFailureThenRepair models the Portsmith verify
// flow: a verification tool reports failure, the model repairs the candidate
// with a second tool, then verification passes in the same conversation.
func TestDeliveryVerifyCandidateFailureThenRepair(t *testing.T) {
	manager, dir := sessionManager(t)
	defer manager.Close()

	var verifications, repairs atomic.Int32
	registry, err := NewToolRegistry(dir, []ToolDefinition{
		{
			Name:        "verify_candidate",
			Description: "verify the candidate",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
				if verifications.Add(1) == 1 {
					return ToolResult{
						Content: []aitypes.ContentBlock{aitypes.TextBlock("verification failed: output missing")},
						IsError: true,
					}, nil
				}
				return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("offline-verification-passed")}}, nil
			},
		},
		{
			Name:        "repair",
			Description: "repair the candidate",
			Parameters:  json.RawMessage(`{"type":"object"}`),
			Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
				repairs.Add(1)
				return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("repair applied")}}, nil
			},
		},
	}, []string{"verify_candidate", "repair"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}

	requests := 0
	stream := func(*aitypes.Model, *aitypes.TranscriptContext, *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		requests++
		switch requests {
		case 1:
			return sessionDone(aitypes.StopReasonToolUse, sessionToolCall("c1", "verify_candidate", `{}`))
		case 2:
			return sessionDone(aitypes.StopReasonToolUse, sessionToolCall("c2", "repair", `{}`))
		case 3:
			return sessionDone(aitypes.StopReasonToolUse, sessionToolCall("c3", "verify_candidate", `{}`))
		default:
			return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("candidate repaired and verified"))
		}
	}

	session, err := CreateAgentSession(SessionOptions{
		Cwd: dir, Manager: manager,
		Tools: registry,
		Model: ModelOptions{Model: sessionTestModel(), StreamFn: stream},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.Prompt(context.Background(), "verify then repair")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if result.StopReason != aitypes.StopReasonStop {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if verifications.Load() != 2 || repairs.Load() != 1 {
		t.Fatalf("verifications=%d repairs=%d, want 2/1", verifications.Load(), repairs.Load())
	}
	if !containsSessionText(result.Messages, "verification failed") || !containsSessionText(result.Messages, "offline-verification-passed") {
		t.Fatal("failed then successful verification did not stay in one conversation")
	}
}

// TestDeliveryToolHookDeniesBeforeExecution proves the Before hook runs before
// the tool body and can deny a call.
func TestDeliveryToolHookDeniesBeforeExecution(t *testing.T) {
	manager, dir := sessionManager(t)
	defer manager.Close()

	var executed atomic.Int32
	var requests atomic.Int32
	hooks := ToolHooks{Before: func(context.Context, ToolCall) error {
		return fmt.Errorf("denied by policy")
	}}
	registry, err := NewToolRegistry(dir, []ToolDefinition{{
		Name:       "guarded",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("ran")}}, nil
		},
	}}, []string{"guarded"}, nil, hooks)
	if err != nil {
		t.Fatal(err)
	}

	stream := func(*aitypes.Model, *aitypes.TranscriptContext, *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		if requests.Add(1) == 1 {
			return sessionDone(aitypes.StopReasonToolUse, sessionToolCall("g1", "guarded", `{}`))
		}
		return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
	}
	session, err := CreateAgentSession(SessionOptions{
		Cwd: dir, Manager: manager, Tools: registry,
		Model: ModelOptions{Model: sessionTestModel(), StreamFn: stream},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	if _, err := session.Prompt(context.Background(), "run guarded"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if executed.Load() != 0 {
		t.Fatal("denied tool body still executed")
	}
}

// TestDeliveryPerUserSessionIsolation runs two sessions concurrently and proves
// their models, tools and transcripts do not cross.
func TestDeliveryPerUserSessionIsolation(t *testing.T) {
	type userResult struct {
		text  string
		tools []string
		err   error
	}
	run := func(user, tool string) userResult {
		manager, err := OpenSession(filepath.Join(t.TempDir(), "session.jsonl"))
		if err != nil {
			return userResult{err: err}
		}
		defer manager.Close()
		dir := t.TempDir()
		registry, err := NewToolRegistry(dir, []ToolDefinition{{
			Name:       tool,
			Parameters: json.RawMessage(`{"type":"object"}`),
			Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
				return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock(user + "-tool-output")}}, nil
			},
		}}, []string{tool}, nil, ToolHooks{})
		if err != nil {
			return userResult{err: err}
		}
		requests := 0
		stream := func(_ *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
			requests++
			if requests == 1 {
				return sessionDone(aitypes.StopReasonToolUse, sessionToolCall(user+"-call", tool, `{}`))
			}
			return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock(user+"-done"))
		}
		session, err := CreateAgentSession(SessionOptions{
			Cwd: dir, Manager: manager, Tools: registry,
			Model: ModelOptions{Model: sessionTestModel(), StreamFn: stream},
		})
		if err != nil {
			return userResult{err: err}
		}
		defer session.Close()
		result, err := session.Prompt(context.Background(), user)
		if err != nil {
			return userResult{err: err}
		}
		return userResult{text: sessionResultText(result), tools: registry.Names()}
	}

	results := make(chan userResult, 2)
	go func() { results <- run("alice", "alice_tool") }()
	go func() { results <- run("bob", "bob_tool") }()
	seen := map[string]userResult{}
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Fatalf("isolated run failed: %v", r.err)
		}
		seen[r.text] = r
	}
	alice, ok := seen["alice-done"]
	if !ok {
		t.Fatalf("alice result missing: %#v", seen)
	}
	bob, ok := seen["bob-done"]
	if !ok {
		t.Fatalf("bob result missing: %#v", seen)
	}
	if strings.Join(alice.tools, ",") != "alice_tool" {
		t.Fatalf("alice tools leaked: %v", alice.tools)
	}
	if strings.Join(bob.tools, ",") != "bob_tool" {
		t.Fatalf("bob tools leaked: %v", bob.tools)
	}
}

// TestDeliveryCancellationReachesProvider proves a cancelled Prompt stops the
// in-flight provider stream and that the session stays usable.
func TestDeliveryCancellationReachesProvider(t *testing.T) {
	manager, dir := sessionManager(t)
	defer manager.Close()

	var calls atomic.Int32
	blocking := make(chan struct{})
	stream := func(model *aitypes.Model, _ *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		response := aitypes.NewAssistantMessageEventStream()
		if calls.Add(1) == 1 {
			go func() {
				<-blocking
				<-options.Signal
				assistant := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
				assistant.StopReason = aitypes.StopReasonAborted
				text := "aborted"
				assistant.ErrorMessage = &text
				response.Push(aitypes.NewErrorEvent(aitypes.StopReasonAborted, assistant))
			}()
			return response
		}
		assistant := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
		assistant.StopReason = aitypes.StopReasonStop
		assistant.Content = []aitypes.ContentBlock{aitypes.TextBlock("recovered")}
		response.Push(aitypes.NewDoneEvent(aitypes.StopReasonStop, assistant))
		return response
	}

	session, err := CreateAgentSession(SessionOptions{
		Cwd: dir, Manager: manager,
		Model:  ModelOptions{Model: sessionTestModel(), StreamFn: stream},
		Policy: RunPolicy{RetryAttempts: -1},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	done := make(chan error, 1)
	go func() { _, promptErr := session.Prompt(context.Background(), "first"); done <- promptErr }()
	time.Sleep(20 * time.Millisecond)
	session.Abort()
	close(blocking)
	select {
	case promptErr := <-done:
		if promptErr == nil {
			t.Fatal("cancelled prompt returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled prompt did not return")
	}

	result, err := session.Prompt(context.Background(), "second")
	if err != nil {
		t.Fatalf("session unusable after cancellation: %v", err)
	}
	if result.StopReason != aitypes.StopReasonStop {
		t.Fatalf("recovery stop reason = %q", result.StopReason)
	}
}

// TestDeliverySourceMapsAreComplete checks the local source-map invariant that
// the independent judge enforces from its own frozen list.
func TestDeliverySourceMapsAreComplete(t *testing.T) {
	files, err := filepath.Glob("source_map_*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 7 {
		t.Fatalf("source map files = %d, want 7: %v", len(files), files)
	}
	total := 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var rows []struct {
			Source, Upstream, Reason string
		}
		if err := json.Unmarshal(data, &rows); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if len(rows) == 0 {
			t.Fatalf("%s: empty source map", file)
		}
		for _, row := range rows {
			if row.Source == "" || row.Upstream == "" || len(row.Reason) < 12 {
				t.Fatalf("%s: incomplete row %#v", file, row)
			}
			total++
		}
	}
	if total != 998 {
		t.Fatalf("source map rows = %d, want 998", total)
	}
}

// containsSessionText reports whether any message exposes the text.
func containsSessionText(messages []agenttypes.AgentMessage, text string) bool {
	data, err := json.Marshal(messages)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), text)
}

// sessionResultText returns the text of the last assistant message in a run
// result.
func sessionResultText(result RunResult) string {
	last := ""
	for _, message := range result.Messages {
		if message.Message == nil || message.Message.Assistant == nil {
			continue
		}
		var builder strings.Builder
		for _, block := range message.Message.Assistant.Content {
			if block.IsText() && block.Text != nil {
				builder.WriteString(block.Text.Text)
			}
		}
		if builder.Len() > 0 {
			last = builder.String()
		}
	}
	return last
}
