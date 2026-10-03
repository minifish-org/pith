package harness_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	ai "github.com/minifish-org/pith/packages/ai/types"
	chord "github.com/minifish-org/pith/packages/chord"
	h "github.com/minifish-org/pith/packages/durable/harness"
	sqlite "github.com/minifish-org/pith/packages/durable/storage/sqlite"
)

type djDeferredModels struct {
	djModels
	polls     atomic.Int32
	cancels   atomic.Int32
	firstPoll chan struct{}
	hold      bool
}

func (m *djDeferredModels) Poll(ctx context.Context, ref h.ModelRef, handle ai.DeferredHandle) (ai.AssistantMessage, error) {
	if ref.Provider != "fixture" || handle.Id != "pending-response" {
		return ai.AssistantMessage{}, context.Canceled
	}
	if m.polls.Add(1) == 1 && m.hold {
		close(m.firstPoll)
		<-ctx.Done()
		return ai.AssistantMessage{}, ctx.Err()
	}
	return djAnswer("polled"), nil
}
func (m *djDeferredModels) Cancel(context.Context, h.ModelRef, ai.DeferredHandle) error {
	m.cancels.Add(1)
	return nil
}
func djDeferredAnswer() ai.AssistantMessage {
	m := djAnswer("")
	m.StopReason = ai.StopReasonDeferred
	delay := int64(0)
	m.Deferred = &ai.DeferredHandle{Provider: "fixture", ModelId: "fixture", Api: "openai-completions", Id: "pending-response", PollAfterMs: &delay}
	return m
}

func TestPortsmithJudgeDurableDeferredReopenWithoutResend(t *testing.T) {
	ctx := djContext(t)
	path := t.TempDir() + "/state.sqlite"
	models := &djDeferredModels{firstPoll: make(chan struct{}), hold: true}
	models.run = func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		return djDeferredAnswer(), nil
	}
	reg := h.CreateRegistry()
	hh, root := djFileOpen(t, ctx, path, reg, models)
	djConfigure(t, ctx, root)
	s := djSubmit(t, ctx, root, djInput("defer", "defer"))
	id := s.ID()
	djAwait(t, ctx, models.firstPoll)
	djCheck(t, hh.Close(ctx))
	if models.cancels.Load() != 0 {
		t.Fatal("graceful close canceled durable provider handle")
	}
	hh, _ = djFileOpen(t, ctx, path, reg, models)
	defer hh.Close(context.Background())
	s, e := hh.Submission(ctx, id)
	djCheck(t, e)
	djSettled(t, ctx, s, "done")
	if len(models.Requests()) != 1 || models.polls.Load() != 2 {
		t.Fatalf("deferred recovery resent provider request: requests=%d polls=%d", len(models.Requests()), models.polls.Load())
	}
}

func TestPortsmithJudgeDurableDeferredAbortCancelsProvider(t *testing.T) {
	ctx := djContext(t)
	models := &djDeferredModels{firstPoll: make(chan struct{}), hold: true}
	models.run = func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		return djDeferredAnswer(), nil
	}
	_, root := djOpen(t, ctx, h.CreateRegistry(), models)
	djConfigure(t, ctx, root)
	s := djSubmit(t, ctx, root, djInput("defer", "defer"))
	djAwait(t, ctx, models.firstPoll)
	djCheck(t, root.Abort(ctx, h.AbortOptions{}))
	r := djSettled(t, ctx, s, "unanswered")
	if r.Reason != "aborted" || models.cancels.Load() != 1 {
		t.Fatalf("deferred abort receipt=%+v cancellations=%d", r, models.cancels.Load())
	}
}

