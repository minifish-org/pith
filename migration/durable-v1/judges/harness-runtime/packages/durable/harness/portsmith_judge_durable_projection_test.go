package harness_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	chord "github.com/minifish-org/pith/packages/chord"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
)

func TestPortsmithJudgeDurableCommittedFramesAndReadOnly(t *testing.T) {
	ctx := djContext(t)
	reg := h.CreateRegistry()
	var calls atomic.Int32
	task := djTask("judge.readonly", func(ctx context.Context, _ durable.TaskRecord, r durable.TaskRuntime) error {
		calls.Add(1)
		return r.Commit(ctx, func(_ durable.Tx, c *durable.TaskRecord) error { c.State = djTerminal("completed", nil); return nil })
	})
	djCheck(t, reg.Install(h.Extension{Name: "readonly", Tasks: []durable.TaskDefinition{task}}))
	hh, root := djOpen(t, ctx, reg, nil)
	id := djCreate(t, ctx, root, task, nil, false)
	watch, e := root.Watch(ctx)
	djCheck(t, e)
	defer watch.Stop()
	graph, e := hh.WatchTaskGraph(ctx)
	djCheck(t, e)
	defer graph.Stop()
	if _, ok := graph.Value().Tasks[strconv.FormatInt(int64(id), 10)]; !ok {
		t.Fatal("graph omitted live task")
	}
	_, e = hh.Usage(ctx)
	djCheck(t, e)
	_, e = root.Context(ctx)
	djCheck(t, e)
	inspection, e := hh.Inspect(ctx)
	djCheck(t, e)
	if inspection.Scheduling != "paused" || calls.Load() != 0 {
		t.Fatalf("viewer enabled scheduling: %+v calls=%d", inspection, calls.Load())
	}
	committed := make(chan struct{})
	var once sync.Once
	djCheck(t, watch.Start(func(_ context.Context, view h.ConversationView, ops []chord.Op) error {
		found := 0
		for _, entry := range view.Entries {
			if strings.HasPrefix(entry.Kind, "judge.atomic.") {
				found++
			}
		}
		if found == 1 {
			t.Error("watch exposed half an atomic commit")
		}
		if found == 2 {
			if len(ops) == 0 {
				t.Error("complete commit emitted no structural operations")
			}
			once.Do(func() { close(committed) })
		}
		return nil
	}))
	djCheck(t, root.Commit(ctx, func(tx durable.Tx) error {
		if _, e := tx.AppendEntry(ctx, root.ID(), durable.EntryDraft{Kind: "judge.atomic.one"}); e != nil {
			return e
		}
		_, e := tx.AppendEntry(ctx, root.ID(), durable.EntryDraft{Kind: "judge.atomic.two"})
		return e
	}))
	djAwait(t, ctx, committed)
	if calls.Load() != 0 {
		t.Fatal("passive entry commit ran paused task")
	}
	djReceipt(t, ctx, hh, id, "completed")
}

func TestPortsmithJudgeDurableAgentEventsAndUsage(t *testing.T) {
	ctx := djContext(t)
	models := &djModels{}
	hh, root := djOpen(t, ctx, h.CreateRegistry(), models)
	djConfigure(t, ctx, root)
	events, e := h.WatchEvents(ctx, hh, root.ID())
	djCheck(t, e)
	defer events.Stop()
	if events.Snapshot().Type != "snapshot" {
		t.Fatal("event attachment did not hydrate snapshot")
	}
	var mu sync.Mutex
	typesSeen := map[string]int{}
	ended := make(chan struct{})
	var once sync.Once
	djCheck(t, events.Start(func(_ context.Context, batch []h.AgentEvent) error {
		mu.Lock()
		defer mu.Unlock()
		for _, event := range batch {
			typesSeen[event.Type]++
			if event.Type == "run_end" {
				once.Do(func() { close(ended) })
			}
			encoded, e := json.Marshal(event)
			if e != nil {
				t.Error(e)
			}
			var fields map[string]any
			if e = json.Unmarshal(encoded, &fields); e != nil {
				t.Error(e)
			}
			if fields["type"] != event.Type {
				t.Errorf("event wire lost type: %s", encoded)
			}
		}
		return nil
	}))
	djSettled(t, ctx, djSubmit(t, ctx, root, djInput("hello", "events")), "done")
	djAwait(t, ctx, ended)
	mu.Lock()
	for _, name := range []string{"run_start", "run_end", "message_end", "submission"} {
		if typesSeen[name] == 0 {
			t.Errorf("missing committed lifecycle %s: %v", name, typesSeen)
		}
	}
	mu.Unlock()
	usage, e := hh.Usage(ctx)
	djCheck(t, e)
	data, _ := json.Marshal(usage)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	if len(fields["models"]) == 0 {
		t.Fatalf("model usage category missing: %s", data)
	}
}

func TestPortsmithJudgeDurableManualCompactionKeepsHistory(t *testing.T) {
	ctx := djContext(t)
	var summaries atomic.Int32
	models := &djModels{run: func(_ context.Context, r h.ModelRequest, _ func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if !r.Summary {
			t.Error("manual compaction invoked ordinary generation")
		}
		summaries.Add(1)
		return djAnswer("summary of earlier work"), nil
	}}
	hh, root := djOpen(t, ctx, h.CreateRegistry(), models)
	djConfigure(t, ctx, root)
	djCheck(t, root.Commit(ctx, func(tx durable.Tx) error {
		for _, text := range []string{strings.Repeat("earlier work ", 30000), "recent work"} {
			if _, e := tx.AppendEntry(ctx, root.ID(), durable.EntryDraft{Kind: "pi.user", Model: []ai.Message{ai.NewUserMessageVariant(ai.NewUserMessage(text, 1))}}); e != nil {
				return e
			}
		}
		return nil
	}))
	before := djHistory(t, ctx, root)
	id, e := root.Compact(ctx, "summarize decisions")
	djCheck(t, e)
	receipt := djReceipt(t, ctx, hh, id, "completed")
	if receipt.State.Outcome == nil || summaries.Load() != 1 {
		t.Fatalf("summary task=%+v calls=%d", receipt, summaries.Load())
	}
	view, e := root.Context(ctx)
	djCheck(t, e)
	if view.Head == nil || view.Head.Kind != "pi.compaction" {
		t.Fatalf("summary did not head model context: %+v", view.Head)
	}
	history := djHistory(t, ctx, root)
	if len(history) <= len(before) {
		t.Fatal("compaction deleted original transcript instead of append")
	}
	if !strings.Contains(string(djJSON(view.Messages)), "summary of earlier work") {
		t.Fatal("summary omitted from model context")
	}
	if !strings.Contains(string(djJSON(view.Messages)), "recent work") {
		t.Fatal("compaction lost recent context")
	}
}
