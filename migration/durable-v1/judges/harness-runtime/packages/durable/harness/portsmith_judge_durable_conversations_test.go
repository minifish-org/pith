package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	durable "github.com/minifish-org/pith/packages/durable"
	h "github.com/minifish-org/pith/packages/durable/harness"
)

func djInput(text, key string) h.SubmissionDraft {
	return h.SubmissionDraft{Type: "input", Content: ai.UserContentText(text), RequestID: key}
}
func djSubmit(t *testing.T, ctx context.Context, c *h.Conversation, d h.SubmissionDraft) *h.Submission {
	t.Helper()
	s, e := c.Submit(ctx, d)
	djCheck(t, e)
	return s
}
func djSettled(t *testing.T, ctx context.Context, s *h.Submission, status string) *durable.SubmissionRecord {
	t.Helper()
	r, e := s.Wait(ctx)
	djCheck(t, e)
	if r.Status != status {
		t.Fatalf("submission=%+v want %s", r, status)
	}
	return r
}
func djHistory(t *testing.T, ctx context.Context, c *h.Conversation) []durable.EntryRecord {
	t.Helper()
	p, e := c.Entries(ctx, durable.EntryQuery{}, 1000, nil)
	djCheck(t, e)
	items := append([]durable.EntryRecord(nil), p.Items...)
	for p.Next != nil {
		p, e = c.Entries(ctx, durable.EntryQuery{}, 1000, p.Next)
		djCheck(t, e)
		items = append(items, p.Items...)
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items
}
func djUserTexts(messages []ai.Message) []string {
	var texts []string
	for _, m := range messages {
		if m.User != nil {
			texts = append(texts, m.User.Content.Text)
		}
	}
	return texts
}

func TestPortsmithJudgeDurableSubmissionDedupAndNoModel(t *testing.T) {
	ctx := djContext(t)
	hh, root := djOpen(t, ctx, h.CreateRegistry(), &djModels{})
	first := djSubmit(t, ctx, root, djInput("first", "key"))
	same := djSubmit(t, ctx, root, djInput("changed payload", "key"))
	if first.ID() != same.ID() {
		t.Fatal("same requestId allocated two submissions")
	}
	receipt := djSettled(t, ctx, first, "unanswered")
	if receipt.Reason != "no_model" {
		t.Fatalf("no_model=%+v", receipt)
	}
	before := djHistory(t, ctx, root)
	_, e := root.Submit(ctx, h.SubmissionDraft{Type: "write", RequestID: "key", Entry: &durable.EntryDraft{Kind: "judge.note", Data: djJSON("note")}})
	if e == nil {
		t.Fatal("cross-type dedup accepted")
	}
	after := djHistory(t, ctx, root)
	if len(before) != len(after) {
		t.Fatal("dedup rejection wrote an entry")
	}
	other, e := hh.CreateConversation(ctx, h.CreateOptions{Ownership: durable.ConversationOwnership{Kind: "ownerless"}})
	djCheck(t, e)
	different := djSubmit(t, ctx, other, djInput("different conversation", "key"))
	if different.ID() == first.ID() {
		t.Fatal("requestId dedup escaped conversation scope")
	}
	djSettled(t, ctx, different, "unanswered")
	reacquired, e := hh.Submission(ctx, first.ID())
	djCheck(t, e)
	r, e := reacquired.Status(ctx)
	djCheck(t, e)
	if r.Status != "unanswered" || r.Reason != "no_model" {
		t.Fatalf("terminal receipt lost: %+v", r)
	}
}

func TestPortsmithJudgeDurableQueuePassiveWritesAndWait(t *testing.T) {
	ctx := djContext(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	models := &djModels{run: func(ctx context.Context, _ h.ModelRequest, _ func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ai.AssistantMessage{}, ctx.Err()
			}
		}
		return djAnswer("answer"), nil
	}}
	hh, root := djOpen(t, ctx, h.CreateRegistry(), models)
	djConfigure(t, ctx, root)
	first := djSubmit(t, ctx, root, djInput("first", "first"))
	djAwait(t, ctx, started)
	rejected := djInput("reject", "rejected")
	rejected.WhenBusy = "reject"
	if _, e := root.Submit(ctx, rejected); e == nil {
		t.Fatal("busy reject accepted")
	}
	inspection, e := hh.Inspect(ctx)
	djCheck(t, e)
	for _, s := range inspection.Submissions {
		if s.RequestID != nil && *s.RequestID == "rejected" {
			t.Fatal("busy rejection persisted submission")
		}
	}
	follow := djSubmit(t, ctx, root, djInput("follow", "follow"))
	write := djSubmit(t, ctx, root, h.SubmissionDraft{Type: "write", RequestID: "passive", Entry: &durable.EntryDraft{Kind: "judge.note", Data: djJSON("passive")}})
	record, e := write.Status(ctx)
	djCheck(t, e)
	if record.Status != "queued" {
		t.Fatalf("busy write=%+v", record)
	}
	waiter, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := follow.Wait(waiter); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancelled submission wait=%v", e)
	}
	record, e = follow.Status(ctx)
	djCheck(t, e)
	if record.Status != "queued" {
		t.Fatal("wait canceled admitted input")
	}
	close(release)
	djSettled(t, ctx, first, "done")
	djSettled(t, ctx, follow, "done")
	djSettled(t, ctx, write, "done")
	djCheck(t, root.WaitForIdle(ctx))
	if calls.Load() != 2 {
		t.Fatalf("passive write started generation: calls=%d", calls.Load())
	}
	history := djHistory(t, ctx, root)
	note, userFollow := -1, -1
	for i, e := range history {
		if e.Kind == "judge.note" {
			note = i
		}
		for _, m := range e.Model {
			if m.User != nil && m.User.Content.Text == "follow" {
				userFollow = i
			}
		}
	}
	if note < 0 || userFollow <= note {
		t.Fatalf("final boundary did not place passive write before next input: note=%d follow=%d", note, userFollow)
	}
}

