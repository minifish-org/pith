// Pi 1.0 core and nested tool execution self-tests.
//
// These port the source regressions from packages/agent/test/agent-loop.test.ts
// (runToolCall, structured-content replacement, hook ordering) and the
// thinkingLevel recording change from packages/agent/src/agent-loop.ts. They are
// offline and deterministic: no network, no external processes.
package agent

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

const piV1EchoSchema = `{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`

func piV1AssistantMessage() aitypes.AssistantMessage {
	return aitypes.AssistantMessage{
		Role:       aitypes.AssistantMessageRole,
		Api:        aitypes.Api("openai-completions"),
		Provider:   aitypes.ProviderId("fixture"),
		Model:      "fixture",
		StopReason: aitypes.StopReasonToolUse,
		Timestamp:  1,
	}
}

func piV1EchoTool(name string, execute func(id string, params any, signal <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error)) agenttypes.AgentTool[any, any] {
	return agenttypes.AgentTool[any, any]{
		Tool:    aitypes.NewTool(name, name, json.RawMessage(piV1EchoSchema)),
		Label:   name,
		Execute: execute,
	}
}

// TestPiV1RunToolCallSuccessAndOrdering checks that a successful nested call
// runs prepare, before, execute and after exactly once, preserves identity and
// returns structured content.
func TestPiV1RunToolCallSuccessAndOrdering(t *testing.T) {
	var order []string
	var mu sync.Mutex
	record := func(value string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, value)
	}

	tool := piV1EchoTool("echo", func(id string, params any, _ <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
		record("execute")
		return agenttypes.AgentToolResult[any]{
			Content:           []aitypes.ContentBlock{aitypes.TextBlock("echo")},
			StructuredContent: json.RawMessage(`{"value":"a"}`),
		}, nil
	})

	options := agenttypes.RunToolCallOptions{
		Tools: []agenttypes.AgentTool[any, any]{tool},
		BeforeToolCall: func(c agenttypes.BeforeToolCallContext, _ <-chan struct{}) (*agenttypes.BeforeToolCallResult, error) {
			record("before")
			if args, ok := c.Args.(map[string]any); !ok || args["value"] != "a" {
				t.Errorf("before hook args = %#v", c.Args)
			}
			if c.ToolCall.Id != "outer/1" {
				t.Errorf("before hook tool call = %#v", c.ToolCall)
			}
			return nil, nil
		},
		AfterToolCall: func(c agenttypes.AfterToolCallContext, _ <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
			record("after")
			if c.IsError {
				t.Error("after hook saw an unexpected error")
			}
			if string(c.Result.StructuredContent) != `{"value":"a"}` {
				t.Errorf("after hook structured content = %s", c.Result.StructuredContent)
			}
			return nil, nil
		},
	}

	outcome := RunToolCall(aitypes.NewToolCall("outer/1", "echo", json.RawMessage(`{"value":"a"}`)), options)
	if outcome.IsError {
		t.Fatalf("outcome unexpectedly failed: %+v", outcome)
	}
	if outcome.ToolCall.Id != "outer/1" || outcome.ToolCall.Name != "echo" {
		t.Fatalf("identity lost: %+v", outcome.ToolCall)
	}
	if string(outcome.Result.StructuredContent) != `{"value":"a"}` {
		t.Fatalf("structured content = %s", outcome.Result.StructuredContent)
	}
	if !stringSliceEqual(order, []string{"before", "execute", "after"}) {
		t.Fatalf("hook order = %v", order)
	}
}

// TestPiV1RunToolCallNeverFabricatesOutcomes checks unknown tools and schema
// validation failures become error outcomes and never reach the hooks.
func TestPiV1RunToolCallNeverFabricatesOutcomes(t *testing.T) {
	executed := 0
	before := 0
	after := 0
	tool := piV1EchoTool("echo", func(_ string, _ any, _ <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
		executed++
		return agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock("ok")}}, nil
	})
	options := agenttypes.RunToolCallOptions{
		Tools: []agenttypes.AgentTool[any, any]{tool},
		BeforeToolCall: func(agenttypes.BeforeToolCallContext, <-chan struct{}) (*agenttypes.BeforeToolCallResult, error) {
			before++
			return nil, nil
		},
		AfterToolCall: func(agenttypes.AfterToolCallContext, <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
			after++
			return nil, nil
		},
	}

	unknown := RunToolCall(aitypes.NewToolCall("u", "missing", json.RawMessage(`{}`)), options)
	if !unknown.IsError || piV1BlockText(unknown.Result.Content[0]) != "Tool missing not found" {
		t.Fatalf("unknown tool outcome = %+v", unknown)
	}

	invalid := RunToolCall(aitypes.NewToolCall("i", "echo", json.RawMessage(`{}`)), options)
	if !invalid.IsError {
		t.Fatalf("invalid arguments outcome = %+v", invalid)
	}

	if executed != 0 || before != 0 || after != 0 {
		t.Fatalf("hooks/execute ran for rejected calls: executed=%d before=%d after=%d", executed, before, after)
	}
}

