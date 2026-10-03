package harness_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	chord "github.com/minifish-org/pith/packages/chord"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
)

func TestProjectionCommittedFramesAndReadOnly(t *testing.T) {
	ctx := testContext(t)
	registry := h.CreateRegistry()
	task := testTask("test.readonly", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
		return r.Commit(ctx, func(_ durable.Tx, current *durable.TaskRecord) error {
			current.State = durable.TaskState{Status: "terminal", Outcome: &durable.TaskOutcome{Status: "completed"}}
			return nil
		})
	})
	if err := registry.Install(h.Extension{Name: "ro", Tasks: []durable.TaskDefinition{task}}); err != nil {
		t.Fatal(err)
	}
	harness, root := testOpen(t, ctx, registry, nil)
	var id durable.TaskID
	if err := root.Commit(ctx, func(tx durable.Tx) error {
		var err error
		id, err = tx.CreateTask(ctx, task, testJSON(nil), durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "conversation"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	watch, err := root.Watch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Stop()
	graph, err := harness.WatchTaskGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Stop()
	if _, ok := graph.Value().Tasks[strconv.FormatInt(int64(id), 10)]; !ok {
		t.Fatal("graph omitted live task")
	}
	inspection, err := harness.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Scheduling != "paused" {
		t.Fatalf("inspection=%+v", inspection)
	}
	committed := make(chan struct{})
	if err := watch.Start(func(_ context.Context, view h.ConversationView, ops []chord.Op) error {
		found := 0
		for _, entry := range view.Entries {
			if strings.HasPrefix(entry.Kind, "test.atomic.") {
				found++
			}
		}
		if found == 2 && len(ops) > 0 {
			select {
			case <-committed:
			default:
				close(committed)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := root.Commit(ctx, func(tx durable.Tx) error {
		if _, err := tx.AppendEntry(ctx, root.ID(), durable.EntryDraft{Kind: "test.atomic.one"}); err != nil {
			return err
		}
		_, err := tx.AppendEntry(ctx, root.ID(), durable.EntryDraft{Kind: "test.atomic.two"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	<-committed
}

func TestProjectionAgentEvents(t *testing.T) {
	ctx := testContext(t)
	harness, root := testOpen(t, ctx, h.CreateRegistry(), &testModels{})
	if err := root.Configure(ctx, json.RawMessage(`{"model":{"provider":"fixture","modelId":"fixture"}}`)); err != nil {
		t.Fatal(err)
	}
	events, err := h.WatchEvents(ctx, harness, root.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer events.Stop()
	if events.Snapshot().Type != "snapshot" {
		t.Fatal("missing snapshot")
	}
	seen := map[string]bool{}
	var mu sync.Mutex
	ended := make(chan struct{})
	if err := events.Start(func(_ context.Context, batch []h.AgentEvent) error {
		mu.Lock()
		defer mu.Unlock()
		for _, event := range batch {
			seen[event.Type] = true
			if event.Type == "run_end" {
				select {
				case <-ended:
				default:
					close(ended)
				}
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Error(err)
				continue
			}
			var fields map[string]any
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Error(err)
				continue
			}
			if fields["type"] != event.Type {
				t.Errorf("wire lost type: %s", encoded)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	submission, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("hello"), RequestID: "events"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := submission.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	<-ended
	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"run_start", "run_end", "message_end", "submission"} {
		if !seen[name] {
			t.Errorf("missing %s: %v", name, seen)
		}
	}
}
