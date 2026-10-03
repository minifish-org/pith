package chord_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/chord/delta"
)

func durableChordEqual(t *testing.T, got, want any) {
	t.Helper()
	g, e := json.Marshal(got)
	if e != nil {
		t.Fatal(e)
	}
	w, e := json.Marshal(want)
	if e != nil {
		t.Fatal(e)
	}
	if string(g) != string(w) {
		t.Fatalf("JSON mismatch: got %s, want %s", g, w)
	}
}

func TestPortsmithJudgeDurableChordStrictJSONOwnership(t *testing.T) {
	child := map[string]any{"text": "你好😀", "value": 1}
	input := map[string]any{"left": child, "right": child, "array": []any{nil, true, 2}}
	copy, e := chord.CopyJSON(input)
	if e != nil {
		t.Fatal(e)
	}
	object := copy.(map[string]any)
	object["left"].(map[string]any)["value"] = 9
	durableChordEqual(t, child, map[string]any{"text": "你好😀", "value": 1})
	durableChordEqual(t, object["right"], map[string]any{"text": "你好😀", "value": 1})
	if !chord.IsJSONValue(input) {
		t.Fatal("strict JSON rejected")
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, invalid := range []any{math.NaN(), math.Inf(1), math.Inf(-1), func() {}, make(chan int), cycle, map[string]any{"bad": math.NaN()}} {
		if chord.IsJSONValue(invalid) {
			t.Fatalf("non-JSON value accepted: %T", invalid)
		}
		if _, e := chord.CopyJSON(invalid); e == nil {
			t.Fatalf("non-JSON copy accepted: %T", invalid)
		}
	}
}

// This is a producer-owned attachment fixture, not a candidate implementation.
// It supplies authoritative values/cursors so the judge can verify publication.
type durableChordAttachment struct {
	snapshot        chord.StateSnapshot
	mu              sync.Mutex
	listener        func(chord.StateFrame)
	buffered        []chord.StateFrame
	activationError error
	disposed        atomic.Int32
}

func (a *durableChordAttachment) Snapshot() chord.StateSnapshot { return a.snapshot }
func (a *durableChordAttachment) Activate(listener func(chord.StateFrame)) error {
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
func (a *durableChordAttachment) Dispose() { a.disposed.Add(1) }
func (a *durableChordAttachment) emit(frame chord.StateFrame) {
	a.mu.Lock()
	f := a.listener
	a.mu.Unlock()
	if f != nil {
		f(frame)
	}
}

type durableChordSource struct {
	attachment *durableChordAttachment
	err        error
}

func (s durableChordSource) Attach() (chord.StateAttachment, error) { return s.attachment, s.err }

type durableChordDelivery struct {
	value    any
	context  context.Context
	delivery chord.StateDelivery
}

func durableChordReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("publication did not complete")
		var zero T
		return zero
	}
}