// TestPiV1RunToolCallBlockedSkipsAfter checks a before-hook block becomes an
// error outcome, skips execution and does not run the after hook.
func TestPiV1RunToolCallBlockedSkipsAfter(t *testing.T) {
	executed := 0
	after := 0
	tool := piV1EchoTool("echo", func(_ string, _ any, _ <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
		executed++
		return agenttypes.AgentToolResult[any]{}, nil
	})
	blocked := true
	reason := "permission denied"
	options := agenttypes.RunToolCallOptions{
		Tools: []agenttypes.AgentTool[any, any]{tool},
		BeforeToolCall: func(agenttypes.BeforeToolCallContext, <-chan struct{}) (*agenttypes.BeforeToolCallResult, error) {
			return &agenttypes.BeforeToolCallResult{Block: &blocked, Reason: &reason}, nil
		},
		AfterToolCall: func(agenttypes.AfterToolCallContext, <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
			after++
			return nil, nil
		},
	}
	outcome := RunToolCall(aitypes.NewToolCall("b", "echo", json.RawMessage(`{"value":"x"}`)), options)
	if !outcome.IsError || executed != 0 || after != 0 {
		t.Fatalf("blocked outcome = %+v executed=%d after=%d", outcome, executed, after)
	}
	if piV1BlockText(outcome.Result.Content[0]) != "permission denied" {
		t.Fatalf("blocked reason = %q", piV1BlockText(outcome.Result.Content[0]))
	}
}

