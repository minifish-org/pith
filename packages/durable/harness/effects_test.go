package harness_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
)

func testToolDeclaration(name string) ai.Tool {
	return ai.NewTool(name, name, json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`))
}

func testToolCall(name string) ai.AssistantMessage {
	message := testAnswer("")
	_ = json.Unmarshal(testJSON(map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "id": "call-1", "name": name, "arguments": map[string]any{}}},
		"api": "openai-completions", "provider": "fixture", "model": "fixture", "stopReason": "toolUse", "timestamp": 1,
	}), &message)
	return message
}

func testToolText(entries []durable.EntryRecord) (string, bool) {
	for _, entry := range entries {
		for _, message := range entry.Model {
			if message.ToolResult != nil {
				var builder strings.Builder
				for _, block := range message.ToolResult.Content {
					if block.Text != nil {
						builder.WriteString(block.Text.Text)
					}
				}
				return builder.String(), message.ToolResult.IsError
			}
		}
	}
	return "", false
}

func TestEffectsToolRoundAndOutputBounds(t *testing.T) {
	ctx := testContext(t)
	registry := h.CreateRegistry()
	executions := atomic.Int32{}
	tool := h.ToolRegistration{
		Declaration:  testToolDeclaration("output"),
		OutputLimits: &h.OutputLimits{MaxBytes: 10, MaxLines: 1, Retain: "tail"},
		Execute: func(ctx context.Context, _ json.RawMessage, api h.ToolAPI) (h.ToolResult, error) {
			executions.Add(1)
			for _, chunk := range [][]byte{[]byte("discard\n"), {0xe4}, {0xbd, 0xa0}, []byte("\x00好\n")} {
				if err := api.Output(chunk); err != nil {
					return h.ToolResult{}, err
				}
			}
			return h.ToolResult{Details: json.RawMessage(`null`), Usage: &ai.Usage{Input: 2, Output: 1, TotalTokens: 3}}, nil
		},
	}
	if err := registry.Install(h.Extension{Name: "tools", Tools: []h.ToolRegistration{tool}}); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	models := &testModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if requests.Add(1) == 1 {
			return testToolCall("output"), nil
		}
		return testAnswer("done"), nil
	}}
	harness, root := testOpen(t, ctx, registry, models)
	if err := root.Configure(ctx, json.RawMessage(`{"model":{"provider":"fixture","modelId":"fixture"}}`)); err != nil {
		t.Fatal(err)
	}
	submission, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("output"), RequestID: "output"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := submission.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := root.Entries(ctx, durable.EntryQuery{}, 1000, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, isError := testToolText(page.Items)
	if isError || !strings.HasPrefix(text, "你好\n") || strings.Contains(text, "discard") {
		t.Fatalf("bounds text=%q error=%v", text, isError)
	}
	if executions.Load() != 1 {
		t.Fatalf("executions=%d", executions.Load())
	}
	usage, err := harness.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(usage)
	if !strings.Contains(string(encoded), "tools") {
		t.Fatalf("usage=%s", encoded)
	}
}

func TestEffectsToolReplayPolicies(t *testing.T) {
	for _, policy := range []struct {
		stored, current string
		rerun           bool
	}{{"unsafe", "safe", false}, {"", "safe", false}} {
		t.Run(policy.stored+"-"+policy.current, func(t *testing.T) {
			ctx := testContext(t)
			registry := h.CreateRegistry()
			var executions atomic.Int32
			install := func(replay string) {
				tool := h.ToolRegistration{Declaration: testToolDeclaration("work"), Replay: replay, Execute: func(context.Context, json.RawMessage, h.ToolAPI) (h.ToolResult, error) {
					executions.Add(1)
					return h.ToolResult{}, nil
				}}
				if err := registry.Install(h.Extension{Name: "tools", Tools: []h.ToolRegistration{tool}}); err != nil {
					t.Fatal(err)
				}
			}
			install(policy.stored)
			var requests atomic.Int32
			models := &testModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
				if requests.Add(1) == 1 {
					return testToolCall("work"), nil
				}
				return testAnswer("done"), nil
			}}
			_, root := testOpen(t, ctx, registry, models)
			if err := root.Configure(ctx, json.RawMessage(`{"model":{"provider":"fixture","modelId":"fixture"}}`)); err != nil {
				t.Fatal(err)
			}
			submission, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("work"), RequestID: "work"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := submission.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if executions.Load() != 1 {
				t.Fatalf("executions=%d", executions.Load())
			}
		})
	}
}
