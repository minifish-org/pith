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
	sqlite "github.com/minifish-org/pith/packages/durable/storage/sqlite"
)

func djFileOpen(t *testing.T, ctx context.Context, path string, reg *h.Registry, models h.ModelRunner) (*h.Harness, *h.Conversation) {
	t.Helper()
	store, e := sqlite.Open(ctx, path, sqlite.Options{})
	djCheck(t, e)
	hh, e := h.Open(ctx, store, h.Options{Registry: reg, Models: models})
	djCheck(t, e)
	root, e := hh.Root(ctx, h.RootOptions{})
	djCheck(t, e)
	return hh, root
}
func djToolText(entries []durable.EntryRecord) (string, bool) {
	for _, e := range entries {
		for _, m := range e.Model {
			if m.ToolResult != nil {
				var out strings.Builder
				for _, c := range m.ToolResult.Content {
					if c.Text != nil {
						out.WriteString(c.Text.Text)
					}
				}
				return out.String(), m.ToolResult.IsError
			}
		}
	}
	return "", false
}

func TestPortsmithJudgeDurableToolReplayPoliciesAndIntent(t *testing.T) {
	for _, policy := range []struct {
		stored, current string
		rerun           bool
	}{{"safe", "safe", true}, {"safe", "unsafe", false}, {"unsafe", "safe", false}, {"", "safe", false}} {
		t.Run(policy.stored+"-"+policy.current, func(t *testing.T) {
			ctx := djContext(t)
			path := t.TempDir() + "/state.sqlite"
			reg := h.CreateRegistry()
			started := make(chan struct{})
			var executions, hooks atomic.Int32
			var retained h.ToolAPI
			execute := func(ctx context.Context, _ json.RawMessage, api h.ToolAPI) (h.ToolResult, error) {
				retained = api
				run := executions.Add(1)
				djCheck(t, api.Output([]byte("partial\n")))
				djCheck(t, api.Details(ctx, djJSON(map[string]any{"run": run})))
				if run == 1 {
					close(started)
					<-ctx.Done()
					return h.ToolResult{}, ctx.Err()
				}
				return h.ToolResult{}, nil
			}
			install := func(replay string) {
				djCheck(t, reg.Install(h.Extension{Name: "tools", Tools: []h.ToolRegistration{{Declaration: djDeclaration("work"), Replay: replay, Execute: execute}}, Hooks: []h.HookRegistration{{Task: "pi.tool", Handlers: map[string]h.HookHandler{"beforeTool": func(ctx context.Context, _ json.RawMessage, api h.HookAPI) (json.RawMessage, error) {
					hooks.Add(1)
					_, e := api.Memo(ctx, "decision", djJSON("fixed"))
					return nil, e
				}}}}}))
			}
			install(policy.stored)
			var requests atomic.Int32
			models := &djModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
				if requests.Add(1) == 1 {
					return djCall("work"), nil
				}
				return djAnswer("done"), nil
			}}
			hh, root := djFileOpen(t, ctx, path, reg, models)
			djConfigure(t, ctx, root)
			submission := djSubmit(t, ctx, root, djInput("work", "request"))
			id := submission.ID()
			djAwait(t, ctx, started)
			djCheck(t, hh.Close(ctx))
			if e := retained.Output([]byte("late")); e == nil {
				t.Fatal("closed invocation accepted output")
			}
			install(policy.current)
			hh, root = djFileOpen(t, ctx, path, reg, models)
			defer func() { djCheck(t, hh.Close(context.Background())) }()
			pending, e := hh.Submission(ctx, id)
			djCheck(t, e)
			djSettled(t, ctx, pending, "done")
			expected := int32(1)
			if policy.rerun {
				expected = 2
			}
			if executions.Load() != expected || hooks.Load() != 1 {
				t.Fatalf("intent replay executions=%d hooks=%d want executions=%d hooks=1", executions.Load(), hooks.Load(), expected)
			}
			text, isError := djToolText(djHistory(t, ctx, root))
			if isError == policy.rerun || !strings.Contains(text, "partial\n") {
				t.Fatalf("recovered result text=%q error=%v", text, isError)
			}
			if !policy.rerun && !strings.Contains(text, "interrupted") {
				t.Fatalf("unsafe result concealed interruption: %q", text)
			}
		})
	}
}

