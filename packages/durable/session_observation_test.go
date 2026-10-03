package durable_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/chord"
	d "github.com/minifish-org/pith/packages/durable"
)

func TestWatchAbsentDocumentReturnsNil(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("watch", d.HistoryRewindable, d.ForkAsOf)
	watch, err := session.WatchDoc(ctx, definition, conversationAddress("watch", d.RootConversationID))
	if err != nil || watch != nil {
		t.Fatalf("watch created a document: %+v %v", watch, err)
	}
	state, err := session.DocumentState(ctx, definition, conversationAddress("watch", d.RootConversationID))
	if err != nil || state != nil {
		t.Fatalf("document state created a document: %+v %v", state, err)
	}
}

type watchFrameResult struct {
	value d.JsonObject
	ops   []chord.Op
}

func TestWatchDeliversExactCommittedFrames(t *testing.T) {
	session, _ := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	definition := counterDefinition("watch", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("watch", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	watch, err := session.WatchDoc(ctx, definition, address)
	if err != nil || watch == nil {
		t.Fatalf("watch %+v %v", watch, err)
	}
	defer watch.Stop()
	frames := make(chan watchFrameResult, 4)
	if err := watch.Start(func(_ context.Context, value d.JsonObject, ops []chord.Op) error {
		frames <- watchFrameResult{value: value, ops: ops}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 1) }); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		if countOf(t, frame.value) != 1 {
			t.Fatalf("watch frame value %+v", frame.value)
		}
		if len(frame.ops) == 0 {
			t.Fatal("watch frame carried no operations")
		}
	case <-ctx.Done():
		t.Fatal("watch did not deliver a committed frame")
	}
}

