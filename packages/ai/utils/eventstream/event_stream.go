// Package eventstream provides a generic asynchronous event stream with FIFO
// buffering and ordered delivery to waiting consumers.
//
// This is a Go port of the FifoQueue and generic EventStream from Pi
// (packages/ai/src/utils/event-stream.ts) at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// AssistantMessageEventStream / createAssistantMessageEventStream are
// intentionally NOT part of this file; they are ported in a later
// ai-foundation step once the full AI message types exist.
package eventstream

import (
	"context"
	"sync"
)

// StreamItem is a single result yielded by Next. When Done is true the stream
// has finished and Value is the zero value of T.
type StreamItem[T any] struct {
	Value T
	Done  bool
}

// fifoQueue is an internal FIFO queue. It avoids shifting the whole backing
// array on every dequeue by using an incoming/outgoing pair of stacks, matching
// the upstream FifoQueue implementation. Dequeued references are cleared so
// long-lived consumers do not retain drained values.
type fifoQueue[T any] struct {
	incoming []T
	outgoing []T
}

func (q *fifoQueue[T]) length() int {
	return len(q.incoming) + len(q.outgoing)
}

func (q *fifoQueue[T]) enqueue(value T) {
	q.incoming = append(q.incoming, value)
}

func (q *fifoQueue[T]) dequeue() (T, bool) {
	if len(q.outgoing) == 0 {
		for len(q.incoming) > 0 {
			last := len(q.incoming) - 1
			v := q.incoming[last]
			var zero T
			q.incoming[last] = zero
			q.incoming = q.incoming[:last]
			q.outgoing = append(q.outgoing, v)
		}
		q.incoming = q.incoming[:0]
	}
	if len(q.outgoing) == 0 {
		var zero T
		return zero, false
	}
	last := len(q.outgoing) - 1
	v := q.outgoing[last]
	var zero T
	q.outgoing[last] = zero
	q.outgoing = q.outgoing[:last]
	return v, true
}

// EventStream is a generic, concurrently usable event stream.
//
// Values pushed with Push are delivered in FIFO order: either to the earliest
// registered waiting consumer (a channel returned by Next) or buffered until a
// consumer arrives. The stream never broadcasts; each event goes to exactly one
// consumer.
//
// isComplete and extractResult are user callbacks. They are invoked without any
// internal lock held, so they may safely reenter Push/Next/End. A panic in a
// callback propagates to the caller of the invoking method (typically Push).
type EventStream[T any, R any] struct {
	isComplete    func(T) bool
	extractResult func(T) R

	mu      sync.Mutex
	queue   fifoQueue[T]
	waiting fifoQueue[chan StreamItem[T]]
	done    bool

	// resultSet records whether a final result has been determined; guarded by
	// mu. resultReady is closed exactly once when resultSet flips to true, and
	// is used to wait without busy-looping.
	resultSet   bool
	result      R
	resultReady chan struct{}
}

// NewEventStream creates an EventStream. isComplete reports whether an event
// terminates the stream; extractResult derives the final result from the
// completing event.
func NewEventStream[T any, R any](isComplete func(T) bool, extractResult func(T) R) *EventStream[T, R] {
	return &EventStream[T, R]{
		isComplete:    isComplete,
		extractResult: extractResult,
		resultReady:   make(chan struct{}),
	}
}

// Push delivers an event. If the event completes the stream, the stream is
// marked done and its result is extracted before delivery, so the completing
// event is still consumable but subsequent Push calls are ignored.
//
// The completion decision, the result recording, and the choice between
// handing the event to the earliest waiting consumer or buffering it all happen
// atomically under the lock (matching upstream's synchronous push). The channel
// send itself happens after the lock is released; waiter channels are buffered
// with capacity 1 so the send never blocks.
func (s *EventStream[T, R]) Push(event T) {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	// Callbacks run without the lock held so they may reenter the stream, and so
	// a callback panic cannot leave the lock held.
	complete := s.isComplete(event)
	var extracted R
	if complete {
		extracted = s.extractResult(event)
	}

	s.mu.Lock()
	if s.done {
		// Another goroutine already completed (or ended) the stream; the event
		// is ignored, matching upstream behavior after completion.
		s.mu.Unlock()
		return
	}
	if complete {
		s.done = true
		if !s.resultSet {
			s.resultSet = true
			s.result = extracted
			close(s.resultReady)
		}
	}
	waiter, ok := s.waiting.dequeue()
	if !ok {
		s.queue.enqueue(event)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	waiter <- StreamItem[T]{Value: event}
}

// Next registers a consumer and returns a channel that will receive exactly one
// item: either the next event (Done false) or a terminal item (Done true) when
// the stream ends. The consumer is registered before Next returns. The channel
// is buffered with capacity 1 and is never closed by the stream; callers must
// not close it.
func (s *EventStream[T, R]) Next() <-chan StreamItem[T] {
	ch := make(chan StreamItem[T], 1)

	s.mu.Lock()
	if v, ok := s.queue.dequeue(); ok {
		s.mu.Unlock()
		ch <- StreamItem[T]{Value: v}
		return ch
	}
	if s.done {
		s.mu.Unlock()
		ch <- StreamItem[T]{Done: true}
		return ch
	}
	s.waiting.enqueue(ch)
	s.mu.Unlock()
	return ch
}

// End terminates the stream. If result is non-nil the final result is recorded
// (unless a result was already determined). Buffered events remain consumable
// before the Done terminal is delivered; currently waiting consumers are woken
// with Done items. A nil End does not determine the final result, so a later
// End(&zero) may still provide it.
func (s *EventStream[T, R]) End(result *R) {
	var wake []chan StreamItem[T]

	s.mu.Lock()
	s.done = true
	if result != nil && !s.resultSet {
		s.resultSet = true
		s.result = *result
		close(s.resultReady)
	}
	for {
		waiter, ok := s.waiting.dequeue()
		if !ok {
			break
		}
		wake = append(wake, waiter)
	}
	s.mu.Unlock()

	for _, waiter := range wake {
		waiter <- StreamItem[T]{Done: true}
	}
}

// Result returns the final result of the stream. It blocks until a result is
// determined or ctx is done. If a result is already determined it is returned
// even when ctx is already cancelled. Repeated calls return the same result.
func (s *EventStream[T, R]) Result(ctx context.Context) (R, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	s.mu.Lock()
	if s.resultSet {
		v := s.result
		s.mu.Unlock()
		return v, nil
	}
	ready := s.resultReady
	s.mu.Unlock()

	select {
	case <-ready:
		s.mu.Lock()
		v := s.result
		s.mu.Unlock()
		return v, nil
	case <-ctx.Done():
		var zero R
		return zero, ctx.Err()
	}
}
