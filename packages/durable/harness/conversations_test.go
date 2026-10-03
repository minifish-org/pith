package harness_test

import (
	"encoding/json"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
)

func TestConversationSubmissionDedupAndNoModel(t *testing.T) {
	ctx := testContext(t)
	harness, root := testOpen(t, ctx, h.CreateRegistry(), &testModels{})
	first, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("first"), RequestID: "key"})
	if err != nil {
		t.Fatal(err)
	}
	same, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("changed"), RequestID: "key"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != same.ID() {
		t.Fatal("requestId did not deduplicate")
	}
	record, err := first.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "unanswered" || record.Reason != "no_model" {
		t.Fatalf("record=%+v", record)
	}
	if _, err := root.Submit(ctx, h.SubmissionDraft{Type: "write", RequestID: "key", Entry: &durable.EntryDraft{Kind: "test.note"}}); err == nil {
		t.Fatal("cross-type dedup accepted")
	}
	_ = harness
}

func TestConversationAgentConfigureAndForkAsOf(t *testing.T) {
	ctx := testContext(t)
	_, root := testOpen(t, ctx, h.CreateRegistry(), &testModels{})
	if err := root.Configure(ctx, json.RawMessage(`{"instructions":"old"}`)); err != nil {
		t.Fatal(err)
	}
	var cutoff *durable.EntryRecord
	if err := root.Commit(ctx, func(tx durable.Tx) error {
		var err error
		cutoff, err = tx.AppendEntry(ctx, root.ID(), durable.EntryDraft{Kind: "pi.user", Model: []ai.Message{ai.NewUserMessageVariant(ai.NewUserMessage("ancestor", 1))}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := root.Configure(ctx, json.RawMessage(`{"instructions":"new"}`)); err != nil {
		t.Fatal(err)
	}
	child, err := root.Fork(ctx, cutoff.ID, h.CreateOptions{Ownership: durable.ConversationOwnership{Kind: "ownerless"}})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := child.Agent(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if agent.Instructions != "old" {
		t.Fatalf("fork agent=%+v", agent)
	}
	view, err := child.Context(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) != 1 || view.Messages[0].User == nil || view.Messages[0].User.Content.Text != "ancestor" {
		t.Fatalf("fork context=%+v", view.Messages)
	}
	if err := child.Reset(ctx, "handoff"); err != nil {
		t.Fatal(err)
	}
	view, err = child.Context(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Head == nil || view.Head.Kind != "pi.reset" {
		t.Fatalf("reset head=%+v", view.Head)
	}
}