func TestPortsmithJudgeDurableCommittedGenerationPartialRecovery(t *testing.T) {
	ctx := djContext(t)
	path := t.TempDir() + "/state.sqlite"
	var calls atomic.Int32
	committed := make(chan struct{})
	var once sync.Once
	models := &djModels{run: func(ctx context.Context, _ h.ModelRequest, emit func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if calls.Add(1) == 1 {
			if e := emit(djAnswer("committed partial")); e != nil {
				return ai.AssistantMessage{}, e
			}
			<-ctx.Done()
			return ai.AssistantMessage{}, ctx.Err()
		}
		return djAnswer("complete"), nil
	}}
	reg := h.CreateRegistry()
	hh, root := djFileOpen(t, ctx, path, reg, models)
	djConfigure(t, ctx, root)
	watch, e := root.Watch(ctx)
	djCheck(t, e)
	djCheck(t, watch.Start(func(_ context.Context, v h.ConversationView, _ []chord.Op) error {
		encoded, _ := json.Marshal(v.Docs["pi.live"])
		if bytesContains(encoded, []byte("committed partial")) {
			once.Do(func() { close(committed) })
		}
		return nil
	}))
	s := djSubmit(t, ctx, root, djInput("partial", "partial"))
	id := s.ID()
	djAwait(t, ctx, committed)
	djCheck(t, hh.Close(ctx))
	djCheck(t, watch.Stop())
	hh, root = djFileOpen(t, ctx, path, reg, models)
	defer hh.Close(context.Background())
	s, e = hh.Submission(ctx, id)
	djCheck(t, e)
	djSettled(t, ctx, s, "done")
	requests := models.Requests()
	if len(requests) != 2 || !reflect.DeepEqual(requests[0].Messages, requests[1].Messages) {
		t.Fatalf("recovery repeated preparation or changed request: request count=%d", len(requests))
	}
	seenAborted := false
	for _, entry := range djHistory(t, ctx, root) {
		for _, m := range entry.Model {
			if m.Assistant != nil && m.Assistant.StopReason == ai.StopReasonAborted {
				seenAborted = true
			}
		}
	}
	if !seenAborted {
		t.Fatal("committed partial not retained as aborted raw transcript entry")
	}
	view, e := root.Context(ctx)
	djCheck(t, e)
	for _, m := range view.Messages {
		if m.Assistant != nil && m.Assistant.StopReason == ai.StopReasonAborted {
			t.Fatal("aborted partial entered future model context")
		}
	}
}
func bytesContains(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}

func TestPortsmithJudgeDurableRetryDeadlineReopen(t *testing.T) {
	ctx := djContext(t)
	path := t.TempDir() + "/retry.sqlite"
	reg := h.CreateRegistry()
	var requests atomic.Int32
	models := &djModels{run: func(context.Context, h.ModelRequest, func(ai.AssistantMessage) error) (ai.AssistantMessage, error) {
		if requests.Add(1) == 1 {
			message := djAnswer("")
			message.StopReason = ai.StopReasonError
			reason := "socket hang up"
			message.ErrorMessage = &reason
			return message, nil
		}
		return djAnswer("after retry"), nil
	}}
	open := func(now int64) (*h.Harness, *h.Conversation) {
		store, e := sqlite.Open(ctx, path, sqlite.Options{})
		djCheck(t, e)
		hh, e := h.Open(ctx, store, h.Options{Registry: reg, Models: models, Now: func() int64 { return now }})
		djCheck(t, e)
		root, e := hh.Root(ctx, h.RootOptions{})
		djCheck(t, e)
		return hh, root
	}
	hh, root := open(1000)
	djConfigure(t, ctx, root)
	retryCommitted := make(chan struct{})
	var once sync.Once
	watch, e := root.Watch(ctx)
	djCheck(t, e)
	djCheck(t, watch.Start(func(_ context.Context, value h.ConversationView, _ []chord.Op) error {
		encoded, _ := json.Marshal(value.Docs["pi.live"])
		if bytesContains(encoded, []byte(`"retry"`)) {
			once.Do(func() { close(retryCommitted) })
		}
		return nil
	}))
	submission := djSubmit(t, ctx, root, djInput("retry", "retry"))
	id := submission.ID()
	djAwait(t, ctx, retryCommitted)
	djCheck(t, hh.Close(ctx))
	if requests.Load() != 1 {
		t.Fatal("constant clock did not keep durable backoff pending")
	}
	hh, _ = open(4000)
	defer hh.Close(context.Background())
	submission, e = hh.Submission(ctx, id)
	djCheck(t, e)
	djSettled(t, ctx, submission, "done")
	if requests.Load() != 2 {
		t.Fatalf("stored deadline did not resume one retry: requests=%d", requests.Load())
	}
}
