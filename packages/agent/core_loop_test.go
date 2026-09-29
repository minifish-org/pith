package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func wrap(message aitypes.Message) agenttypes.AgentMessage {
	return agenttypes.NewAgentMessageFromMessage(message)
}

func userMessage(text string, timestamp float64) agenttypes.AgentMessage {
	return wrap(aitypes.NewUserMessageVariant(aitypes.NewUserMessage(text, timestamp)))
}

func textToolSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}},"required":["n"]}`)
}

func TestStreamFnDefault(t *testing.T) {
	t.Cleanup(func() { SetDefaultStreamFn(nil) })
	SetDefaultStreamFn(nil)
	if _, err := GetDefaultStreamFn(); err == nil {
		t.Fatal("expected an error when no default stream function is configured")
	}

	called := false
	SetDefaultStreamFn(func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		called = true
		return aitypes.NewAssistantMessageEventStream()
	})
	fn, err := GetDefaultStreamFn()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = fn(nil, nil, nil)
	if !called {
		t.Fatal("default stream function was not returned")
	}
}

// newEchoAgent builds an Agent whose stream function emits two tool calls on
// the first request and a final text message on the second.
func newEchoAgent(t *testing.T, parallel bool, counts *[]string, requests *int, events *[]string) *Agent {
	t.Helper()

	var countsMu sync.Mutex

	makeAssistant := func(content []aitypes.ContentBlock, stopReason aitypes.StopReason) aitypes.AssistantMessage {
		return aitypes.AssistantMessage{
			Role:       aitypes.AssistantMessageRole,
			Content:    content,
			Api:        aitypes.Api("openai-completions"),
			Provider:   aitypes.ProviderId("fixture"),
			Model:      "fixture",
			Usage:      aitypes.Usage{},
			StopReason: stopReason,
			Timestamp:  1,
		}
	}

	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		var message aitypes.AssistantMessage
		if *requests == 0 {
			message = makeAssistant([]aitypes.ContentBlock{
				aitypes.ToolCallBlock(aitypes.NewToolCall("c1", "echo", json.RawMessage(`{"n":1}`))),
				aitypes.ToolCallBlock(aitypes.NewToolCall("c2", "echo", json.RawMessage(`{"n":2}`))),
			}, aitypes.StopReasonToolUse)
		} else {
			message = makeAssistant([]aitypes.ContentBlock{aitypes.TextBlock("done")}, aitypes.StopReasonStop)
		}
		*requests++
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}

	echo := agenttypes.AgentTool[any, any]{
		Tool:  aitypes.NewTool("echo", "echo", textToolSchema()),
		Label: "echo",
		Execute: func(toolCallId string, params any, signal <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
			value := ""
			if object, ok := params.(map[string]any); ok {
				value = fmt.Sprint(object["n"])
			}
			countsMu.Lock()
			*counts = append(*counts, value)
			countsMu.Unlock()
			return agenttypes.AgentToolResult[any]{
				Content: []aitypes.ContentBlock{aitypes.TextBlock(value)},
				Details: map[string]any{},
			}, nil
		},
	}

	toolExecution := agenttypes.ToolExecutionSequential
	if parallel {
		toolExecution = agenttypes.ToolExecutionParallel
	}

	runtime, err := NewAgent(AgentOptions{
		StreamFn:      streamFn,
		ToolExecution: toolExecution,
		InitialState:  &AgentInitialState{Tools: []agenttypes.AgentTool[any, any]{echo}},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	runtime.Subscribe(func(event agenttypes.AgentEvent, signal <-chan struct{}) error {
		*events = append(*events, event.Type)
		return nil
	})
	return runtime
}

func TestAgentLoopSequential(t *testing.T) { runAgentLoopModes(t, false) }
func TestAgentLoopParallel(t *testing.T)   { runAgentLoopModes(t, true) }

func runAgentLoopModes(t *testing.T, parallel bool) {
	t.Helper()
	var counts []string
	var events []string
	requests := 0
	runtime := newEchoAgent(t, parallel, &counts, &requests, &events)

	if err := runtime.Prompt([]agenttypes.AgentMessage{userMessage("go", 1)}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	sorted := append([]string(nil), counts...)
	sort.Strings(sorted)
	if !reflect.DeepEqual(sorted, []string{"1", "2"}) {
		t.Fatalf("tool calls = %v, want [1 2]", sorted)
	}

	state := runtime.State()
	roles := make([]string, 0, len(state.Messages))
	for _, message := range state.Messages {
		roles = append(roles, messageRole(message))
	}
	want := []string{"system", "user", "assistant", "toolResult", "toolResult", "assistant"}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles = %v, want %v", roles, want)
	}
	if len(events) == 0 || events[len(events)-1] != agenttypes.AgentEventAgentEnd {
		t.Fatalf("last event = %v, want agent_end", events)
	}
	if state.IsStreaming {
		t.Fatal("agent should be idle after a prompt")
	}
}

func TestAgentStateReset(t *testing.T) {
	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
		message.StopReason = aitypes.StopReasonError
		text := "unexpected model"
		message.ErrorMessage = &text
		stream.Push(aitypes.NewErrorEvent(aitypes.StopReasonError, message))
		return stream
	}
	systemPrompt := "system"
	runtime, err := NewAgent(AgentOptions{
		StreamFn: streamFn,
		InitialState: &AgentInitialState{
			SystemPrompt:  &systemPrompt,
			ThinkingLevel: agenttypes.ThinkingLow,
			Messages:      []agenttypes.AgentMessage{userMessage("hi", 1)},
		},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if got := len(runtime.State().Messages); got != 2 {
		t.Fatalf("initial messages = %d, want 2", got)
	}
	runtime.Steer(userMessage("queued", 2))
	if !runtime.HasQueuedMessages() {
		t.Fatal("expected a queued message")
	}
	if err := runtime.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	state := runtime.State()
	if len(state.Messages) != 1 || messageRole(state.Messages[0]) != aitypes.SystemMessageRole {
		t.Fatalf("messages after reset = %v", state.Messages)
	}
	if state.IsStreaming {
		t.Fatal("reset must clear isStreaming")
	}
	if len(state.PendingToolCalls) != 0 {
		t.Fatalf("pending tool calls after reset = %v", state.PendingToolCalls)
	}
	if runtime.HasQueuedMessages() {
		t.Fatal("reset must clear queues")
	}

	data, err := json.Marshal(state.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	if string(data) != `[{"role":"system","content":"system","timestamp":0}]` {
		t.Fatalf("serialized reset transcript = %s", data)
	}
}

func TestQueueModes(t *testing.T) {
	queue := newPendingMessageQueue(agenttypes.QueueModeOneAtATime)
	queue.enqueue(userMessage("a", 1))
	queue.enqueue(userMessage("b", 2))
	if got := queue.drain(); len(got) != 1 {
		t.Fatalf("one-at-a-time drain = %d messages", len(got))
	}
	if got := queue.peek(); len(got) != 1 {
		t.Fatalf("one-at-a-time peek = %d messages", len(got))
	}
	if got := queue.drain(); len(got) != 1 {
		t.Fatalf("second drain = %d messages", len(got))
	}
	if queue.hasItems() {
		t.Fatal("queue should be empty")
	}

	queue.setMode(agenttypes.QueueModeAll)
	queue.enqueue(userMessage("c", 3))
	queue.enqueue(userMessage("d", 4))
	if got := queue.drain(); len(got) != 2 {
		t.Fatalf("all drain = %d messages, want 2", len(got))
	}
}

func TestBeforeToolCallBlocks(t *testing.T) {
	requests := 0
	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		message := aitypes.AssistantMessage{
			Role:       aitypes.AssistantMessageRole,
			Content:    []aitypes.ContentBlock{aitypes.ToolCallBlock(aitypes.NewToolCall("c1", "echo", json.RawMessage(`{"n":1}`)))},
			Api:        aitypes.Api("openai-completions"),
			Provider:   aitypes.ProviderId("fixture"),
			Model:      "fixture",
			StopReason: aitypes.StopReasonToolUse,
			Timestamp:  1,
		}
		if requests > 0 {
			message = aitypes.AssistantMessage{
				Role:       aitypes.AssistantMessageRole,
				Content:    []aitypes.ContentBlock{aitypes.TextBlock("done")},
				Api:        aitypes.Api("openai-completions"),
				Provider:   aitypes.ProviderId("fixture"),
				Model:      "fixture",
				StopReason: aitypes.StopReasonStop,
				Timestamp:  1,
			}
		}
		requests++
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}
	executed := false
	echo := agenttypes.AgentTool[any, any]{
		Tool:  aitypes.NewTool("echo", "echo", textToolSchema()),
		Label: "echo",
		Execute: func(toolCallId string, params any, signal <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
			executed = true
			return agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock("ran")}, Details: map[string]any{}}, nil
		},
	}
	blocked := true
	reason := "blocked by policy"
	runtime, err := NewAgent(AgentOptions{
		StreamFn:      streamFn,
		ToolExecution: agenttypes.ToolExecutionSequential,
		BeforeToolCall: func(context agenttypes.BeforeToolCallContext, signal <-chan struct{}) (*agenttypes.BeforeToolCallResult, error) {
			return &agenttypes.BeforeToolCallResult{Block: &blocked, Reason: &reason}, nil
		},
		InitialState: &AgentInitialState{Tools: []agenttypes.AgentTool[any, any]{echo}},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if err := runtime.Prompt([]agenttypes.AgentMessage{userMessage("go", 1)}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if executed {
		t.Fatal("tool must not execute when beforeToolCall blocks it")
	}
	state := runtime.State()
	found := false
	for _, message := range state.Messages {
		if message.Message != nil && message.Message.Role == aitypes.ToolResultMessageRole && message.Message.ToolResult != nil {
			found = true
			if !message.Message.ToolResult.IsError {
				t.Fatal("blocked tool result should be an error")
			}
			if text := message.Message.ToolResult.Content[0].Text.Text; text != reason {
				t.Fatalf("blocked reason = %q, want %q", text, reason)
			}
		}
	}
	if !found {
		t.Fatal("expected a tool result message")
	}
}

func TestAfterToolCallOverrides(t *testing.T) {
	requests := 0
	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		message := aitypes.AssistantMessage{
			Role:       aitypes.AssistantMessageRole,
			Content:    []aitypes.ContentBlock{aitypes.ToolCallBlock(aitypes.NewToolCall("c1", "echo", json.RawMessage(`{"n":1}`)))},
			Api:        aitypes.Api("openai-completions"),
			Provider:   aitypes.ProviderId("fixture"),
			Model:      "fixture",
			StopReason: aitypes.StopReasonToolUse,
			Timestamp:  1,
		}
		if requests > 0 {
			message = aitypes.AssistantMessage{
				Role:       aitypes.AssistantMessageRole,
				Content:    []aitypes.ContentBlock{aitypes.TextBlock("done")},
				Api:        aitypes.Api("openai-completions"),
				Provider:   aitypes.ProviderId("fixture"),
				Model:      "fixture",
				StopReason: aitypes.StopReasonStop,
				Timestamp:  1,
			}
		}
		requests++
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}
	echo := agenttypes.AgentTool[any, any]{
		Tool:  aitypes.NewTool("echo", "echo", textToolSchema()),
		Label: "echo",
		Execute: func(toolCallId string, params any, signal <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
			return agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock("original")}, Details: map[string]any{}}, nil
		},
	}
	override := true
	runtime, err := NewAgent(AgentOptions{
		StreamFn:      streamFn,
		ToolExecution: agenttypes.ToolExecutionSequential,
		AfterToolCall: func(context agenttypes.AfterToolCallContext, signal <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
			return &agenttypes.AfterToolCallResult{
				Content: []aitypes.ContentBlock{aitypes.TextBlock("overridden")},
				IsError: &override,
			}, nil
		},
		InitialState: &AgentInitialState{Tools: []agenttypes.AgentTool[any, any]{echo}},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if err := runtime.Prompt([]agenttypes.AgentMessage{userMessage("go", 1)}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	for _, message := range runtime.State().Messages {
		if message.Message != nil && message.Message.Role == aitypes.ToolResultMessageRole && message.Message.ToolResult != nil {
			result := message.Message.ToolResult
			if result.Content[0].Text.Text != "overridden" {
				t.Fatalf("content = %q, want overridden", result.Content[0].Text.Text)
			}
			if !result.IsError {
				t.Fatal("isError override was not applied")
			}
			return
		}
	}
	t.Fatal("no tool result found")
}

func TestFinishTurnEndsRun(t *testing.T) {
	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		message := aitypes.AssistantMessage{
			Role:       aitypes.AssistantMessageRole,
			Content:    []aitypes.ContentBlock{aitypes.TextBlock("hello")},
			Api:        aitypes.Api("openai-completions"),
			Provider:   aitypes.ProviderId("fixture"),
			Model:      "fixture",
			StopReason: aitypes.StopReasonStop,
			Timestamp:  1,
		}
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}
	var events []string
	runtime, err := NewAgent(AgentOptions{
		StreamFn: streamFn,
		FinishTurn: func(turn agenttypes.AgentTurnContext, signal <-chan struct{}) (agenttypes.AgentTurnDecision, error) {
			return agenttypes.AgentTurnDecision{Action: "end"}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	runtime.Subscribe(func(event agenttypes.AgentEvent, signal <-chan struct{}) error {
		events = append(events, event.Type)
		return nil
	})
	if err := runtime.Prompt([]agenttypes.AgentMessage{userMessage("hi", 1)}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(events) == 0 || events[len(events)-1] != agenttypes.AgentEventAgentEnd {
		t.Fatalf("events = %v", events)
	}
	if countAgentEnd(events) != 1 {
		t.Fatalf("agent_end emitted %d times, want 1", countAgentEnd(events))
	}
}

func countAgentEnd(events []string) int {
	count := 0
	for _, event := range events {
		if event == agenttypes.AgentEventAgentEnd {
			count++
		}
	}
	return count
}

func TestStreamProxyReconstructsPartial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/stream" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("content-type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		lines := []string{
			`data: {"type":"start"}`,
			`data: {"type":"text_start","contentIndex":0}`,
			`data: {"type":"text_delta","contentIndex":0,"delta":"hel"}`,
			`data: {"type":"text_delta","contentIndex":0,"delta":"lo"}`,
			`data: {"type":"text_end","contentIndex":0}`,
			`data: {"type":"done","reason":"stop","usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"totalTokens":3,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}`,
		}
		for _, line := range lines {
			fmt.Fprintln(w, line)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	model := &aitypes.Model{Id: "fixture", Name: "Fixture", Api: aitypes.Api("openai-completions"), Provider: aitypes.ProviderId("fixture")}
	transcript := aitypes.NewTranscriptContext([]aitypes.Message{aitypes.NewUserMessageVariant(aitypes.NewUserMessage("hi", 1))})
	stream := StreamProxy(model, transcript, ProxyStreamOptions{ProxyUrl: server.URL, AuthToken: "token"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var eventTypes []string
	for {
		item := <-stream.Next()
		if item.Done {
			break
		}
		eventTypes = append(eventTypes, string(item.Value.Type))
	}
	wantTypes := []string{"start", "text_start", "text_delta", "text_delta", "text_end", "done"}
	if !reflect.DeepEqual(eventTypes, wantTypes) {
		t.Fatalf("event types = %v, want %v", eventTypes, wantTypes)
	}

	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if result.StopReason != aitypes.StopReasonStop {
		t.Fatalf("stop reason = %q", result.StopReason)
	}
	if len(result.Content) != 1 || result.Content[0].Text == nil || result.Content[0].Text.Text != "hello" {
		t.Fatalf("content = %+v", result.Content)
	}
}

func TestStreamProxyServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"boom"}`)
	}))
	defer server.Close()

	model := &aitypes.Model{Id: "fixture", Api: aitypes.Api("openai-completions"), Provider: aitypes.ProviderId("fixture")}
	transcript := aitypes.NewTranscriptContext([]aitypes.Message{aitypes.NewUserMessageVariant(aitypes.NewUserMessage("hi", 1))})
	stream := StreamProxy(model, transcript, ProxyStreamOptions{ProxyUrl: server.URL, AuthToken: "token"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if result.StopReason != aitypes.StopReasonError {
		t.Fatalf("stop reason = %q, want error", result.StopReason)
	}
	if result.ErrorMessage == nil || *result.ErrorMessage != "Proxy error: boom" {
		t.Fatalf("error message = %v", result.ErrorMessage)
	}
}

func TestRunAgentLoopPublishesOrderedEvents(t *testing.T) {
	requests := 0
	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		message := aitypes.AssistantMessage{
			Role:       aitypes.AssistantMessageRole,
			Content:    []aitypes.ContentBlock{aitypes.TextBlock("ok")},
			Api:        aitypes.Api("openai-completions"),
			Provider:   aitypes.ProviderId("fixture"),
			Model:      "fixture",
			StopReason: aitypes.StopReasonStop,
			Timestamp:  1,
		}
		requests++
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}
	var events []string
	agentContext := agenttypes.AgentContext{Messages: []agenttypes.AgentMessage{}}
	config := agenttypes.AgentLoopConfig{
		Model: &aitypes.Model{Id: "fixture", Provider: aitypes.ProviderId("fixture")},
		ConvertToLlm: func(messages []agenttypes.AgentMessage) ([]aitypes.Message, error) {
			return defaultConvertToLlm(messages)
		},
	}
	messages, err := RunAgentLoop([]agenttypes.AgentMessage{userMessage("hi", 1)}, agentContext, config, func(event agenttypes.AgentEvent) error {
		events = append(events, event.Type)
		return nil
	}, nil, streamFn)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	want := []string{"agent_start", "turn_start", "message_start", "message_end", "message_start", "message_end", "turn_end", "agent_end"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
}

func TestAgentAbortAndLateUpdates(t *testing.T) {
	release := make(chan struct{})
	streamFn := func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		go func() {
			<-release
			message := aitypes.AssistantMessage{
				Role:       aitypes.AssistantMessageRole,
				Content:    []aitypes.ContentBlock{aitypes.TextBlock("late")},
				Api:        aitypes.Api("openai-completions"),
				Provider:   aitypes.ProviderId("fixture"),
				Model:      "fixture",
				StopReason: aitypes.StopReasonStop,
				Timestamp:  1,
			}
			stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		}()
		return stream
	}
	runtime, err := NewAgent(AgentOptions{StreamFn: streamFn})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- runtime.Prompt([]agenttypes.AgentMessage{userMessage("hi", 1)})
	}()
	// Give the run a moment to start before aborting it.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.Signal() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	runtime.Abort()
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Prompt: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("prompt did not finish after abort")
	}
	if err := runtime.WaitForIdle(context.Background()); err != nil {
		t.Fatalf("WaitForIdle: %v", err)
	}
}

func TestRunAgentLoopContinueRejectsAssistantTail(t *testing.T) {
	assistant := aitypes.AssistantMessage{Role: aitypes.AssistantMessageRole, StopReason: aitypes.StopReasonStop, Timestamp: 1}
	agentContext := agenttypes.AgentContext{Messages: []agenttypes.AgentMessage{wrap(aitypes.NewAssistantMessageVariant(assistant))}}
	config := agenttypes.AgentLoopConfig{ConvertToLlm: defaultConvertToLlm}
	if _, err := RunAgentLoopContinue(agentContext, config, func(event agenttypes.AgentEvent) error { return nil }, nil, func(model *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		return aitypes.NewAssistantMessageEventStream()
	}); err == nil {
		t.Fatal("expected an error continuing from an assistant tail")
	}
}

// ensure the sync import is used in builds where the race detector is off.
var _ = sync.Mutex{}