// TestPiV1RunToolCallStructuredReplacement mirrors the upstream runToolCall
// structured-content matrix: explicit structured content wins, replacing only
// content drops stale structured data, and omitting both keeps the original.
func TestPiV1RunToolCallStructuredReplacement(t *testing.T) {
	tool := piV1EchoTool("echo", func(_ string, _ any, _ <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
		return agenttypes.AgentToolResult[any]{
			Content:           []aitypes.ContentBlock{aitypes.TextBlock("original")},
			StructuredContent: json.RawMessage(`{"value":"original"}`),
		}, nil
	})

	cases := []struct {
		name     string
		after    *agenttypes.AfterToolCallResult
		expected string
	}{
		{name: "content only drops", after: &agenttypes.AfterToolCallResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("redacted")}}, expected: ""},
		{name: "structured only replaces", after: &agenttypes.AfterToolCallResult{StructuredContent: json.RawMessage(`{"value":"replaced"}`)}, expected: `{"value":"replaced"}`},
		{name: "both keep explicit", after: &agenttypes.AfterToolCallResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("redacted")}, StructuredContent: json.RawMessage(`{"value":"both"}`)}, expected: `{"value":"both"}`},
		{name: "details only keeps", after: &agenttypes.AfterToolCallResult{Details: map[string]any{"note": "kept"}}, expected: `{"value":"original"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			afterResult := tc.after
			outcome := RunToolCall(aitypes.NewToolCall("x", "echo", json.RawMessage(`{"value":"original"}`)), agenttypes.RunToolCallOptions{
				Tools: []agenttypes.AgentTool[any, any]{tool},
				AfterToolCall: func(agenttypes.AfterToolCallContext, <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
					return afterResult, nil
				},
			})
			if string(outcome.Result.StructuredContent) != tc.expected {
				t.Fatalf("structured content = %q, want %q", outcome.Result.StructuredContent, tc.expected)
			}
		})
	}
}

// TestPiV1RunToolCallExplicitErrorPreserved checks an execute result flagged
// IsError keeps its error flag and structured data through the hooks.
func TestPiV1RunToolCallExplicitErrorPreserved(t *testing.T) {
	tool := piV1EchoTool("echo", func(_ string, _ any, _ <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
		return agenttypes.AgentToolResult[any]{
			Content:           []aitypes.ContentBlock{aitypes.TextBlock("bad")},
			StructuredContent: json.RawMessage(`{"partial":true}`),
			IsError:           true,
		}, nil
	})
	seenError := false
	outcome := RunToolCall(aitypes.NewToolCall("e", "echo", json.RawMessage(`{"value":"v"}`)), agenttypes.RunToolCallOptions{
		Tools: []agenttypes.AgentTool[any, any]{tool},
		AfterToolCall: func(c agenttypes.AfterToolCallContext, _ <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
			seenError = c.IsError
			return nil, nil
		},
	})
	if !outcome.IsError || !seenError {
		t.Fatalf("explicit error not preserved: outcome=%+v seen=%v", outcome, seenError)
	}
	if string(outcome.Result.StructuredContent) != `{"partial":true}` {
		t.Fatalf("error structured content = %s", outcome.Result.StructuredContent)
	}
}

// TestPiV1RunToolCallCancellation covers before-call cancellation and an
// after-hook failure becoming an error outcome.
func TestPiV1RunToolCallCancellation(t *testing.T) {
	executed := 0
	tool := piV1EchoTool("echo", func(_ string, _ any, _ <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
		executed++
		return agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock("ok")}}, nil
	})

	closed := make(chan struct{})
	close(closed)
	beforeRan := 0
	outcome := RunToolCall(aitypes.NewToolCall("c", "echo", json.RawMessage(`{"value":"v"}`)), agenttypes.RunToolCallOptions{
		Tools:  []agenttypes.AgentTool[any, any]{tool},
		Signal: closed,
		BeforeToolCall: func(agenttypes.BeforeToolCallContext, <-chan struct{}) (*agenttypes.BeforeToolCallResult, error) {
			beforeRan++
			return nil, nil
		},
	})
	if !outcome.IsError || executed != 0 || beforeRan != 1 {
		t.Fatalf("aborted before execute: outcome=%+v executed=%d before=%d", outcome, executed, beforeRan)
	}

	outcome = RunToolCall(aitypes.NewToolCall("c2", "echo", json.RawMessage(`{"value":"v"}`)), agenttypes.RunToolCallOptions{
		Tools: []agenttypes.AgentTool[any, any]{tool},
		AfterToolCall: func(agenttypes.AfterToolCallContext, <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
			return nil, errPiV1AfterFailure
		},
	})
	if !outcome.IsError || piV1BlockText(outcome.Result.Content[0]) != errPiV1AfterFailure.Error() {
		t.Fatalf("after failure outcome = %+v", outcome)
	}
}

var errPiV1AfterFailure = errors.New("after hook failed")

// TestPiV1RunToolCallConcurrentUpdates checks updates emitted from concurrent
// goroutines are collected and delivered exactly once before the call returns.
func TestPiV1RunToolCallConcurrentUpdates(t *testing.T) {
	const workers = 8
	const perWorker = 16
	tool := piV1EchoTool("echo", func(_ string, _ any, _ <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < perWorker; i++ {
					onUpdate(agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock("partial")}})
				}
			}()
		}
		wg.Wait()
		return agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock("done")}}, nil
	})

	var mu sync.Mutex
	updates := 0
	context := agenttypes.AgentContext{Messages: []agenttypes.AgentMessage{userMessage("hi", 1)}}
	options := agenttypes.RunToolCallOptions{
		Tools:   []agenttypes.AgentTool[any, any]{tool},
		Context: context,
		OnUpdate: func(agenttypes.AgentToolResult[any]) {
			mu.Lock()
			updates++
			mu.Unlock()
		},
	}
	outcome := RunToolCall(aitypes.NewToolCall("u", "echo", json.RawMessage(`{"value":"v"}`)), options)
	if outcome.IsError {
		t.Fatalf("outcome = %+v", outcome)
	}
	if updates != workers*perWorker {
		t.Fatalf("updates = %d, want %d", updates, workers*perWorker)
	}
	// Nested calls insert no transcript messages.
	if len(options.Context.Messages) != 1 {
		t.Fatalf("nested call mutated the context transcript: %d messages", len(options.Context.Messages))
	}
}

// TestPiV1LoopFinalizeStructuredAndHooks checks that the main agent loop shares
// the same finalization primitive for structured content and hook ordering.
func TestPiV1LoopFinalizeStructuredAndHooks(t *testing.T) {
	tool := piV1EchoTool("echo", func(_ string, _ any, _ <-chan struct{}, _ agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
		return agenttypes.AgentToolResult[any]{
			Content:           []aitypes.ContentBlock{aitypes.TextBlock("original")},
			StructuredContent: json.RawMessage(`{"value":"original"}`),
		}, nil
	})

	var ends []agenttypes.AgentToolResult[any]
	requests := 0
	streamFn := func(model *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		var message aitypes.AssistantMessage
		if requests == 0 {
			message = piV1AssistantMessage()
			message.Content = []aitypes.ContentBlock{aitypes.ToolCallBlock(aitypes.NewToolCall("c1", "echo", json.RawMessage(`{"value":"original"}`)))}
		} else {
			message = aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
			message.Content = []aitypes.ContentBlock{aitypes.TextBlock("done")}
			message.StopReason = aitypes.StopReasonStop
		}
		requests++
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}

	config := agenttypes.AgentLoopConfig{
		Model:        &aitypes.Model{Id: "fixture", Provider: aitypes.ProviderId("fixture"), Api: aitypes.Api("openai-completions")},
		ConvertToLlm: piV1ConvertToLlm,
		AfterToolCall: func(agenttypes.AfterToolCallContext, <-chan struct{}) (*agenttypes.AfterToolCallResult, error) {
			return &agenttypes.AfterToolCallResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("redacted")}}, nil
		},
	}
	context := agenttypes.AgentContext{Tools: []agenttypes.AgentTool[any, any]{tool}}
	_, err := RunAgentLoop(
		[]agenttypes.AgentMessage{userMessage("go", 1)},
		context,
		config,
		func(event agenttypes.AgentEvent) error {
			if event.Type == agenttypes.AgentEventToolExecutionEnd {
				if result, ok := event.Result.(agenttypes.AgentToolResult[any]); ok {
					ends = append(ends, result)
				}
			}
			return nil
		},
		nil,
		streamFn,
	)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if len(ends) != 1 {
		t.Fatalf("tool_execution_end events = %d", len(ends))
	}
	if len(ends[0].StructuredContent) != 0 {
		t.Fatalf("content-only after hook kept stale structured content: %s", ends[0].StructuredContent)
	}
}

// TestPiV1LoopRecordsThinkingLevel checks that the final assistant message
// records the requested level, including "off", independently of the adapter.
func TestPiV1LoopRecordsThinkingLevel(t *testing.T) {
	cases := []struct {
		name     string
		reason   *agenttypes.ThinkingLevel
		expected string
	}{
		{name: "off by default", reason: nil, expected: "off"},
		{name: "high recorded", reason: piV1ThinkingPtr(agenttypes.ThinkingHigh), expected: "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streamFn := func(model *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
				stream := aitypes.NewAssistantMessageEventStream()
				message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
				message.Content = []aitypes.ContentBlock{aitypes.TextBlock("done")}
				message.StopReason = aitypes.StopReasonStop
				stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
				return stream
			}
			config := agenttypes.AgentLoopConfig{
				SimpleStreamOptions: aitypes.SimpleStreamOptions{Reasoning: tc.reason},
				Model:               &aitypes.Model{Id: "fixture", Provider: aitypes.ProviderId("fixture"), Api: aitypes.Api("openai-completions")},
				ConvertToLlm:        piV1ConvertToLlm,
			}
			messages, err := RunAgentLoop(
				[]agenttypes.AgentMessage{userMessage("go", 1)},
				agenttypes.AgentContext{},
				config,
				func(agenttypes.AgentEvent) error { return nil },
				nil,
				streamFn,
			)
			if err != nil {
				t.Fatalf("RunAgentLoop: %v", err)
			}
			var recorded string
			for _, message := range messages {
				if message.Message != nil && message.Message.Assistant != nil {
					recorded = message.Message.Assistant.ThinkingLevel
				}
			}
			if recorded != tc.expected {
				t.Fatalf("recorded thinking level = %q, want %q", recorded, tc.expected)
			}
		})
	}
}

func piV1ThinkingPtr(level agenttypes.ThinkingLevel) *agenttypes.ThinkingLevel {
	value := level
	return &value
}

func piV1ConvertToLlm(messages []agenttypes.AgentMessage) ([]aitypes.Message, error) {
	out := make([]aitypes.Message, 0, len(messages))
	for _, message := range messages {
		if message.Message != nil {
			out = append(out, *message.Message)
		}
	}
	return out, nil
}

func piV1BlockText(block aitypes.ContentBlock) string {
	if block.Text != nil {
		return block.Text.Text
	}
	return ""
}

func stringSliceEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
