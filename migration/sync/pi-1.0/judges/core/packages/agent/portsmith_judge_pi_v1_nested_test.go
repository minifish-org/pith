package agent_test

import (
	"encoding/json"
	"github.com/minifish-org/pith/packages/agent"
	at "github.com/minifish-org/pith/packages/agent/types"
	ai "github.com/minifish-org/pith/packages/ai/types"
	"testing"
)

func TestPortsmithJudgePiV1NestedToolHooks(t *testing.T) {
	executed := 0
	before := 0
	after := 0
	tool := at.AgentTool[any, any]{Tool: ai.Tool{Name: "lookup", Input: ai.JSONSchemaToolInput(json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`))}, Execute: func(id string, args any, _ <-chan struct{}, _ at.AgentToolUpdateCallback[any]) (at.AgentToolResult[any], error) {
		executed++
		return at.AgentToolResult[any]{Content: []ai.ContentBlock{ai.TextBlock("display")}, StructuredContent: json.RawMessage(`{"value":7}`)}, nil
	}}
	call := ai.ToolCall{Id: "outer/1", Name: "lookup", Arguments: json.RawMessage(`{"q":"hello"}`)}
	options := at.RunToolCallOptions{Tools: []at.AgentTool[any, any]{tool}, BeforeToolCall: func(c at.BeforeToolCallContext, _ <-chan struct{}) (*at.BeforeToolCallResult, error) {
		before++
		return nil, nil
	}, AfterToolCall: func(c at.AfterToolCallContext, _ <-chan struct{}) (*at.AfterToolCallResult, error) {
		after++
		return nil, nil
	}}
	outcome := agent.RunToolCall(call, options)
	if outcome.IsError || executed != 1 || before != 1 || after != 1 {
		t.Fatalf("nested call bypassed/shared hook order: %+v counts %d/%d/%d", outcome, executed, before, after)
	}
	if string(outcome.Result.StructuredContent) != `{"value":7}` || outcome.ToolCall.Id != "outer/1" {
		t.Fatal("nested structured result or identity lost")
	}
	blocked := true
	reason := "permission denied"
	options.BeforeToolCall = func(c at.BeforeToolCallContext, _ <-chan struct{}) (*at.BeforeToolCallResult, error) {
		return &at.BeforeToolCallResult{Block: &blocked, Reason: &reason}, nil
	}
	outcome = agent.RunToolCall(call, options)
	if !outcome.IsError || executed != 1 {
		t.Fatal("denied nested tool executed")
	}
}

func TestPortsmithJudgePiV1StructuredReplacement(t *testing.T) {
	tool := at.AgentTool[any, any]{Tool: ai.Tool{Name: "x", Input: ai.JSONSchemaToolInput(json.RawMessage(`{"type":"object"}`))}, Execute: func(_ string, _ any, _ <-chan struct{}, _ at.AgentToolUpdateCallback[any]) (at.AgentToolResult[any], error) {
		return at.AgentToolResult[any]{Content: []ai.ContentBlock{ai.TextBlock("old")}, StructuredContent: json.RawMessage(`{"stale":true}`), IsError: true}, nil
	}}
	options := at.RunToolCallOptions{Tools: []at.AgentTool[any, any]{tool}, AfterToolCall: func(c at.AfterToolCallContext, _ <-chan struct{}) (*at.AfterToolCallResult, error) {
		if !c.IsError {
			t.Error("explicit tool error lost")
		}
		return &at.AfterToolCallResult{Content: []ai.ContentBlock{ai.TextBlock("replacement")}}, nil
	}}
	result := agent.RunToolCall(ai.ToolCall{Id: "1", Name: "x", Arguments: json.RawMessage(`{}`)}, options)
	if !result.IsError || len(result.Result.StructuredContent) != 0 {
		t.Fatalf("content replacement retained stale structured data or lost error: %+v", result)
	}
	options.AfterToolCall = func(c at.AfterToolCallContext, _ <-chan struct{}) (*at.AfterToolCallResult, error) {
		return &at.AfterToolCallResult{Content: []ai.ContentBlock{ai.TextBlock("replacement")}, StructuredContent: json.RawMessage(`{"new":true}`)}, nil
	}
	result = agent.RunToolCall(ai.ToolCall{Id: "2", Name: "x", Arguments: json.RawMessage(`{}`)}, options)
	if string(result.Result.StructuredContent) != `{"new":true}` {
		t.Fatal("explicit structured replacement lost")
	}
}
