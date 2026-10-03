package durable

import (
	"context"
	"errors"
	"sync"

	"github.com/minifish-org/pith/packages/chord"
)

// maxPendingWatchFrames is the upstream bound on exact committed frames
// retained behind one unavailable watch listener. When that backlog overflows,
// pending updates coalesce to the newest revision while a not-yet-started
// hydration is retained. It is an upstream correctness rule, not a new
// arbitrary migration limit.
const maxPendingWatchFrames = 100

// retirementOperations is the canonical terminal update for a retired document
// incarnation or a cleared conversation view.
var retirementOperations = []chord.Op{{"r", nil}}

// observedValue is a committed document value or nil once retired.
type observedValue = JsonObject

// documentSource is the Session-to-Chord bridge owned one-to-one by one
// attached state: a document, or (later) a conversation view. A nil value
// retires it.
//
// It is the Go counterpart of upstream CommittedStateSource. Chord's
// AttachReplicatedState owns the subscriber workers, bounded pending backlog,
// and hydration sequencing; this source only forwards authoritative frames in
// source order.
type documentSource struct {
	mu          sync.Mutex
	value       observedValue
	cursor      uint64
	release     func()
	closed      bool
	retired     bool
	attachments map[*sourceAttachment]struct{}
}

func newDocumentSource(value observedValue, release func()) *documentSource {
	return &documentSource{value: value, release: release, attachments: map[*sourceAttachment]struct{}{}}
}

// Attach synchronously captures the current value and cursor and registers the
// attachment to receive every later committed frame.
func (s *documentSource) Attach() (chord.StateAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("durable: document state source is closed")
	}
	attachment := &sourceAttachment{snapshot: chord.StateSnapshot{Value: s.value, Cursor: s.cursor}}
	attachment.release = func() { s.detachAttachment(attachment) }
	s.attachments[attachment] = struct{}{}
	return attachment, nil
}

func (s *documentSource) detachAttachment(attachment *sourceAttachment) {
	s.mu.Lock()
	delete(s.attachments, attachment)
	empty := len(s.attachments) == 0 && !s.closed
	s.mu.Unlock()
	if empty {
		s.finishDisposal()
	}
}

// advance publishes one authoritative revision. A nil value retires the
// incarnation.
func (s *documentSource) advance(value observedValue, ops []chord.Op, ctx context.Context) {
	s.mu.Lock()
	if s.closed || s.retired {
		s.mu.Unlock()
		return
	}
	s.value = value
	s.cursor++
	if value == nil {
		s.retired = true
	}
	// A retired revision must reach subscribers as an untyped nil, not a typed
	// nil map, so ordinary Go nil checks observe the null reset.
	var frameValue any
	if value != nil {
		frameValue = value
	}
	frame := chord.StateFrame{Value: frameValue, Cursor: s.cursor, Ops: ops, Context: ctx}
	attachments := make([]*sourceAttachment, 0, len(s.attachments))
	for attachment := range s.attachments {
		attachments = append(attachments, attachment)
	}
	s.mu.Unlock()
	for _, attachment := range attachments {
		attachment.publish(frame)
	}
}

// closeSession disposes every attachment and releases the session subscription
// exactly once.
func (s *documentSource) closeSession() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	attachments := make([]*sourceAttachment, 0, len(s.attachments))
	for attachment := range s.attachments {
		attachments = append(attachments, attachment)
	}
	s.attachments = map[*sourceAttachment]struct{}{}
	s.mu.Unlock()
	for _, attachment := range attachments {
		attachment.Dispose()
	}
	s.finishDisposal()
}

func (s *documentSource) finishDisposal() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.value = nil
	release := s.release
	s.release = nil
	s.mu.Unlock()
	if release != nil {
		release()
	}
}

// sourceAttachment buffers frames until activation and drains them in source
// order.
type sourceAttachment struct {
	mu        sync.Mutex
	snapshot  chord.StateSnapshot
	release   func()
	frames    []chord.StateFrame
	listener  func(chord.StateFrame)
	activated bool
	disposed  bool
}

// Snapshot returns the atomic attachment boundary snapshot.
func (a *sourceAttachment) Snapshot() chord.StateSnapshot { return a.snapshot }

// Activate installs the frame listener and drains any buffered frames.
func (a *sourceAttachment) Activate(listener func(chord.StateFrame)) error {
	a.mu.Lock()
	if a.activated {
		a.mu.Unlock()
		return errors.New("durable: document state attachment is already active")
	}
	if a.disposed {
		a.mu.Unlock()
		return errors.New("durable: document state attachment is disposed")
	}
	a.activated = true
	a.listener = listener
	frames := a.frames
	a.frames = nil
	a.mu.Unlock()
	for _, frame := range frames {
		listener(frame)
	}
	return nil
}

func (a *sourceAttachment) publish(frame chord.StateFrame) {
	a.mu.Lock()
	if a.disposed {
		a.mu.Unlock()
		return
	}
	if !a.activated {
		a.frames = append(a.frames, frame)
		a.mu.Unlock()
		return
	}
	listener := a.listener
	a.mu.Unlock()
	if listener != nil {
		listener(frame)
	}
}

// Dispose releases the attachment. It is idempotent.
func (a *sourceAttachment) Dispose() {
	a.mu.Lock()
	if a.disposed {
		a.mu.Unlock()
		return
	}
	a.disposed = true
	a.frames = nil
	a.listener = nil
	release := a.release
	a.release = nil
	a.mu.Unlock()
	if release != nil {
		release()
	}
}

