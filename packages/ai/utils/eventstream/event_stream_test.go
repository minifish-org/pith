package eventstream

import (
	"context"
	"sync"
	"testing"
	"time"
)

func mustNext(t *testing.T, ch <-chan StreamItem[int]) StreamItem[int] {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(time.Second):
		t.Fatal("Next blocked")
		return StreamItem[int]{}
	}
}

func plainStream() *EventStream[int, int] {
	return NewEventStream(func(int) bool { return false }, func(n int) int { return n })
}

// Mirrors "drains buffered events in order and ignores events pushed after
// completion".
func TestEventStreamDrainsBufferedAndIgnoresAfterCompletion(t *testing.T) {
	s := NewEventStream(func(n int) bool { return n == 3 }, func(n int) int { return n })
	s.Push(1)
	s.Push(2)
	s.Push(3)
	s.Push(4)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got, err := s.Result(ctx); err != nil || got != 3 {
		t.Fatalf("Result = %v, %v; want 3, nil", got, err)
	}

	var events []int
	for {
		item := mustNext(t, s.Next())
		if item.Done {
			break
		}
		events = append(events, item.Value)
	}
	if len(events) != 3 || events[0] != 1 || events[1] != 2 || events[2] != 3 {
		t.Fatalf("events = %v; want [1 2 3]", events)
	}
}

// Mirrors "preserves order when events arrive after buffered draining starts".
func TestEventStreamPreservesOrderDuringDraining(t *testing.T) {
	s := plainStream()
	s.Push(1)
	s.Push(2)

	if got := mustNext(t, s.Next()); got.Done || got.Value != 1 {
		t.Fatalf("first = %+v; want 1", got)
	}

	s.Push(3)
	if got := mustNext(t, s.Next()); got.Done || got.Value != 2 {
		t.Fatalf("second = %+v; want 2", got)
	}
	if got := mustNext(t, s.Next()); got.Done || got.Value != 3 {
		t.Fatalf("third = %+v; want 3", got)
	}

	s.End(nil)
	if !mustNext(t, s.Next()).Done {
		t.Fatal("expected terminal item")
	}
}

// Mirrors "delivers events to waiting consumers in registration order".
func TestEventStreamDeliversToWaitersInOrder(t *testing.T) {
	s := plainStream()
	first := s.Next()
	second := s.Next()

	s.Push(1)
	s.Push(2)

	if got := mustNext(t, first); got.Done || got.Value != 1 {
		t.Fatalf("first = %+v; want 1", got)
	}
	if got := mustNext(t, second); got.Done || got.Value != 2 {
		t.Fatalf("second = %+v; want 2", got)
	}
}

// Mirrors "drains buffered events after end and resolves the explicit result".
func TestEventStreamEndDrainsAndResolvesResult(t *testing.T) {
	s := NewEventStream(func(int) bool { return false }, func(n int) string { return "unused" })
	s.Push(1)
	s.Push(2)
	result := "complete"
	s.End(&result)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if got, err := s.Result(ctx); err != nil || got != "complete" {
		t.Fatalf("Result = %v, %v; want complete, nil", got, err)
	}

	var events []int
	for {
		item := mustNext(t, s.Next())
		if item.Done {
			break
		}
		events = append(events, item.Value)
	}
	if len(events) != 2 || events[0] != 1 || events[1] != 2 {
		t.Fatalf("events = %v; want [1 2]", events)
	}
}

// Mirrors "wakes all waiting consumers when ended without a result".
func TestEventStreamEndWakesAllWaiters(t *testing.T) {
	s := plainStream()
	first := s.Next()
	second := s.Next()

	s.End(nil)

	if !mustNext(t, first).Done {
		t.Fatal("first waiter not released")
	}
	if !mustNext(t, second).Done {
		t.Fatal("second waiter not released")
	}
}

func TestEventStreamResultCancellation(t *testing.T) {
	s := plainStream()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.Result(ctx); err == nil {
		t.Fatal("expected cancellation error for absent result")
	}

	z := 0
	s.End(&z)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if got, err := s.Result(ctx2); err != nil || got != 0 {
		t.Fatalf("Result = %v, %v; want 0, nil", got, err)
	}
}

func TestEventStreamFirstResultWins(t *testing.T) {
	s := NewEventStream(func(n int) bool { return n == 3 }, func(n int) int { return n })
	s.Push(3)
	v := 99
	s.End(&v)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already-cancelled ctx must not override a determined result
	for i := 0; i < 3; i++ {
		if got, err := s.Result(ctx); err != nil || got != 3 {
			t.Fatalf("iteration %d: Result = %v, %v; want 3, nil", i, got, err)
		}
	}
}

func TestEventStreamConcurrentPushNoLossNoDuplicates(t *testing.T) {
	s := plainStream()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			s.Push(n)
		}(i)
	}
	wg.Wait()
	s.End(nil)

	seen := map[int]bool{}
	for {
		item := mustNext(t, s.Next())
		if item.Done {
			break
		}
		if seen[item.Value] {
			t.Fatalf("duplicate event %d", item.Value)
		}
		seen[item.Value] = true
	}
	if len(seen) != 100 {
		t.Fatalf("saw %d events; want 100", len(seen))
	}
}

func TestEventStreamReentrantCallbackDoesNotHoldLock(t *testing.T) {
	var s *EventStream[int, int]
	var waiting <-chan StreamItem[int]
	s = NewEventStream(func(n int) bool {
		waiting = s.Next()
		return true
	}, func(n int) int { return n })

	done := make(chan struct{})
	go func() {
		s.Push(5)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Push deadlocked while callback reentered Next")
	}
	if got := mustNext(t, waiting); got.Done || got.Value != 5 {
		t.Fatalf("reentrant next = %+v; want 5", got)
	}
}

func TestEventStreamCallbackPanicPropagates(t *testing.T) {
	s := NewEventStream(func(n int) bool {
		if n < 0 {
			panic("fixture")
		}
		return false
	}, func(n int) int { return n })

	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected panic to propagate")
			}
		}()
		s.Push(-1)
	}()

	// The lock must not be retained after a callback panic.
	done := make(chan struct{})
	go func() {
		s.Push(1)
		s.End(nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("panic retained lock")
	}
}

func TestEventStreamFifoReleasesReferences(t *testing.T) {
	q := &fifoQueue[int]{}
	for i := 0; i < 5; i++ {
		q.enqueue(i)
	}
	for i := 0; i < 5; i++ {
		v, ok := q.dequeue()
		if !ok || v != i {
			t.Fatalf("dequeue = %v, %v; want %d, true", v, ok, i)
		}
	}
	if q.length() != 0 {
		t.Fatalf("length = %d; want 0", q.length())
	}
}