func TestPortsmithJudgeDurableChordAttachmentOrderingAndAuthority(t *testing.T) {
	key := struct{ role string }{"invocation"}
	ctx := context.WithValue(context.Background(), key, "writer")
	a := &durableChordAttachment{snapshot: chord.StateSnapshot{Value: map[string]any{"text": "initial"}, Cursor: 40}, buffered: []chord.StateFrame{
		{Value: map[string]any{"text": "buffered"}, Cursor: 41, Ops: []delta.Op{{"s", delta.Path{"text"}, "not-authority"}}, Context: ctx},
	}}
	errorsSeen := make(chan error, 4)
	state, e := chord.AttachReplicatedState(durableChordSource{attachment: a}, chord.StateOptions{OnError: func(e error) { errorsSeen <- e }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(state.Dispose)
	durableChordEqual(t, state.Value(), map[string]any{"text": "buffered"})
	seen := make(chan durableChordDelivery, 5)
	stop, e := state.Subscribe(func(v any, c context.Context, d chord.StateDelivery) error {
		seen <- durableChordDelivery{v, c, d}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(stop)
	hydrate := durableChordReceive(t, seen)
	if hydrate.delivery.Kind != "hydrate" || hydrate.delivery.Sequence != 1 {
		t.Fatalf("hydration at current publication sequence: %+v", hydrate.delivery)
	}
	durableChordEqual(t, hydrate.value, map[string]any{"text": "buffered"})
	a.emit(chord.StateFrame{Value: map[string]any{"text": "frame-authority"}, Cursor: 42, Ops: []delta.Op{{"s", delta.Path{"text"}, "wrong-if-reapplied"}}, Context: ctx})
	update := durableChordReceive(t, seen)
	if update.delivery.Kind != "update" || update.delivery.Sequence != 2 {
		t.Fatalf("buffered source publication then update sequence: %+v", update.delivery)
	}
	if update.context.Value(key) != "writer" {
		t.Fatal("update lost invocation context")
	}
	durableChordEqual(t, update.value, map[string]any{"text": "frame-authority"})
	durableChordEqual(t, hydrate.value, map[string]any{"text": "buffered"})
	stop()
	state.Dispose()
	state.Dispose()
	if a.disposed.Load() != 1 {
		t.Fatalf("attachment disposed %d times", a.disposed.Load())
	}
	a.emit(chord.StateFrame{Value: map[string]any{"text": "after-disposal"}, Cursor: 43, Context: ctx})
	durableChordEqual(t, state.Value(), map[string]any{"text": "frame-authority"})
	select {
	case e := <-errorsSeen:
		t.Fatalf("valid source reported failure: %v", e)
	default:
	}
}

func TestPortsmithJudgeDurableChordSourceFailures(t *testing.T) {
	for _, name := range []string{"gap", "duplicate", "invalid_json"} {
		t.Run(name, func(t *testing.T) {
			a := &durableChordAttachment{snapshot: chord.StateSnapshot{Value: map[string]any{"v": 0}, Cursor: 7}}
			reported := make(chan error, 4)
			state, e := chord.AttachReplicatedState(durableChordSource{attachment: a}, chord.StateOptions{OnError: func(e error) { reported <- e }})
			if e != nil {
				t.Fatal(e)
			}
			t.Cleanup(state.Dispose)
			frame := chord.StateFrame{Value: map[string]any{"v": 1}, Cursor: 8, Context: context.Background()}
			if name == "gap" {
				frame.Cursor = 9
			}
			if name == "duplicate" {
				frame.Cursor = 7
			}
			if name == "invalid_json" {
				frame.Value = map[string]any{"v": math.NaN()}
			}
			a.emit(frame)
			if e := durableChordReceive(t, reported); e == nil {
				t.Fatal("nil source failure")
			}
			durableChordEqual(t, state.Value(), map[string]any{"v": 0})
			if a.disposed.Load() != 1 {
				t.Fatal("bad source not released")
			}
			a.emit(chord.StateFrame{Value: map[string]any{"v": 2}, Cursor: 10, Context: context.Background()})
			durableChordEqual(t, state.Value(), map[string]any{"v": 0})
		})
	}
	activationFailure := errors.New("activation rejected")
	a := &durableChordAttachment{snapshot: chord.StateSnapshot{Value: map[string]any{}, Cursor: 0}, activationError: activationFailure}
	if _, e := chord.AttachReplicatedState(durableChordSource{attachment: a}, chord.StateOptions{}); !errors.Is(e, activationFailure) {
		t.Fatalf("activation error lost: %v", e)
	}
	if a.disposed.Load() != 1 {
		t.Fatal("failed activation leaked source")
	}
	attachFailure := errors.New("attach rejected")
	if _, e := chord.AttachReplicatedState(durableChordSource{err: attachFailure}, chord.StateOptions{}); !errors.Is(e, attachFailure) {
		t.Fatalf("attachment error lost: %v", e)
	}
}

func TestPortsmithJudgeDurableChordSlowSubscribersAndErrorIsolation(t *testing.T) {
	a := &durableChordAttachment{snapshot: chord.StateSnapshot{Value: map[string]any{"v": 0}, Cursor: 0}}
	reported := make(chan error, 10)
	state, e := chord.AttachReplicatedState(durableChordSource{attachment: a}, chord.StateOptions{OnError: func(e error) { reported <- e }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(state.Dispose)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	slow := make(chan durableChordDelivery, 5)
	stopSlow, e := state.Subscribe(func(v any, c context.Context, d chord.StateDelivery) error {
		if d.Kind == "hydrate" {
			close(entered)
			<-release
		}
		slow <- durableChordDelivery{v, c, d}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(stopSlow)
	durableChordReceive(t, entered)
	fast := make(chan durableChordDelivery, 5)
	stopFast, e := state.Subscribe(func(v any, c context.Context, d chord.StateDelivery) error {
		fast <- durableChordDelivery{v, c, d}
		if d.Kind == "update" && d.Sequence == 1 {
			return errors.New("observer failed")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(stopFast)
	durableChordReceive(t, fast)
	for cursor := uint64(1); cursor <= 2; cursor++ {
		producerDone := make(chan struct{})
		go func(cursor uint64) {
			a.emit(chord.StateFrame{Value: map[string]any{"v": cursor}, Cursor: cursor, Ops: []delta.Op{{"s", delta.Path{"v"}, cursor}}, Context: context.Background()})
			close(producerDone)
		}(cursor)
		durableChordReceive(t, producerDone)
		delivery := durableChordReceive(t, fast)
		if delivery.delivery.Sequence != cursor {
			t.Fatalf("fast subscriber order: %+v", delivery.delivery)
		}
	}
	if e := durableChordReceive(t, reported); e == nil {
		t.Fatal("callback failure unreported")
	}
	releaseOnce.Do(func() { close(release) })
	for seq := uint64(0); seq <= 2; seq++ {
		d := durableChordReceive(t, slow)
		if d.delivery.Sequence != seq {
			t.Fatalf("slow subscriber order: %+v", d.delivery)
		}
	}
}

func TestPortsmithJudgeDurableChordSubscriberOverflow(t *testing.T) {
	a := &durableChordAttachment{snapshot: chord.StateSnapshot{Value: map[string]any{"v": 0}, Cursor: 0}}
	state, e := chord.AttachReplicatedState(durableChordSource{attachment: a}, chord.StateOptions{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(state.Dispose)
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	seen := make(chan chord.StateDelivery, 140)
	stop, e := state.Subscribe(func(_ any, _ context.Context, d chord.StateDelivery) error {
		if d.Kind == "hydrate" {
			close(entered)
			<-release
		}
		seen <- d
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(stop)
	durableChordReceive(t, entered)
	for cursor := uint64(1); cursor <= 120; cursor++ {
		a.emit(chord.StateFrame{Value: map[string]any{"v": cursor}, Cursor: cursor, Context: context.Background()})
	}
	once.Do(func() { close(release) })
	if first := durableChordReceive(t, seen); first.Kind != "hydrate" || first.Sequence != 0 {
		t.Fatalf("hydration discarded: %+v", first)
	}
	for seq := uint64(101); seq <= 120; seq++ {
		if d := durableChordReceive(t, seen); d.Kind != "update" || d.Sequence != seq {
			t.Fatalf("overflow backlog at %d: %+v", seq, d)
		}
	}
	durableChordEqual(t, state.Value(), map[string]any{"v": 120})
}
