package codingagent

import (
	"context"
	"encoding/json"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
	"testing"
)

func TestSessionSearchMakesDeferredToolCallableInNextTurn(t *testing.T) {
	manager, dir := sessionManager(t)
	defer manager.Close()
	calls := 0
	deferred := sessionTool("deferred_probe", func(context.Context, json.RawMessage) (ToolResult, error) {
		calls++
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("ok")}}, nil
	})
	deferred.Exposure = ExposureDeferred
	registry, err := NewToolRegistry(dir, []ToolDefinition{deferred}, nil, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.CloseTools()
	search, err := CreateToolSearchTool(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err = registry.Register(search); err != nil {
		t.Fatal(err)
	}
	turns := 0
	stream := func(_ *aitypes.Model, transcript *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		turns++
		visible := false
		for _, tool := range aiutils.GetCurrentTools(transcript.Messages) {
			visible = visible || tool.Name == "deferred_probe"
		}
		switch turns {
		case 1:
			if visible {
				t.Error("deferred tool exposed before search")
			}
			return sessionDone(aitypes.StopReasonToolUse, sessionToolCall("search", "tool_search", `{"query":"deferred_probe"}`))
		case 2:
			if !visible {
				t.Error("search did not update next model request")
			}
			return sessionDone(aitypes.StopReasonToolUse, sessionToolCall("probe", "deferred_probe", `{}`))
		default:
			return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
		}
	}
	session, err := CreateAgentSession(SessionOptions{Cwd: dir, Manager: manager, Tools: registry, Model: ModelOptions{Model: sessionTestModel(), StreamFn: stream}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if _, err = session.Prompt(context.Background(), "Discover and use the tool"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || turns != 3 {
		t.Fatalf("execution: calls=%d turns=%d", calls, turns)
	}
}
