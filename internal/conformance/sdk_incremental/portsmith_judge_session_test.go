package sdk_incremental_test

import (
	"context"
	"encoding/json"
	at "github.com/minifish-org/pith/packages/agent/types"
	ai "github.com/minifish-org/pith/packages/ai/types"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func response(reason ai.StopReason, blocks ...ai.ContentBlock) *ai.AssistantMessageEventStream {
	s := ai.NewAssistantMessageEventStream()
	m := ai.NewAssistantMessage(ai.ApiOpenAICompletions, ai.ProviderOpenAI, "test", 1)
	m.Content = blocks
	m.StopReason = reason
	m.Usage.Input = 10
	m.Usage.Output = 5
	m.Usage.TotalTokens = 15
	if reason == ai.StopReasonError {
		msg := "503 service unavailable"
		m.ErrorMessage = &msg
		s.Push(ai.NewErrorEvent(reason, m))
	} else {
		s.Push(ai.NewDoneEvent(reason, m))
	}
	return s
}
func TestPortsmithJudgeSDKSession(t *testing.T) {
	dir := t.TempDir()
	manager, err := sdk.OpenSession(filepath.Join(dir, "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	executions := 0
	registry, err := sdk.NewToolRegistry(dir, []sdk.ToolDefinition{{Name: "verify_candidate", Description: "verify", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (sdk.ToolResult, error) {
		executions++
		return sdk.ToolResult{Content: []ai.ContentBlock{ai.TextBlock("judge-pass")}}, nil
	}}}, []string{"verify_candidate"}, nil, sdk.ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	stream := at.StreamFn(func(m *ai.Model, c *ai.TranscriptContext, o *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		requests++
		if m.ContextWindow != 1000000 || m.MaxTokens != 384000 {
			t.Error("lost model options")
		}
		if requests == 1 {
			call := ai.NewToolCall("v1", "verify_candidate", json.RawMessage(`{}`))
			return response(ai.StopReasonToolUse, ai.ContentBlock{Type: ai.ContentTypeToolCall, ToolCall: &call})
		}
		data, _ := json.Marshal(c)
		if !strings.Contains(string(data), "judge-pass") {
			t.Error("tool result absent from next request")
		}
		return response(ai.StopReasonStop, ai.TextBlock("done"))
	})
	session, err := sdk.CreateAgentSession(sdk.SessionOptions{Cwd: dir, Manager: manager, Model: sdk.ModelOptions{Model: model(), StreamFn: stream}, Tools: registry})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	events := []string{}
	off := session.Subscribe(func(e sdk.SessionEvent) { mu.Lock(); events = append(events, e.Type); mu.Unlock() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	res, err := session.Prompt(ctx, "fix code")
	if err != nil {
		t.Fatal(err)
	}
	if executions != 1 || requests != 2 || res.StopReason != ai.StopReasonStop || res.Turns != 2 || res.Usage.Input != 20 || len(res.Messages) < 4 {
		t.Fatalf("loop result %+v executions %d requests %d", res, executions, requests)
	}
	mu.Lock()
	joined := strings.Join(events, ",")
	mu.Unlock()
	a := strings.Index(joined, "tool_execution_start")
	b := strings.Index(joined, "tool_execution_end")
	if a < 0 || b <= a {
		t.Fatal("tool events missing or out of order", joined)
	}
	off()
	session.Close()
	restored, err := sdk.CreateAgentSession(sdk.SessionOptions{Cwd: dir, Manager: manager, Model: sdk.ModelOptions{Model: model(), StreamFn: stream}, Tools: registry})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if len(restored.Messages()) < 4 {
		t.Fatal("conversation lost on reopen")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err = restored.Prompt(cancelled, "no"); err == nil {
		t.Fatal("cancelled prompt accepted")
	}
}
