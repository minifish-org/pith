package harnesevents

import (
	"context"
	"errors"
	"testing"
)

func TestHarnessEventBusIsolatesHandlerFailures(t *testing.T) {
	bus := NewHarnessEventBus()
	seen := []string{}
	bus.On("run_start", func(event HarnessEvent, ctx Context) error {
		seen = append(seen, "first")
		return nil
	})
	bus.On("run_start", func(event HarnessEvent, ctx Context) error {
		return errors.New("boom")
	})
	bus.On("handler_error", func(event HarnessEvent, ctx Context) error {
		seen = append(seen, "error-reported")
		return nil
	})
	if err := bus.Emit(context.Background(), HarnessEvent{Type: "run_start", Payload: []byte(`{"n":1}`)}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != "first" || seen[1] != "error-reported" {
		t.Fatalf("unexpected delivery: %#v", seen)
	}
}

func TestHarnessEventBusRecipientsSnapshot(t *testing.T) {
	bus := NewHarnessEventBus()
	seen := 0
	bus.On("x", func(event HarnessEvent, ctx Context) error {
		seen++
		bus.On("x", func(event HarnessEvent, ctx Context) error { seen++; return nil })
		return nil
	})
	if err := bus.Emit(context.Background(), HarnessEvent{Type: "x"}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("listener registered during delivery must not observe the in-flight event: %d", seen)
	}
}

func TestWatchHandleBuffersUntilStart(t *testing.T) {
	bus := NewHarnessEventBus()
	watcher, err := Watch[int](bus, 0, nil, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	watcher.Push(HarnessEvent{Type: "a"}, context.Background())
	watcher.Push(HarnessEvent{Type: "b"}, context.Background())

	seen := []string{}
	if err := watcher.Start(func(event HarnessEvent, ctx Context) error {
		seen = append(seen, event.Type)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("buffered events not delivered: %#v", seen)
	}
	watcher.Push(HarnessEvent{Type: "c"}, context.Background())
	if len(seen) != 3 {
		t.Fatalf("live event not delivered: %#v", seen)
	}
	watcher.Unsubscribe()
	watcher.Push(HarnessEvent{Type: "d"}, context.Background())
	if len(seen) != 3 {
		t.Fatalf("event delivered after unsubscribe: %#v", seen)
	}
}
