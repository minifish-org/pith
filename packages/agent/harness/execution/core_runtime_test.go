package harnessexecution

import (
	"encoding/json"
	"strings"
	"testing"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// These self-tests translate the upstream execution-tools and
// execution-assistant scenarios into deterministic Go tests.

func TestPrepareToolCallUnavailable(t *testing.T) {
	call := aitypes.NewToolCall("c1", "missing", json.RawMessage(`{}`))
	_, immediate := PrepareToolCall(call, nil)
	if immediate == nil || !immediate.IsError {
		t.Fatalf("unavailable tool must produce an immediate error: %+v", immediate)
	}
	if len(immediate.Result.Content) == 0 || immediate.Result.Content[0].Text == nil || !strings.Contains(immediate.Result.Content[0].Text.Text, "missing") {
		t.Fatalf("unavailable tool message = %+v", immediate.Result)
	}
}

func TestApplyBeforeToolDecisionBlocks(t *testing.T) {
	call := aitypes.NewToolCall("c1", "echo", json.RawMessage(`{}`))
	definitions := []HarnessToolDefinition{{Tool: aitypes.Tool{Name: "echo", Description: "echo"}}}
	prepared, immediate := PrepareToolCall(call, definitions)
	if immediate != nil {
		t.Fatalf("prepare = %+v", immediate)
	}
	cleared, blocked := ApplyBeforeToolDecision(prepared, &BeforeToolDecision{Block: &BeforeToolBlock{Reason: "denied", Terminate: true}})
	if blocked == nil || !blocked.IsError || !blocked.Terminate {
		t.Fatalf("block decision = %+v", blocked)
	}
	if cleared.ToolCall.Name != "" {
		t.Fatalf("blocked call must not clear: %+v", cleared)
	}
}

func TestFinalizeToolCallAppliesPatch(t *testing.T) {
	yes := true
	executed := ExecutedToolCall{Result: agenttypes.AgentToolResult[any]{Content: []aitypes.ContentBlock{aitypes.TextBlock("before")}}, IsError: false}
	call := ClearedToolCall{ToolCall: aitypes.NewToolCall("c1", "echo", json.RawMessage(`{}`))}
	patched := FinalizeToolCall(call, executed, &AfterToolPatch{Content: []aitypes.ContentBlock{aitypes.TextBlock("after")}, IsError: &yes, Terminate: &yes})
	if !patched.IsError || !patched.Terminate {
		t.Fatalf("finalized = %+v", patched)
	}
	if patched.Result.Content[0].Text == nil || patched.Result.Content[0].Text.Text != "after" {
		t.Fatalf("patched content = %+v", patched.Result.Content)
	}
	message := CreateToolResultMessage(patched)
	if message.Role != "toolResult" || message.ToolCallId != "c1" || !message.IsError {
		t.Fatalf("tool result message = %+v", message)
	}
}

func TestConsumeAssistantStreamRejectsDoubleStart(t *testing.T) {
	stream := aitypes.NewAssistantMessageEventStream()
	message := aitypes.NewAssistantMessage("openai-completions", "fixture", "fixture", 1)
	stream.Push(aitypes.NewStartEvent(message))
	stream.Push(aitypes.NewStartEvent(message))
	stream.End(&message)
	if _, err := ConsumeAssistantStream(stream, nil, nil, nil); err == nil {
		t.Fatal("double start must be rejected")
	}
}
