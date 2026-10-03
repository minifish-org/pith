package harness_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	h "github.com/minifish-org/pith/packages/durable/harness"
	sqlite "github.com/minifish-org/pith/packages/durable/storage/sqlite"
)

type recoveredModels struct {
	testModels
	polls   atomic.Int32
	cancels atomic.Int32
	first   chan struct{}
	hold    bool
}

func (m *recoveredModels) Poll(ctx context.Context, ref h.ModelRef, handle ai.DeferredHandle) (ai.AssistantMessage, error) {
	if m.polls.Add(1) == 1 && m.hold {
		close(m.first)
		<-ctx.Done()
		return ai.AssistantMessage{}, ctx.Err()
	}
	return testAnswer("polled"), nil
}

func (m *recoveredModels) Cancel(context.Context, h.ModelRef, ai.DeferredHandle) error {
	m.cancels.Add(1)
	return nil
}

func deferredAnswer() ai.AssistantMessage {
	message := testAnswer("")
	message.StopReason = ai.StopReasonDeferred
	delay := int64(0)
	message.Deferred = &ai.DeferredHandle{Provider: "fixture", ModelId: "fixture", Api: "openai-completions", Id: "pending-response", PollAfterMs: &delay}
	return message
}

func TestGenerationDeferredRecoveryWithoutResend(t *testing.T) {
	ctx := testContext(t)
	path := t.TempDir() + "/state.sqlite"
	models := &recoveredModels{first: make(chan struct{}), hold: true}
	models.run = func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		return deferredAnswer(), nil
	}
	registry := h.CreateRegistry()
	open := func() (*h.Harness, *h.Conversation) {
		store, err := sqlite.Open(ctx, path, sqlite.Options{})
		if err != nil {
			t.Fatal(err)
		}
		harness, err := h.Open(ctx, store, h.Options{Registry: registry, Models: models})
		if err != nil {
			t.Fatal(err)
		}
		root, err := harness.Root(ctx, h.RootOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return harness, root
	}
	harness, root := open()
	if err := root.Configure(ctx, json.RawMessage(`{"model":{"provider":"fixture","modelId":"fixture"}}`)); err != nil {
		t.Fatal(err)
	}
	submission, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("defer"), RequestID: "defer"})
	if err != nil {
		t.Fatal(err)
	}
	<-models.first
	if err := harness.Close(ctx); err != nil {
		t.Fatal(err)
	}
	harness, _ = open()
	defer harness.Close(context.Background())
	reacquired, err := harness.Submission(ctx, submission.ID())
	if err != nil {
		t.Fatal(err)
	}
	record, err := reacquired.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "done" {
		t.Fatalf("record=%+v", record)
	}
	if models.polls.Load() != 2 {
		t.Fatalf("polls=%d", models.polls.Load())
	}
}

func TestGenerationRetryDeadline(t *testing.T) {
	ctx := testContext(t)
	registry := h.CreateRegistry()
	var requests atomic.Int32
	models := &testModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if requests.Add(1) == 1 {
			message := testAnswer("")
			message.StopReason = ai.StopReasonError
			reason := "socket hang up"
			message.ErrorMessage = &reason
			return message, nil
		}
		return testAnswer("after retry"), nil
	}}
	harness, root := testOpen(t, ctx, registry, models)
	if err := root.Configure(ctx, json.RawMessage(`{"model":{"provider":"fixture","modelId":"fixture"}}`)); err != nil {
		t.Fatal(err)
	}
	submission, err := root.Submit(ctx, h.SubmissionDraft{Type: "input", Content: ai.UserContentText("retry"), RequestID: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := submission.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "done" || requests.Load() != 2 {
		t.Fatalf("record=%+v requests=%d", record, requests.Load())
	}
	_ = harness
}