type watchFrame struct {
	value JsonObject
	ops   []chord.Op
	ctx   context.Context
}

// DocumentWatch is a serialized exact-frame watch bound to one document
// incarnation or conversation view. A nil value retires it. It is the Go
// counterpart of upstream CommittedWatch.
//
// Start never invokes the listener inline: frames are delivered on a private
// delivery goroutine, so a slow observer never blocks the source, another
// observer, or a Session commit.
type DocumentWatch struct {
	mu       sync.Mutex
	value    JsonObject
	detach   func()
	pending  []watchFrame
	listener func(context.Context, JsonObject, []chord.Op) error
	started  bool
	retired  bool
	end      *WatchEnd
	closed   chan WatchEnd
	done     chan struct{}
	signal   chan struct{}
	draining bool
}

func newDocumentWatch(value JsonObject) *DocumentWatch {
	return &DocumentWatch{
		value:  value,
		closed: make(chan WatchEnd, 1),
		done:   make(chan struct{}),
		signal: make(chan struct{}, 1),
	}
}

func (w *DocumentWatch) setDetach(detach func()) {
	w.mu.Lock()
	w.detach = detach
	w.mu.Unlock()
}

// Value returns an independent copy of the newest committed value.
func (w *DocumentWatch) Value() JsonObject {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.value == nil {
		return nil
	}
	clone, err := chord.CopyJSON(map[string]any(w.value))
	if err != nil {
		return nil
	}
	object, ok := clone.(map[string]any)
	if !ok {
		return nil
	}
	return JsonObject(object)
}

// Closed reports the terminal watch end. It is closed when the watch stops,
// retires, is cancelled by its acquisition context, or a listener fails.
func (w *DocumentWatch) Closed() <-chan WatchEnd { return w.closed }

// Start installs the delivery listener. A watch delivers only revisions
// committed after acquisition; the current value is read through Value.
func (w *DocumentWatch) Start(listener func(context.Context, JsonObject, []chord.Op) error) error {
	if listener == nil {
		return errors.New("durable: nil document watch listener")
	}
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return errors.New("durable: document watch is already started")
	}
	if w.end != nil {
		w.mu.Unlock()
		return errors.New("durable: document watch is stopped")
	}
	w.started = true
	w.listener = listener
	if !w.draining {
		w.draining = true
		go w.drainLoop()
	}
	w.mu.Unlock()
	w.wake()
	return nil
}

// Stop idempotently terminates delivery. An already-running callback is not
// cancelled or joined.
func (w *DocumentWatch) Stop() error {
	w.terminate(WatchEnd{Reason: "stopped"})
	return nil
}

func (w *DocumentWatch) cancel() {
	w.terminate(WatchEnd{Reason: "cancelled"})
}

func (w *DocumentWatch) closeSession() {
	w.terminate(WatchEnd{Reason: "session_closed"})
}

// observeContext cancels the watch when its acquisition context is cancelled.
func (w *DocumentWatch) observeContext(ctx context.Context) {
	if ctx == nil || ctx.Done() == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			w.terminate(WatchEnd{Reason: "cancelled", Err: ctx.Err()})
		case <-w.done:
		}
	}()
}

// advance queues one committed frame for serialized delivery.
func (w *DocumentWatch) advance(value JsonObject, ops []chord.Op, ctx context.Context) {
	w.mu.Lock()
	if w.end != nil || w.retired {
		w.mu.Unlock()
		return
	}
	if value == nil {
		w.retired = true
	}
	if len(w.pending) >= maxPendingWatchFrames {
		w.pending = w.pending[:0]
		w.pending = append(w.pending, watchFrame{value: value, ops: []chord.Op{{"r", value}}, ctx: ctx})
	} else {
		w.pending = append(w.pending, watchFrame{value: value, ops: ops, ctx: ctx})
	}
	started := w.started
	w.mu.Unlock()
	if started {
		w.wake()
	}
}

func (w *DocumentWatch) wake() {
	select {
	case w.signal <- struct{}{}:
	default:
	}
}

func (w *DocumentWatch) drainLoop() {
	for {
		<-w.signal
		for {
			w.mu.Lock()
			if w.end != nil {
				w.mu.Unlock()
				return
			}
			if !w.started || len(w.pending) == 0 {
				w.mu.Unlock()
				break
			}
			frame := w.pending[0]
			w.pending = w.pending[1:]
			w.value = frame.value
			listener := w.listener
			w.mu.Unlock()
			if listener != nil {
				ctx := frame.ctx
				if ctx == nil {
					ctx = context.Background()
				}
				if err := listener(context.WithoutCancel(ctx), frame.value, frame.ops); err != nil {
					w.terminate(WatchEnd{Reason: "listener_error", Err: err})
					return
				}
			}
			if frame.value == nil {
				w.terminate(WatchEnd{Reason: "retired"})
				return
			}
		}
	}
}

func (w *DocumentWatch) terminate(end WatchEnd) {
	w.mu.Lock()
	if w.end != nil {
		w.mu.Unlock()
		return
	}
	w.end = &end
	w.pending = nil
	detach := w.detach
	w.detach = nil
	w.mu.Unlock()
	if detach != nil {
		detach()
	}
	close(w.done)
	w.closed <- end
	close(w.closed)
}
