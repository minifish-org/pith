package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ai "github.com/minifish-org/pith/packages/ai/types"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
	memory "github.com/minifish-org/pith/packages/durable/storage/memory"
)

// These fixtures specify observations independently of candidate code. Deadlines
// only catch hangs; channel acknowledgements, never delays, establish ordering.
func djJSON(v any) json.RawMessage {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	return b
}
func djCheck(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func djContext(t *testing.T) context.Context {
	t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(c)
	return ctx
}
func djAwait(t *testing.T, ctx context.Context, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func djTerminal(status string, v any) durable.TaskState {
	return durable.TaskState{Status: "terminal", Outcome: &durable.TaskOutcome{Status: status, Result: djJSON(v)}}
}
func djAbort(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
	return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
		current.State = djTerminal("aborted", nil)
		return nil
	})
}
func djTask(name string, run durable.PhaseHandler) durable.TaskDefinition {
	return durable.TaskDefinition{Name: name, Version: 1, Initial: func(json.RawMessage) (json.RawMessage, error) { return djJSON(map[string]any{"phase": "run"}), nil }, Phases: map[string]durable.PhaseHandler{"run": run}, Abort: djAbort}
}
func djOpen(t *testing.T, ctx context.Context, registry *h.Registry, models h.ModelRunner) (*h.Harness, *h.Conversation) {
	t.Helper()
	if models == nil {
		models = &djModels{}
	}
	hh, e := h.Open(ctx, memory.New(), h.Options{Registry: registry, Models: models})
	djCheck(t, e)
	t.Cleanup(func() { djCheck(t, hh.Close(context.Background())) })
	root, e := hh.Root(ctx, h.RootOptions{})
	djCheck(t, e)
	return hh, root
}
func djCreate(t *testing.T, ctx context.Context, c *h.Conversation, def durable.TaskDefinition, input any, background bool) durable.TaskID {
	t.Helper()
	var id durable.TaskID
	djCheck(t, c.Commit(ctx, func(tx durable.Tx) error {
		var e error
		id, e = tx.CreateTask(ctx, def, djJSON(input), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "conversation"}, Background: background})
		return e
	}))
	return id
}
func djReceipt(t *testing.T, ctx context.Context, hh *h.Harness, id durable.TaskID, status string) *durable.TaskRecord {
	t.Helper()
	r, e := hh.WaitForTask(ctx, id)
	djCheck(t, e)
	if r == nil || r.State.Status != "terminal" || r.State.Outcome == nil || r.State.Outcome.Status != status {
		t.Fatalf("task %d receipt=%+v want %s", id, r, status)
	}
	return r
}

type djModels struct {
	mu       sync.Mutex
	requests []h.ModelRequest
	run      func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error)
	resolve  func(context.Context, h.ModelRef) (*ai.Model, error)
}

func (m *djModels) Resolve(ctx context.Context, ref h.ModelRef) (*ai.Model, error) {
	if m.resolve != nil {
		return m.resolve(ctx, ref)
	}
	var model ai.Model
	_ = json.Unmarshal([]byte(`{"id":"fixture","name":"fixture","api":"openai-completions","provider":"fixture","contextWindow":128000,"maxTokens":4096,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`), &model)
	return &model, nil
}
func (m *djModels) Run(ctx context.Context, r h.ModelRequest, emit func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
	m.mu.Lock()
	m.requests = append(m.requests, r)
	m.mu.Unlock()
	if m.run != nil {
		return m.run(ctx, r, emit)
	}
	return djAnswer("done"), nil
}
func (m *djModels) Requests() []h.ModelRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]h.ModelRequest(nil), m.requests...)
}
func djAnswer(text string) ai.AssistantMessage {
	var m ai.AssistantMessage
	_ = json.Unmarshal(djJSON(map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}, "api": "openai-completions", "provider": "fixture", "model": "fixture", "stopReason": "stop", "timestamp": 1, "usage": map[string]any{"input": 2, "output": 1, "totalTokens": 3, "cost": map[string]any{"input": 0, "output": 0, "total": 0}}}), &m)
	return m
}
func djCall(name string) ai.AssistantMessage {
	m := djAnswer("")
	_ = json.Unmarshal(djJSON(map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "toolCall", "id": "call-1", "name": name, "arguments": map[string]any{}}}, "api": "openai-completions", "provider": "fixture", "model": "fixture", "stopReason": "toolUse", "timestamp": 1}), &m)
	return m
}
func djDeclaration(name string) ai.Tool {
	var tool ai.Tool
	_ = json.Unmarshal(djJSON(map[string]any{"name": name, "description": name, "parameters": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}), &tool)
	return tool
}
func djConfigure(t *testing.T, ctx context.Context, c *h.Conversation) {
	t.Helper()
	djCheck(t, c.Configure(ctx, json.RawMessage(`{"model":{"provider":"fixture","modelId":"fixture"}}`)))
}

