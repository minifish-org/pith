package adaptive_publisher

import (
	"strings"
	"sync"
	"testing"
)

// fakeClock is a deterministic Clock for tests. It mirrors vi.useFakeTimers.
type fakeClock struct {
	mu     sync.Mutex
	now    int64
	timers []*fakeTimer
}

type fakeTimer struct {
	at      int64
	fn      func()
	stopped bool
}

func (c *fakeClock) NowMs() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(ms int64, fn func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{at: c.now + ms, fn: fn}
	c.timers = append(c.timers, timer)
	return timer
}

func (t *fakeTimer) Stop() bool {
	wasActive := !t.stopped
	t.stopped = true
	return wasActive
}

func (c *fakeClock) advance(ms int64) {
	c.mu.Lock()
	target := c.now + ms
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, timer := range c.timers {
			if timer.stopped || timer.at > target {
				continue
			}
			if next == nil || timer.at < next.at {
				next = timer
			}
		}
		if next != nil {
			next.stopped = true
			if next.at > c.now {
				c.now = next.at
			}
		}
		c.mu.Unlock()
		if next == nil {
			break
		}
		next.fn()
	}
	c.mu.Lock()
	c.now = target
	c.mu.Unlock()
}

func intPointer(value int) *int { return &value }

func TestAdaptivePublisherBoundsEventCountByEncodedSize(t *testing.T) {
	clock := &fakeClock{}
	value := "a"
	updates := []string{}
	publisher := NewAdaptivePublisher(AdaptivePublisherOptions[string, string]{
		Snapshot:             func() string { return value },
		Update:               func(_ *string, current string) *string { return &current },
		Measure:              func(update string) int { return len(update) },
		Publish:              func(update string) { updates = append(updates, update) },
		OnError:              func(err error) { panic(err) },
		MinIntervalMs:        intPointer(100),
		TargetBytesPerSecond: intPointer(100),
		Clock:                clock,
	})

	publisher.MarkDirty()
	value = strings.Repeat("x", 100)
	publisher.MarkDirty()
	clock.advance(100)
	if len(updates) != 2 || updates[0] != "a" || updates[1] != strings.Repeat("x", 100) {
		t.Fatalf("unexpected updates after first interval: %#v", updates)
	}

	value = "held"
	publisher.MarkDirty()
	clock.advance(999)
	if len(updates) != 2 {
		t.Fatalf("publication should still be rate-limited: %#v", updates)
	}
	clock.advance(1)
	if len(updates) != 3 || updates[2] != "held" {
		t.Fatalf("trailing timer did not publish held state: %#v", updates)
	}
}

func TestAdaptivePublisherCommitsBaselineBeforeConsumerPanics(t *testing.T) {
	type update struct {
		Previous *string
		Current  string
	}
	clock := &fakeClock{}
	value := "a"
	updates := []update{}
	throwAfterApply := false
	publisher := NewAdaptivePublisher(AdaptivePublisherOptions[string, update]{
		Snapshot: func() string { return value },
		Update: func(previous *string, current string) *update {
			return &update{Previous: previous, Current: current}
		},
		Measure: func(update) int { return 1 },
		Publish: func(current update) {
			updates = append(updates, current)
			if throwAfterApply {
				panic("consumer failed after apply")
			}
		},
		OnError:              func(error) {},
		MinIntervalMs:        intPointer(100),
		TargetBytesPerSecond: intPointer(100),
		Clock:                clock,
	})

	publisher.MarkDirty()
	clock.advance(100)
	value = "ab"
	throwAfterApply = true
	panicked := func() (panicked bool) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		publisher.MarkDirty()
		return false
	}()
	if !panicked {
		t.Fatalf("expected consumer panic to propagate from MarkDirty")
	}
	publisher.Flush(true)
	if len(updates) != 2 {
		t.Fatalf("baseline was not committed before delivery: %#v", updates)
	}
	if updates[1].Previous == nil || *updates[1].Previous != "a" || updates[1].Current != "ab" {
		t.Fatalf("unexpected committed baseline: %#v", updates[1])
	}

	throwAfterApply = false
	clock.advance(100)
	value = "abc"
	publisher.MarkDirty()
	last := updates[len(updates)-1]
	if last.Previous == nil || *last.Previous != "ab" || last.Current != "abc" {
		t.Fatalf("unexpected final publication: %#v", last)
	}
}

func TestAdaptivePublisherDisposeCancelsTrailingTimer(t *testing.T) {
	clock := &fakeClock{}
	value := "a"
	updates := []string{}
	publisher := NewAdaptivePublisher(AdaptivePublisherOptions[string, string]{
		Snapshot:             func() string { return value },
		Update:               func(_ *string, current string) *string { return &current },
		Measure:              func(string) int { return 1 },
		Publish:              func(update string) { updates = append(updates, update) },
		OnError:              func(error) {},
		MinIntervalMs:        intPointer(100),
		TargetBytesPerSecond: intPointer(100),
		Clock:                clock,
	})
	publisher.MarkDirty()
	if len(updates) != 1 {
		t.Fatalf("first publication should be immediate: %#v", updates)
	}
	value = "b"
	publisher.MarkDirty()
	publisher.Dispose()
	clock.advance(1_000)
	if len(updates) != 1 {
		t.Fatalf("disposed publisher published again: %#v", updates)
	}
}