func TestPortsmithJudgeDurableToolRoundParallelAndSequential(t *testing.T) {
	for _, mode := range []string{"parallel", "sequential"} {
		t.Run(mode, func(t *testing.T) {
			ctx := djContext(t)
			reg := h.CreateRegistry()
			firstStarted := make(chan struct{})
			secondStarted := make(chan struct{})
			firstRelease := make(chan struct{})
			var firstDone atomic.Bool
			first := h.ToolRegistration{Declaration: djDeclaration("first"), ExecutionMode: mode, Execute: func(ctx context.Context, _ json.RawMessage, _ h.ToolAPI) (h.ToolResult, error) {
				close(firstStarted)
				select {
				case <-firstRelease:
				case <-ctx.Done():
					return h.ToolResult{}, ctx.Err()
				}
				firstDone.Store(true)
				return h.ToolResult{Content: []ai.ContentBlock{}}, nil
			}}
			second := h.ToolRegistration{Declaration: djDeclaration("second"), Execute: func(context.Context, json.RawMessage, h.ToolAPI) (h.ToolResult, error) {
				if mode == "sequential" && !firstDone.Load() {
					t.Error("second sequential callback preceded first completion")
				}
				close(secondStarted)
				return h.ToolResult{Content: []ai.ContentBlock{}}, nil
			}}
			djCheck(t, reg.Install(h.Extension{Name: "round", Tools: []h.ToolRegistration{first, second}}))
			var requests atomic.Int32
			models := &djModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
				if requests.Add(1) == 1 {
					message := djCall("first")
					other := djCall("second")
					other.Content[0].ToolCall.Id = "call-2"
					message.Content = append(message.Content, other.Content[0])
					return message, nil
				}
				return djAnswer("done"), nil
			}}
			_, root := djOpen(t, ctx, reg, models)
			djConfigure(t, ctx, root)
			s := djSubmit(t, ctx, root, djInput("round", "round"))
			djAwait(t, ctx, firstStarted)
			if mode == "parallel" {
				djAwait(t, ctx, secondStarted)
			}
			close(firstRelease)
			djSettled(t, ctx, s, "done")
			djAwait(t, ctx, secondStarted)
		})
	}
}

func TestPortsmithJudgeDurableBoundedOutputDetailsAndUsage(t *testing.T) {
	ctx := djContext(t)
	reg := h.CreateRegistry()
	tool := h.ToolRegistration{Declaration: djDeclaration("output"), OutputLimits: &h.OutputLimits{MaxBytes: 10, MaxLines: 1, Retain: "tail"}, Execute: func(ctx context.Context, _ json.RawMessage, api h.ToolAPI) (h.ToolResult, error) {
		for _, chunk := range [][]byte{[]byte("discard\n"), {0xe4}, {0xbd, 0xa0}, []byte("\x00好\n")} {
			if e := api.Output(chunk); e != nil {
				return h.ToolResult{}, e
			}
		}
		if e := api.Details(ctx, json.RawMessage(`{"old":true}`)); e != nil {
			return h.ToolResult{}, e
		}
		return h.ToolResult{Details: json.RawMessage(`null`), Usage: &ai.Usage{Input: 2, Output: 1, TotalTokens: 3}}, nil
	}}
	djCheck(t, reg.Install(h.Extension{Name: "output", Tools: []h.ToolRegistration{tool}}))
	var requests atomic.Int32
	models := &djModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if requests.Add(1) == 1 {
			return djCall("output"), nil
		}
		return djAnswer("done"), nil
	}}
	hh, root := djOpen(t, ctx, reg, models)
	djConfigure(t, ctx, root)
	djSettled(t, ctx, djSubmit(t, ctx, root, djInput("output", "output")), "done")
	history := djHistory(t, ctx, root)
	text, isError := djToolText(history)
	if isError || !strings.HasPrefix(text, "你好\n") || strings.Contains(text, "discard") || strings.ContainsRune(text, '\x00') {
		t.Fatalf("UTF8/control/tail bounds result=%q error=%v", text, isError)
	}
	found := false
	for _, entry := range history {
		for _, m := range entry.Model {
			if m.ToolResult != nil {
				encoded, _ := json.Marshal(m.ToolResult)
				var value map[string]json.RawMessage
				_ = json.Unmarshal(encoded, &value)
				if string(value["details"]) != "null" {
					t.Fatalf("explicit null lost to running details: %s", encoded)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("no persisted tool result")
	}
	usage, e := hh.Usage(ctx)
	djCheck(t, e)
	encoded, _ := json.Marshal(usage)
	if !strings.Contains(string(encoded), "tools") {
		t.Fatalf("tool usage not exposed separately: %s", encoded)
	}
}
