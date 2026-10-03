package chord

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/chord/delta"
)

type testAttachment struct {
	snapshot        StateSnapshot
	mu              sync.Mutex
	listener        func(StateFrame)
	buffered        []StateFrame
	activationError error
	disposed        atomic.Int32
}

func (a *testAttachment) Snapshot() StateSnapshot { return a.snapshot }

func (a *testAttachment) Activate(listener func(StateFrame)) error {
	if a.activationError != nil {
		return a.activationError
	}
	a.mu.Lock()
	a.listener = listener
	frames := a.buffered
	a.buffered = nil
	a.mu.Unlock()
	for _, frame := range frames {
		listener(frame)
	}
	return nil
}

func (a *testAttachment) Dispose() { a.disposed.Add(1) }

func (a *testAttachment) emit(frame StateFrame) {
	a.mu.Lock()
	listener := a.listener
	a.mu.Unlock()
	if listener != nil {
		listener(frame)
	}
}

type testSource struct {
	attachment *testAttachment
	err        error
}

func (s testSource) Attach() (StateAttachment, error) { return s.attachment, s.err }

type testDelivery struct {
	value    any
	context  context.Context
	delivery StateDelivery
}

func receiveDelivery[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not complete")
		var zero T
		return zero
	}
}

func TestAttachedStateOrderingAndAuthority(t *testing.T) {
	key := struct{ role string }{"invocation"}
	ctx := context.WithValue(context.Background(), key, "writer")
	attachment := &testAttachment{snapshot: StateSnapshot{Value: map[string]any{"text": "initial"}, Cursor: 40}, buffered: []StateFrame{
		{Value: map[string]any{"text": "buffered"}, Cursor: 41, Ops: []delta.Op{{"s", delta.Path{"text"}, "not-authority"}}, Context: ctx},
	}}
	state, err := AttachReplicatedState(testSource{attachment: attachment}, StateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(state.Dispose)
	chordJSONEqual(t, state.Value(), map[string]any{"text": "buffered"})
	seen := make(chan testDelivery, 5)
	stop, err := state.Subscribe(func(value any, deliveryCtx context.Context, delivery StateDelivery) error {
		seen <- testDelivery{value, deliveryCtx, delivery}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	hydrate := receiveDelivery(t, seen)
	if hydrate.delivery.Kind != "hydrate" || hydrate.delivery.Sequence != 1 {
		t.Fatalf("unexpected hydration: %+v", hydrate.delivery)
	}
	chordJSONEqual(t, hydrate.value, map[string]any{"text": "buffered"})
	attachment.emit(StateFrame{Value: map[string]any{"text": "frame-authority"}, Cursor: 42, Ops: []delta.Op{{"s", delta.Path{"text"}, "wrong"}}, Context: ctx})
	update := receiveDelivery(t, seen)
	if update.delivery.Kind != "update" || update.delivery.Sequence != 2 {
		t.Fatalf("unexpected update: %+v", update.delivery)
	}
	if update.context.Value(key) != "writer" {
		t.Fatal("update lost the invocation context")
	}
	chordJSONEqual(t, update.value, map[string]any{"text": "frame-authority"})
	stop()
	state.Dispose()
	state.Dispose()
	if attachment.disposed.Load() != 1 {
		t.Fatalf("attachment disposed %d times", attachment.disposed.Load())
	}
	attachment.emit(StateFrame{Value: map[string]any{"text": "after-disposal"}, Cursor: 43, Context: ctx})
	chordJSONEqual(t, state.Value(), map[string]any{"text": "frame-authority"})
}

func TestAttachedStateReportsSourceFailures(t *testing.T) {
	for _, name := range []string{"gap", "duplicate", "invalid_json"} {
		t.Run(name, func(t *testing.T) {
			attachment := &testAttachment{snapshot: StateSnapshot{Value: map[string]any{"v": 0}, Cursor: 7}}
			reported := make(chan error, 4)
			state, err := AttachReplicatedState(testSource{attachment: attachment}, StateOptions{OnError: func(err error) { reported <- err }})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(state.Dispose)
			frame := StateFrame{Value: map[string]any{"v": 1}, Cursor: 8, Context: context.Background()}
			switch name {
			case "gap":
				frame.Cursor = 9
			case "duplicate":
				frame.Cursor = 7
			case "invalid_json":
				frame.Value = map[string]any{"v": math.NaN()}
			}
			attachment.emit(frame)
			if err := receiveDelivery(t, reported); err == nil {
				t.Fatal("nil source failure")
			}
			chordJSONEqual(t, state.Value(), map[string]any{"v": 0})
			if attachment.disposed.Load() != 1 {
				t.Fatal("bad source not released")
			}
			attachment.emit(StateFrame{Value: map[string]any{"v": 2}, Cursor: 10, Context: context.Background()})
			chordJSONEqual(t, state.Value(), map[string]any{"v": 0})
		})
	}
	activationFailure := errors.New("activation rejected")
	attachment := &testAttachment{snapshot: StateSnapshot{Value: map[string]any{}, Cursor: 0}, activationError: activationFailure}
	if _, err := AttachReplicatedState(testSource{attachment: attachment}, StateOptions{}); !errors.Is(err, activationFailure) {
		t.Fatalf("activation error lost: %v", err)
	}
	if attachment.disposed.Load() != 1 {
		t.Fatal("failed activation leaked the source")
	}
	attachFailure := errors.New("attach rejected")
	if _, err := AttachReplicatedState(testSource{err: attachFailure}, StateOptions{}); !errors.Is(err, attachFailure) {
		t.Fatalf("attachment error lost: %v", err)
	}
}

func TestAttachedStateSlowSubscriberAndErrorIsolation(t *testing.T) {
	attachment := &testAttachment{snapshot: StateSnapshot{Value: map[string]any{"v": 0}, Cursor: 0}}
	reported := make(chan error, 10)
	state, err := AttachReplicatedState(testSource{attachment: attachment}, StateOptions{OnError: func(err error) { reported <- err }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(state.Dispose)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	slow := make(chan StateDelivery, 5)
	stopSlow, err := state.Subscribe(func(_ any, _ context.Context, delivery StateDelivery) error {
		if delivery.Kind == "hydrate" {
			close(entered)
			<-release
		}
		slow <- delivery
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopSlow)
	receiveDelivery(t, entered)
	fast := make(chan StateDelivery, 5)
	stopFast, err := state.Subscribe(func(_ any, _ context.Context, delivery StateDelivery) error {
		fast <- delivery
		if delivery.Kind == "update" && delivery.Sequence == 1 {
			return errors.New("observer failed")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopFast)
	receiveDelivery(t, fast)
	for cursor := uint64(1); cursor <= 2; cursor++ {
		attachment.emit(StateFrame{Value: map[string]any{"v": cursor}, Cursor: cursor, Context: context.Background()})
		if delivery := receiveDelivery(t, fast); delivery.Sequence != cursor {
			t.Fatalf("fast subscriber order: %+v", delivery)
		}
	}
	if err := receiveDelivery(t, reported); err == nil {
		t.Fatal("callback failure unreported")
	}
	releaseOnce.Do(func() { close(release) })
	for sequence := uint64(0); sequence <= 2; sequence++ {
		if delivery := receiveDelivery(t, slow); delivery.Sequence != sequence {
			t.Fatalf("slow subscriber order: %+v", delivery)
		}
	}
}

func TestAttachedStateSubscriberOverflowCoalesces(t *testing.T) {
	attachment := &testAttachment{snapshot: StateSnapshot{Value: map[string]any{"v": 0}, Cursor: 0}}
	state, err := AttachReplicatedState(testSource{attachment: attachment}, StateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(state.Dispose)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	seen := make(chan StateDelivery, 140)
	stop, err := state.Subscribe(func(_ any, _ context.Context, delivery StateDelivery) error {
		if delivery.Kind == "hydrate" {
			close(entered)
			<-release
		}
		seen <- delivery
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	receiveDelivery(t, entered)
	for cursor := uint64(1); cursor <= 120; cursor++ {
		attachment.emit(StateFrame{Value: map[string]any{"v": cursor}, Cursor: cursor, Context: context.Background()})
	}
	once.Do(func() { close(release) })
	if first := receiveDelivery(t, seen); first.Kind != "hydrate" || first.Sequence != 0 {
		t.Fatalf("hydration discarded: %+v", first)
	}
	for sequence := uint64(101); sequence <= 120; sequence++ {
		if delivery := receiveDelivery(t, seen); delivery.Kind != "update" || delivery.Sequence != sequence {
			t.Fatalf("overflow backlog at %d: %+v", sequence, delivery)
		}
	}
	chordJSONEqual(t, state.Value(), map[string]any{"v": 120})
}
