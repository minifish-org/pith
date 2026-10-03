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

func TestCompactionManualKeepsHistory(t *testing.T) {
	ctx := testContext(t)
	var summaries atomic.Int32
	models := &testModels{run: func(_ context.Context, request h.ModelRequest, _ func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if !request.Summary {
			t.Errorf("manual compaction invoked ordinary generation")
		}
		summaries.Add(1)
		return testAnswer("summary of earlier work"), nil
	}}
	harness, root := testOpen(t, ctx, h.CreateRegistry(), models)
	if err := root.Configure(ctx, json.RawMessage(`{"model":{"provider":"fixture","modelId":"fixture"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := root.Commit(ctx, func(tx durable.Tx) error {
		for _, text := range []string{strings.Repeat("earlier work ", 30000), "recent work"} {
			if _, err := tx.AppendEntry(ctx, root.ID(), durable.EntryDraft{Kind: "pi.user", Model: []ai.Message{ai.NewUserMessageVariant(ai.NewUserMessage(text, 1))}}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	id, err := root.Compact(ctx, "summarize decisions")
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := harness.WaitForTask(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State.Outcome == nil || summaries.Load() != 1 {
		t.Fatalf("receipt=%+v summaries=%d", receipt, summaries.Load())
	}
	view, err := root.Context(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Head == nil || view.Head.Kind != "pi.compaction" {
		t.Fatalf("head=%+v", view.Head)
	}
	encoded, _ := json.Marshal(view.Messages)
	if !strings.Contains(string(encoded), "summary of earlier work") || !strings.Contains(string(encoded), "recent work") {
		t.Fatalf("context=%s", encoded)
	}
}