func TestWatchDeliversRetirementAndCloses(t *testing.T) {
	session, _ := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	definition := counterDefinition("retire", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("retire", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	watch, err := session.WatchDoc(ctx, definition, address)
	if err != nil || watch == nil {
		t.Fatal(err)
	}
	retired := make(chan d.JsonObject, 1)
	if err := watch.Start(func(_ context.Context, value d.JsonObject, _ []chord.Op) error {
		if value == nil {
			retired <- nil
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.Commit(ctx, func(tx d.Tx) error { return tx.RetireDoc(ctx, definition, address) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-retired:
	case <-ctx.Done():
		t.Fatal("watch did not deliver retirement")
	}
	select {
	case end := <-watch.Closed():
		if end.Reason != "retired" {
			t.Fatalf("watch end %+v", end)
		}
	case <-ctx.Done():
		t.Fatal("watch did not close after retirement")
	}
}

func TestWatchListenerErrorClosesOnlyThatWatch(t *testing.T) {
	session, _ := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	definition := counterDefinition("listener", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("listener", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	failing, err := session.WatchDoc(ctx, definition, address)
	if err != nil || failing == nil {
		t.Fatal(err)
	}
	if err := failing.Start(func(context.Context, d.JsonObject, []chord.Op) error { return errors.New("boom") }); err != nil {
		t.Fatal(err)
	}
	healthy, err := session.WatchDoc(ctx, definition, address)
	if err != nil || healthy == nil {
		t.Fatal(err)
	}
	defer healthy.Stop()
	delivered := make(chan struct{}, 1)
	if err := healthy.Start(func(context.Context, d.JsonObject, []chord.Op) error {
		select {
		case delivered <- struct{}{}:
		default:
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 1) }); err != nil {
		t.Fatal(err)
	}
	select {
	case end := <-failing.Closed():
		if end.Reason != "listener_error" || end.Err == nil {
			t.Fatalf("listener failure end %+v", end)
		}
	case <-ctx.Done():
		t.Fatal("failing watch did not close")
	}
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("healthy watch stopped receiving after an isolated listener failure")
	}
}

func TestWatchStopIdempotentAndLateStartRejected(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("stop", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("stop", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	watch, err := session.WatchDoc(ctx, definition, address)
	if err != nil || watch == nil {
		t.Fatal(err)
	}
	if err := watch.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := watch.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := watch.Start(func(context.Context, d.JsonObject, []chord.Op) error { return nil }); err == nil {
		t.Fatal("start after stop was accepted")
	}
}

func TestWatchOverflowCoalescesToRootReset(t *testing.T) {
	session, _ := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	definition := counterDefinition("overflow", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("overflow", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	watch, err := session.WatchDoc(ctx, definition, address)
	if err != nil || watch == nil {
		t.Fatal(err)
	}
	defer watch.Stop()
	// Commit enough revisions while the listener is unavailable to overflow the
	// bounded pending queue.
	for i := 1; i <= 101; i++ {
		count := i
		if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, count) }); err != nil {
			t.Fatal(err)
		}
	}
	frames := make(chan watchFrameResult, 4)
	if err := watch.Start(func(_ context.Context, value d.JsonObject, ops []chord.Op) error {
		frames <- watchFrameResult{value: value, ops: ops}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case frame := <-frames:
		if countOf(t, frame.value) != 101 {
			t.Fatalf("overflow reset did not carry the newest value: %+v", frame.value)
		}
		if len(frame.ops) != 1 || frame.ops[0][0] != "r" {
			t.Fatalf("overflow reset was not a root replacement: %+v", frame.ops)
		}
	case <-ctx.Done():
		t.Fatal("overflow reset was not delivered")
	}
}

func TestWatchContextCancellationEndsDelivery(t *testing.T) {
	session, _ := newTestSession(t)
	definition := counterDefinition("cancel", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("cancel", d.RootConversationID)
	if err := session.Commit(context.Background(), func(tx d.Tx) error { return setCount(context.Background(), tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	watch, err := session.WatchDoc(ctx, definition, address)
	if err != nil || watch == nil {
		t.Fatal(err)
	}
	if err := watch.Start(func(context.Context, d.JsonObject, []chord.Op) error { return nil }); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case end := <-watch.Closed():
		if end.Reason != "cancelled" {
			t.Fatalf("watch end %+v", end)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("watch did not observe acquisition cancellation")
	}
}

func TestDocumentStateHydratesAndUpdates(t *testing.T) {
	session, _ := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	definition := counterDefinition("state", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("state", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	state, err := session.DocumentState(ctx, definition, address)
	if err != nil || state == nil {
		t.Fatalf("document state %+v %v", state, err)
	}
	defer state.Dispose()
	type delivery struct {
		kind     string
		sequence uint64
		value    any
	}
	deliveries := make(chan delivery, 4)
	unsubscribe, err := state.Subscribe(func(value any, _ context.Context, info chord.StateDelivery) error {
		deliveries <- delivery{kind: info.Kind, sequence: info.Sequence, value: value}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	select {
	case first := <-deliveries:
		if first.kind != "hydrate" || first.sequence != 0 {
			t.Fatalf("first delivery %+v", first)
		}
	case <-ctx.Done():
		t.Fatal("document state did not hydrate")
	}
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 1) }); err != nil {
		t.Fatal(err)
	}
	select {
	case update := <-deliveries:
		if update.kind != "update" || update.sequence != 1 {
			t.Fatalf("update delivery %+v", update)
		}
		value, ok := update.value.(map[string]any)
		if !ok {
			t.Fatalf("update value type %T", update.value)
		}
		if countOf(t, d.JsonObject(value)) != 1 {
			t.Fatalf("update value %+v", update.value)
		}
	case <-ctx.Done():
		t.Fatal("document state did not deliver an update")
	}
}

func TestDocumentStateDeliversRetirement(t *testing.T) {
	session, _ := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	definition := counterDefinition("state-retire", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("state-retire", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	state, err := session.DocumentState(ctx, definition, address)
	if err != nil || state == nil {
		t.Fatal(err)
	}
	defer state.Dispose()
	retired := make(chan any, 1)
	unsubscribe, err := state.Subscribe(func(value any, _ context.Context, _ chord.StateDelivery) error {
		if value == nil {
			retired <- nil
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if err := session.Commit(ctx, func(tx d.Tx) error { return tx.RetireDoc(ctx, definition, address) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-retired:
	case <-ctx.Done():
		t.Fatal("document state did not deliver retirement")
	}
}

func TestWatchDoesNotBlockSessionCommit(t *testing.T) {
	session, _ := newTestSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	definition := counterDefinition("slow", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("slow", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	watch, err := session.WatchDoc(ctx, definition, address)
	if err != nil || watch == nil {
		t.Fatal(err)
	}
	entered := make(chan d.JsonObject, 1)
	release := make(chan struct{})
	defer close(release)
	if err := watch.Start(func(delivery context.Context, value d.JsonObject, ops []chord.Op) error {
		if len(ops) == 0 {
			return errors.New("empty changed frame")
		}
		entered <- value
		select {
		case <-release:
			return nil
		case <-delivery.Done():
			return delivery.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 1) })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("a blocked watch callback stopped a Session commit")
	}
	select {
	case value := <-entered:
		if countOf(t, value) != 1 {
			t.Fatalf("watch frame %+v", value)
		}
	case <-ctx.Done():
		t.Fatal("watch lost the post-acquisition commit")
	}
	if err := watch.Stop(); err != nil {
		t.Fatal(err)
	}
}