func TestPortsmithJudgeDurableTaskProgressAndFault(t *testing.T) {
	ctx := djContext(t)
	reg := h.CreateRegistry()
	var steps atomic.Int32
	progressing := djTask("judge.progress", func(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
		steps.Add(1)
		var checkpoint struct {
			Step int `json:"step"`
		}
		_ = json.Unmarshal(task.State.Checkpoint, &checkpoint)
		return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
			if checkpoint.Step == 0 {
				current.State = durable.TaskState{Status: "running", Checkpoint: json.RawMessage(`{"phase":"run","step":1}`)}
			} else {
				current.State = djTerminal("completed", 7)
			}
			return nil
		})
	})
	noProgress := djTask("judge.stuck", func(context.Context, durable.TaskRecord, durable.TaskRuntime) error { return nil })
	throwing := djTask("judge.throw", func(context.Context, durable.TaskRecord, durable.TaskRuntime) error {
		return errors.New("judge failure")
	})
	djCheck(t, reg.Install(h.Extension{Name: "judge", Tasks: []durable.TaskDefinition{progressing, noProgress, throwing}}))
	hh, root := djOpen(t, ctx, reg, nil)
	id := djCreate(t, ctx, root, progressing, nil, false)
	receipt := djReceipt(t, ctx, hh, id, "completed")
	if steps.Load() != 2 || string(receipt.State.Outcome.Result) != "7" || len(receipt.Memos) != 0 || len(receipt.State.Checkpoint) != 0 {
		t.Fatalf("bad progress/terminal replacement: steps=%d receipt=%+v", steps.Load(), receipt)
	}
	djReceipt(t, ctx, hh, djCreate(t, ctx, root, noProgress, nil, false), "faulted")
	djReceipt(t, ctx, hh, djCreate(t, ctx, root, throwing, nil, false), "faulted")
}

func TestPortsmithJudgeDurableWaitCancellationAndLifetime(t *testing.T) {
	ctx := djContext(t)
	reg := h.CreateRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	var retained durable.TaskRuntime
	task := djTask("judge.wait", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
		retained = r
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
			current.State = djTerminal("completed", "ok")
			return nil
		})
	})
	djCheck(t, reg.Install(h.Extension{Name: "judge", Tasks: []durable.TaskDefinition{task}}))
	hh, root := djOpen(t, ctx, reg, nil)
	id := djCreate(t, ctx, root, task, nil, false)
	djCheck(t, hh.Resume())
	djAwait(t, ctx, started)
	waitCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := hh.WaitForTask(waitCtx, id); !errors.Is(e, context.Canceled) {
		t.Fatalf("wait error=%v", e)
	}
	state, e := hh.GetTask(ctx, id)
	djCheck(t, e)
	if state.AbortRequested || state.State.Status != "running" {
		t.Fatalf("wait canceled durable work: %+v", state)
	}
	close(release)
	djReceipt(t, ctx, hh, id, "completed")
	if _, e := retained.Memo(ctx, "late", djJSON(1)); e == nil {
		t.Fatal("ended invocation accepted memo")
	}
	if e := retained.Commit(ctx, func(durable.Tx, *durable.TaskRecord) error { return nil }); e == nil {
		t.Fatal("ended invocation accepted commit")
	}
}

