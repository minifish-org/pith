// Package adaptive_publisher is the Go port of
// packages/agent/src/harness/utils/adaptive-publisher.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The publisher collapses bursty state changes into the latest snapshot and
// spaces publications proportionally to their encoded size. The first dirty
// state after idle is published immediately; a single trailing timer
// guarantees eventual delivery. Time is provided through Clock so the real
// scheduler can be replaced with a deterministic fake in tests.
package adaptive_publisher

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Timer is a stoppable scheduled callback.
type Timer interface {
	// Stop cancels the timer. It reports whether the timer was active.
	Stop() bool
}

// Clock supplies the wall clock and scheduled callbacks used by
// AdaptivePublisher. The real implementation is RealClock.
type Clock interface {
	// NowMs returns the current time as Unix milliseconds.
	NowMs() int64
	// AfterFunc schedules fn after ms milliseconds (0 means as soon as
	// possible) and returns a handle that can cancel it.
	AfterFunc(ms int64, fn func()) Timer
}

// RealClock is the production Clock backed by the time package.
type RealClock struct{}

// NowMs returns the current time as Unix milliseconds.
func (RealClock) NowMs() int64 { return time.Now().UnixMilli() }

// AfterFunc schedules fn on the runtime timer wheel.
func (RealClock) AfterFunc(ms int64, fn func()) Timer {
	if ms < 0 {
		ms = 0
	}
	return time.AfterFunc(time.Duration(ms)*time.Millisecond, fn)
}

// AdaptivePublisherOptions configures an AdaptivePublisher. Snapshot, Update,
// Measure, Publish and OnError are required. MinIntervalMs and
// TargetBytesPerSecond mirror the upstream defaults when nil.
type AdaptivePublisherOptions[TValue any, TUpdate any] struct {
	// Snapshot captures the current producer state.
	Snapshot func() TValue
	// Update derives a publication from the previously published value and
	// the current snapshot. Returning nil suppresses the publication.
	Update func(previous *TValue, current TValue) *TUpdate
	// Measure reports the encoded size of an update in bytes.
	Measure func(update TUpdate) int
	// Publish delivers an update. It must not be called with internal locks
	// held; a panic is reported through OnError when invoked from the timer.
	Publish func(update TUpdate)
	// OnError receives panics raised by timer-driven publications.
	OnError func(error)
	// MinIntervalMs is the lower bound between publications. Defaults to 100.
	MinIntervalMs *int
	// TargetBytesPerSecond is the throughput budget. Defaults to 100 KiB/s.
	TargetBytesPerSecond *int
	// Clock overrides the time source. Defaults to RealClock.
	Clock Clock
}

// AdaptivePublisher publishes the latest state without queuing intermediate
// mutations. The first dirty state after idle is immediate. Each publication
// then buys a delay proportional to its encoded size, with a minimum interval
// that also bounds event count. A single trailing timer guarantees eventual
// publication.
type AdaptivePublisher[TValue any, TUpdate any] struct {
	options              AdaptivePublisherOptions[TValue, TUpdate]
	minIntervalMs        int
	targetBytesPerSecond int
	clock                Clock

	mu        sync.Mutex
	published *TValue
	dirty     bool
	nextEmit  int64
	timer     Timer
	disposed  bool
	flushing  bool
}

// NewAdaptivePublisher builds a publisher from the supplied options.
func NewAdaptivePublisher[TValue any, TUpdate any](options AdaptivePublisherOptions[TValue, TUpdate]) *AdaptivePublisher[TValue, TUpdate] {
	minInterval := 100
	if options.MinIntervalMs != nil {
		minInterval = *options.MinIntervalMs
	}
	target := 100 * 1024
	if options.TargetBytesPerSecond != nil {
		target = *options.TargetBytesPerSecond
	}
	clock := options.Clock
	if clock == nil {
		clock = RealClock{}
	}
	return &AdaptivePublisher[TValue, TUpdate]{
		options:              options,
		minIntervalMs:        minInterval,
		targetBytesPerSecond: target,
		clock:                clock,
	}
}

// MarkDirty records that the producer changed. Publication happens
// immediately when the interval budget allows, otherwise after the trailing
// timer fires.
func (p *AdaptivePublisher[TValue, TUpdate]) MarkDirty() {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return
	}
	p.dirty = true
	wait := p.nextEmit - p.clock.NowMs()
	p.mu.Unlock()
	if wait <= 0 {
		p.flush(false)
		return
	}
	p.armTimer(wait)
}

// Flush publishes the pending snapshot immediately when force is true, or
// when the interval budget has elapsed. It is a no-op when nothing is dirty.
func (p *AdaptivePublisher[TValue, TUpdate]) Flush(force bool) {
	p.flush(force)
}

// Dispose stops the trailing timer and marks the publisher unusable.
func (p *AdaptivePublisher[TValue, TUpdate]) Dispose() {
	p.mu.Lock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.disposed = true
	p.mu.Unlock()
}

func (p *AdaptivePublisher[TValue, TUpdate]) armTimer(wait int64) {
	p.mu.Lock()
	if p.disposed || p.timer != nil {
		p.mu.Unlock()
		return
	}
	p.timer = p.clock.AfterFunc(wait, p.onTimer)
	p.mu.Unlock()
}

func (p *AdaptivePublisher[TValue, TUpdate]) onTimer() {
	defer func() {
		if recovered := recover(); recovered != nil {
			if p.options.OnError != nil {
				p.options.OnError(toError(recovered))
			}
		}
	}()
	p.mu.Lock()
	p.timer = nil
	p.mu.Unlock()
	p.flush(false)
}

// flush performs at most one publication cycle. It never holds the state lock
// while invoking snapshot/update/measure/publish so a consumer may apply the
// update and then reenter the producer.
func (p *AdaptivePublisher[TValue, TUpdate]) flush(force bool) {
	p.mu.Lock()
	if p.disposed || !p.dirty {
		p.mu.Unlock()
		return
	}
	if p.flushing {
		p.mu.Unlock()
		return
	}
	now := p.clock.NowMs()
	if !force && now < p.nextEmit {
		wait := p.nextEmit - now
		p.mu.Unlock()
		p.armTimer(wait)
		return
	}
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.flushing = true
	p.mu.Unlock()

	func() {
		defer func() {
			p.mu.Lock()
			p.flushing = false
			p.mu.Unlock()
		}()

		current := p.options.Snapshot()
		update := p.options.Update(p.published, current)
		if update == nil {
			p.mu.Lock()
			p.published = &current
			p.dirty = false
			p.mu.Unlock()
			return
		}
		encoded := p.options.Measure(*update)
		delay := encoded * 1000 / p.targetBytesPerSecond
		if delay < p.minIntervalMs {
			delay = p.minIntervalMs
		}
		// Commit before delivery: a consumer may apply the update and then
		// panic or reenter the producer; retaining the old baseline would
		// duplicate that delta.
		p.mu.Lock()
		p.published = &current
		p.dirty = false
		p.nextEmit = now + int64(delay)
		p.mu.Unlock()
		p.options.Publish(*update)
	}()

	// A reentrant producer may have marked the publisher dirty during
	// delivery and the new interval may already be due.
	p.mu.Lock()
	retry := p.dirty && !p.disposed && p.clock.NowMs() >= p.nextEmit
	p.mu.Unlock()
	if retry {
		p.flush(false)
	}
}

func toError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	if text, ok := value.(string); ok {
		return errors.New(text)
	}
	return fmt.Errorf("%v", value)
}