func TestPortsmithJudgeDurableQueuedAbortPreservesWrites(t *testing.T) {
	ctx := djContext(t)
	started := make(chan struct{})
	models := &djModels{run: func(ctx context.Context, _ h.ModelRequest, _ func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		close(started)
		<-ctx.Done()
		return ai.AssistantMessage{}, ctx.Err()
	}}
	_, root := djOpen(t, ctx, h.CreateRegistry(), models)
	djConfigure(t, ctx, root)
	first := djSubmit(t, ctx, root, djInput("first", "first"))
	djAwait(t, ctx, started)
	queued := djSubmit(t, ctx, root, djInput("queued", "queued"))
	write := djSubmit(t, ctx, root, h.SubmissionDraft{Type: "write", Entry: &durable.EntryDraft{Kind: "judge.saved"}})
	result, e := first.Abort(ctx)
	djCheck(t, e)
	if result != "already_placed" {
		t.Fatalf("placed input abort=%s", result)
	}
	result, e = queued.Abort(ctx)
	djCheck(t, e)
	if result != "aborted" {
		t.Fatalf("queued input abort=%s", result)
	}
	r := djSettled(t, ctx, queued, "unanswered")
	if r.Reason != "aborted" {
		t.Fatalf("queued abort reason=%s", r.Reason)
	}
	djCheck(t, root.Abort(ctx, h.AbortOptions{}))
	r = djSettled(t, ctx, first, "unanswered")
	if r.Reason != "aborted" {
		t.Fatalf("conversation abort=%+v", r)
	}
	pending, e := write.Status(ctx)
	djCheck(t, e)
	if pending.Status == "unanswered" {
		t.Fatal("conversation abort withdrew passive write")
	}
}

func TestPortsmithJudgeDurableContextForkAndAgentCutoff(t *testing.T) {
	ctx := djContext(t)
	_, root := djOpen(t, ctx, h.CreateRegistry(), &djModels{})
	djCheck(t, root.Configure(ctx, json.RawMessage(`{"instructions":"old"}`)))
	var cutoff *durable.EntryRecord
	djCheck(t, root.Commit(ctx, func(tx durable.Tx) error {
		var e error
		cutoff, e = tx.AppendEntry(ctx, root.ID(), durable.EntryDraft{Kind: "pi.user", Model: []ai.Message{ai.NewUserMessageVariant(ai.NewUserMessage("ancestor", 1))}})
		return e
	}))
	djCheck(t, root.Configure(ctx, json.RawMessage(`{"instructions":"new"}`)))
	djSubmit(t, ctx, root, h.SubmissionDraft{Type: "write", Entry: &durable.EntryDraft{Kind: "judge.tail"}})
	child, e := root.Fork(ctx, cutoff.ID, h.CreateOptions{Ownership: durable.ConversationOwnership{Kind: "ownerless"}})
	djCheck(t, e)
	agent, e := child.Agent(ctx)
	djCheck(t, e)
	if agent.Instructions != "old" {
		t.Fatalf("fork copied latest agent instead of as-of: %+v", agent)
	}
	view, e := child.Context(ctx)
	djCheck(t, e)
	texts := djUserTexts(view.Messages)
	if len(texts) != 1 || texts[0] != "ancestor" {
		t.Fatalf("fork context=%v", texts)
	}
	for _, entry := range view.Entries {
		if entry.Kind == "judge.tail" {
			t.Fatal("fork inherited tail past cutoff")
		}
	}
	djCheck(t, child.Reset(ctx, "handoff"))
	view, e = child.Context(ctx)
	djCheck(t, e)
	if view.Head == nil || view.Head.Kind != "pi.reset" {
		t.Fatalf("reset head=%+v", view.Head)
	}
	texts = djUserTexts(view.Messages)
	if len(texts) != 1 || texts[0] != "handoff" {
		t.Fatalf("reset context=%v", texts)
	}
	if len(djHistory(t, ctx, child)) < 2 {
		t.Fatal("reset deleted durable history")
	}
}