func TestPortsmithJudgeDurableBlockedRegistryAndMigration(t *testing.T) {
	ctx := djContext(t)
	reg := h.CreateRegistry()
	var calls atomic.Int32
	task := djTask("judge.hot", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
		calls.Add(1)
		return r.Commit(ctx, func(_ durable.Tx, c *durable.TaskRecord) error { c.State = djTerminal("completed", nil); return nil })
	})
	hh, root := djOpen(t, ctx, reg, nil)
	id := djCreate(t, ctx, root, task, nil, false)
	inspection, e := hh.Inspect(ctx)
	djCheck(t, e)
	if len(inspection.Tasks) != 1 || inspection.Tasks[0].State.Kind != "blocked" || inspection.Tasks[0].State.Reason != "missing_task" || calls.Load() != 0 {
		t.Fatalf("inspection dispatched/forgot blocked task: %+v", inspection)
	}
	djCheck(t, hh.Resume())
	task.Version = 2
	task.Migrate = func(input, checkpoint json.RawMessage, from int) (json.RawMessage, json.RawMessage, error) {
		if from != 1 {
			t.Errorf("migration version=%d", from)
		}
		return input, checkpoint, nil
	}
	djCheck(t, reg.Install(h.Extension{Name: "judge", Tasks: []durable.TaskDefinition{task}}))
	receipt := djReceipt(t, ctx, hh, id, "completed")
	if receipt.Version != 2 || calls.Load() != 1 {
		t.Fatalf("migration/registry wake failed: %+v calls=%d", receipt, calls.Load())
	}
	snapshot := reg.Snapshot()
	djCheck(t, reg.Uninstall("judge"))
	if _, ok := snapshot.Task("judge.hot"); !ok {
		t.Fatal("published snapshot mutated after uninstall")
	}
	blocked := djCreate(t, ctx, root, task, nil, false)
	mark, e := hh.AbortTask(ctx, blocked)
	djCheck(t, e)
	if mark != "marked" {
		t.Fatal(mark)
	}
	djReceipt(t, ctx, hh, blocked, "orphaned")
}

func TestPortsmithJudgeDurableOwnershipHeldCompletion(t *testing.T) {
	ctx := djContext(t)
	reg := h.CreateRegistry()
	created := make(chan struct{})
	release := make(chan struct{})
	var childID durable.TaskID
	child := djTask("judge.child", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		return r.Commit(ctx, func(_ durable.Tx, c *durable.TaskRecord) error {
			c.State = djTerminal("completed", "child")
			return nil
		})
	})
	parent := djTask("judge.parent", func(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
		e := r.Commit(ctx, func(tx durable.Tx, c *durable.TaskRecord) error {
			var e error
			childID, e = tx.CreateTask(ctx, child, djJSON(nil), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "task", TaskID: task.ID}})
			if e != nil {
				return e
			}
			c.State = djTerminal("completed", "parent")
			return nil
		})
		close(created)
		return e
	})
	djCheck(t, reg.Install(h.Extension{Name: "judge", Tasks: []durable.TaskDefinition{child, parent}}))
	hh, root := djOpen(t, ctx, reg, nil)
	id := djCreate(t, ctx, root, parent, nil, false)
	djCheck(t, hh.Resume())
	djAwait(t, ctx, created)
	record, e := hh.GetTask(ctx, id)
	djCheck(t, e)
	if record.State.Status != "completing" {
		t.Fatalf("parent terminalized before owned child: %+v", record)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if e := root.WaitForIdle(cancelCtx); !errors.Is(e, context.Canceled) {
		t.Fatalf("owned work did not block idle: %v", e)
	}
	close(release)
	djReceipt(t, ctx, hh, childID, "completed")
	djReceipt(t, ctx, hh, id, "completed")
	djCheck(t, root.WaitForIdle(ctx))
}

func TestPortsmithJudgeDurableBackgroundAbortBoundary(t *testing.T) {
	ctx := djContext(t)
	reg := h.CreateRegistry()
	started := make(chan struct{})
	task := djTask("judge.background", func(ctx context.Context, _ durable.TaskRecord, _ durable.TaskRuntime) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	djCheck(t, reg.Install(h.Extension{Name: "judge", Tasks: []durable.TaskDefinition{task}}))
	hh, root := djOpen(t, ctx, reg, nil)
	id := djCreate(t, ctx, root, task, nil, true)
	djCheck(t, hh.Resume())
	djAwait(t, ctx, started)
	djCheck(t, root.WaitForIdle(ctx))
	djCheck(t, root.Abort(ctx, h.AbortOptions{}))
	record, e := hh.GetTask(ctx, id)
	djCheck(t, e)
	if record.AbortRequested || record.State.Status == "terminal" {
		t.Fatalf("ordinary abort crossed background: %+v", record)
	}
	djCheck(t, root.Abort(ctx, h.AbortOptions{Background: true}))
	djReceipt(t, ctx, hh, id, "aborted")
}
