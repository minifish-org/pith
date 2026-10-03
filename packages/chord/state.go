package chord

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/minifish-org/pith/packages/chord/delta"
)

const maxPendingDeliveries = 100

type stateDelivery struct {
	value    any
	ctx      context.Context
	kind     string
	sequence uint64
}

// stateSubscriber serializes one subscription's callbacks on its own worker so
// a slow subscriber never blocks the source or another subscriber.
type stateSubscriber struct {
	mu       sync.Mutex
	cond     *sync.Cond
	listener StateListener
	report   func(error)
	pending  []stateDelivery
	started  bool
	closed   bool
}

func newStateSubscriber(listener StateListener, report func(error)) *stateSubscriber {
	subscriber := &stateSubscriber{listener: listener, report: report}
	subscriber.cond = sync.NewCond(&subscriber.mu)
	go subscriber.run()
	return subscriber
}

func (s *stateSubscriber) run() {
	for {
		s.mu.Lock()
		for len(s.pending) == 0 && !s.closed {
			s.cond.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		frame := s.pending[0]
		s.pending = s.pending[1:]
		s.started = true
		s.mu.Unlock()
		if err := s.listener(frame.value, frame.ctx, StateDelivery{Kind: frame.kind, Sequence: frame.sequence}); err != nil {
			s.reportError(err)
		}
	}
}

// push queues one delivery. When the backlog reaches the upstream limit, older
// pending deliveries are discarded while a not-yet-started hydration is
// retained, so update sequences may skip.
func (s *stateSubscriber) push(frame stateDelivery) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if len(s.pending) == maxPendingDeliveries {
		if !s.started && len(s.pending) > 0 {
			hydration := s.pending[0]
			s.pending = append(s.pending[:0], hydration)
		} else {
			s.pending = s.pending[:0]
		}
	}
	s.pending = append(s.pending, frame)
	s.cond.Signal()
	s.mu.Unlock()
}

func (s *stateSubscriber) close() {
	s.mu.Lock()
	s.closed = true
	s.pending = nil
	s.cond.Broadcast()
	s.mu.Unlock()
}

func (s *stateSubscriber) reportError(err error) {
	if err == nil || s.report == nil {
		return
	}
	s.report(err)
}

// AttachedState is a synchronously hydrated publication-only state backed by
// one source attachment. Its last published value remains readable after
// disposal; later frames are ignored.
type AttachedState struct {
	mu         sync.Mutex
	attachment StateAttachment
	onError    func(error)
	value      any
	cursor     uint64
	sequence   uint64
	disposed   bool
	subs       map[*stateSubscriber]struct{}
}

// AttachReplicatedState attaches a publication-only replicated state to one
// authoritative immutable source stream.
func AttachReplicatedState(source StateSource, options StateOptions) (*AttachedState, error) {
	if source == nil {
		return nil, errors.New("chord: nil state source")
	}
	attachment, err := source.Attach()
	if err != nil {
		return nil, err
	}
	if attachment == nil {
		return nil, errors.New("chord: nil state attachment")
	}
	snapshot := attachment.Snapshot()
	if !delta.IsJSONValue(snapshot.Value) {
		attachment.Dispose()
		return nil, errors.New("chord: replicated state snapshot is not strict JSON")
	}
	state := &AttachedState{
		attachment: attachment,
		onError:    options.OnError,
		value:      snapshot.Value,
		cursor:     snapshot.Cursor,
		subs:       map[*stateSubscriber]struct{}{},
	}
	if err := attachment.Activate(state.receive); err != nil {
		state.Dispose()
		return nil, err
	}
	return state, nil
}

// Value returns the last published value.
func (s *AttachedState) Value() any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.value
}

// Subscribe installs one listener. The listener first receives hydration at the
// current publication sequence, then future updates in increasing sequence
// order. The returned function unsubscribes.
func (s *AttachedState) Subscribe(listener StateListener) (func(), error) {
	if listener == nil {
		return nil, errors.New("chord: nil state listener")
	}
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return nil, errors.New("chord: replicated state is disposed")
	}
	subscriber := newStateSubscriber(listener, s.report)
	s.subs[subscriber] = struct{}{}
	subscriber.push(stateDelivery{
		value:    s.value,
		ctx:      context.Background(),
		kind:     "hydrate",
		sequence: s.sequence,
	})
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		_, present := s.subs[subscriber]
		delete(s.subs, subscriber)
		s.mu.Unlock()
		if present {
			subscriber.close()
		}
	}, nil
}

// Dispose idempotently releases the source attachment. The last value remains
// readable.
func (s *AttachedState) Dispose() {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	s.disposed = true
	attachment := s.attachment
	s.mu.Unlock()
	if attachment != nil {
		attachment.Dispose()
	}
}

func (s *AttachedState) receive(frame StateFrame) {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return
	}
	expected := s.cursor + 1
	if frame.Cursor != expected {
		s.failLocked()
		s.mu.Unlock()
		s.report(fmt.Errorf("chord: replicated state source cursor gap: expected %d, received %d", expected, frame.Cursor))
		return
	}
	if !delta.IsJSONValue(frame.Value) {
		s.failLocked()
		s.mu.Unlock()
		s.report(errors.New("chord: replicated state revision is not strict JSON"))
		return
	}
	s.cursor = frame.Cursor
	s.value = frame.Value
	s.sequence++
	ctx := frame.Context
	if ctx == nil {
		ctx = context.Background()
	}
	delivery := stateDelivery{value: frame.Value, ctx: ctx, kind: "update", sequence: s.sequence}
	for subscriber := range s.subs {
		subscriber.push(delivery)
	}
	s.mu.Unlock()
}

// failLocked marks the attachment failed, releases the source exactly once, and
// leaves the last valid value in place. The caller must hold s.mu.
func (s *AttachedState) failLocked() {
	if s.disposed {
		return
	}
	s.disposed = true
	if s.attachment != nil {
		s.attachment.Dispose()
	}
}

func (s *AttachedState) report(err error) {
	if err == nil || s.onError == nil {
		return
	}
	s.onError(err)
}
