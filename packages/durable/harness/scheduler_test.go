package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	ai "github.com/minifish-org/pith/packages/ai/types"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
	memory "github.com/minifish-org/pith/packages/durable/storage/memory"
)

func testJSON(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testOpen(t *testing.T, ctx context.Context, registry *h.Registry, models h.ModelRunner) (*h.Harness, *h.Conversation) {
	t.Helper()
	if models == nil {
		models = &testModels{}
	}
	harness, err := h.Open(ctx, memory.New(), h.Options{Registry: registry, Models: models})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = harness.Close(context.Background()) })
	root, err := harness.Root(ctx, h.RootOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return harness, root
}

func testTask(name string, run durable.PhaseHandler) durable.TaskDefinition {
	return durable.TaskDefinition{
		Name:    name,
		Version: 1,
		Initial: func(json.RawMessage) (json.RawMessage, error) { return testJSON(map[string]any{"phase": "run"}), nil },
		Phases:  map[string]durable.PhaseHandler{"run": run},
		Abort: func(_ context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
			return r.Commit(context.Background(), func(_ durable.Tx, current *durable.TaskRecord) error {
				current.State = durable.TaskState{Status: "terminal", Outcome: &durable.TaskOutcome{Status: "aborted"}}
				return nil
			})
		},
	}
}

func entryQueryAll() durable.EntryQuery { return durable.EntryQuery{} }

type testModels struct {
	run func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error)
}

func (m *testModels) Resolve(context.Context, h.ModelRef) (*ai.Model, error) {
	var model ai.Model
	_ = json.Unmarshal([]byte(`{"id":"fixture","name":"fixture","api":"openai-completions","provider":"fixture","contextWindow":128000,"maxTokens":4096,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0}}`), &model)
	return &model, nil
}

func (m *testModels) Run(ctx context.Context, request h.ModelRequest, emit func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
	if m.run != nil {
		return m.run(ctx, request, emit)
	}
	return testAnswer("done"), nil
}

func testAnswer(text string) ai.AssistantMessage {
	var message ai.AssistantMessage
	_ = json.Unmarshal(testJSON(map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}},
		"api": "openai-completions", "provider": "fixture", "model": "fixture",
		"stopReason": "stop", "timestamp": 1,
		"usage": map[string]any{"input": 2, "output": 1, "totalTokens": 3, "cost": map[string]any{}},
	}), &message)
	return message
}

func TestSchedulerCommitsProgressAndFaults(t *testing.T) {
	ctx := testContext(t)
	registry := h.CreateRegistry()
	var steps atomic.Int32
	progressing := testTask("test.progress", func(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
		steps.Add(1)
		var checkpoint struct {
			Step int `json:"step"`
		}
		_ = json.Unmarshal(task.State.Checkpoint, &checkpoint)
		return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
			if checkpoint.Step == 0 {
				current.State = durable.TaskState{Status: "running", Checkpoint: testJSON(map[string]any{"phase": "run", "step": 1})}
			} else {
				current.State = durable.TaskState{Status: "terminal", Outcome: &durable.TaskOutcome{Status: "completed", Result: testJSON(7)}}
			}
			return nil
		})
	})
	noProgress := testTask("test.stuck", func(context.Context, durable.TaskRecord, durable.TaskRuntime) error { return nil })
	throwing := testTask("test.throw", func(context.Context, durable.TaskRecord, durable.TaskRuntime) error {
		return errors.New("boom")
	})
	if err := registry.Install(h.Extension{Name: "test", Tasks: []durable.TaskDefinition{progressing, noProgress, throwing}}); err != nil {
		t.Fatal(err)
	}
	harness, root := testOpen(t, ctx, registry, nil)
	create := func(def durable.TaskDefinition) durable.TaskID {
		var id durable.TaskID
		if err := root.Commit(ctx, func(tx durable.Tx) error {
			var err error
			id, err = tx.CreateTask(ctx, def, testJSON(nil), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "conversation"}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	if err := harness.Resume(); err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.WaitForTask(ctx, create(progressing))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State.Outcome.Status != "completed" || steps.Load() != 2 {
		t.Fatalf("progress receipt=%+v steps=%d", receipt, steps.Load())
	}
	for _, def := range []durable.TaskDefinition{noProgress, throwing} {
		record, err := harness.WaitForTask(ctx, create(def))
		if err != nil {
			t.Fatal(err)
		}
		if record.State.Outcome.Status != "faulted" {
			t.Fatalf("fault outcome=%+v", record.State.Outcome)
		}
	}
}

func TestSchedulerBlockedUntilRegistryInstall(t *testing.T) {
	ctx := testContext(t)
	registry := h.CreateRegistry()
	var calls atomic.Int32
	task := testTask("test.hot", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
		calls.Add(1)
		return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
			current.State = durable.TaskState{Status: "terminal", Outcome: &durable.TaskOutcome{Status: "completed"}}
			return nil
		})
	})
	harness, root := testOpen(t, ctx, registry, nil)
	var id durable.TaskID
	if err := root.Commit(ctx, func(tx durable.Tx) error {
		var err error
		id, err = tx.CreateTask(ctx, task, testJSON(nil), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "conversation"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	inspection, err := harness.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Tasks) != 1 || inspection.Tasks[0].State.Kind != "blocked" {
		t.Fatalf("inspection=%+v", inspection)
	}
	if err := harness.Resume(); err != nil {
		t.Fatal(err)
	}
	if err := registry.Install(h.Extension{Name: "test", Tasks: []durable.TaskDefinition{task}}); err != nil {
		t.Fatal(err)
	}
	record, err := harness.WaitForTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if record.State.Outcome.Status != "completed" || calls.Load() != 1 {
		t.Fatalf("record=%+v calls=%d", record, calls.Load())
	}
}
