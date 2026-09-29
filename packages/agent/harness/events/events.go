// Package harnesevents is the Go port of
// packages/agent/src/harness/events.ts.
//
// This is a Go port of Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The bus delivers events to isolated per-type listeners and to watchers.
// Recipients are snapshotted at emit time, so listeners registered during
// delivery do not observe the in-flight event. Handler failures never abort the
// remaining recipients; they are reported through a synthetic `handler_error`
// event.
//
// Go adaptation: delivery is synchronous and ordered. It preserves the
// upstream observable ordering and recipient isolation while replacing the
// promise chain with direct calls; a slow listener therefore applies
// back-pressure to the emitter.
package harnesevents

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
)

// Context is the invocation-scoped harness context.
type Context = harnesscontext.Context

// HarnessEventType is the event type discriminator.
type HarnessEventType = string

// HarnessEvent is one observable harness event.
//
// Lane and OperationID are optional. Recovery marks a replayed event during
// recovery rather than live execution. Payload carries the type-specific body
// verbatim so no event data is dropped.
type HarnessEvent struct {
	Type        string          `json:"type"`
	Lane        *string         `json:"lane,omitempty"`
	OperationID *string         `json:"operationId,omitempty"`
	Recovery    *bool           `json:"recovery,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// EventListener observes one delivered event.
type EventListener func(event HarnessEvent, ctx Context) error

// Unsubscribe removes a registered listener.
type Unsubscribe func()

// Events is the passive subscription surface of the bus.
type Events interface {
	On(eventType HarnessEventType, listener EventListener) Unsubscribe
}

// HandlerErrorKind is the kind field of a handler_error payload.
const HandlerErrorKindEvent = "event"

// HarnessEventBus is a passive, isolated harness event bus.
type HarnessEventBus struct {
	mu             sync.Mutex
	listeners      map[HarnessEventType][]*listenerEntry
	watchListeners []*watchRegistration
	nextID         int64
	closedErr      error
}

type listenerEntry struct {
	id int64
	fn EventListener
}

type watchRegistration struct {
	push EventListener
}

// NewHarnessEventBus creates an empty event bus.
func NewHarnessEventBus() *HarnessEventBus {
	return &HarnessEventBus{listeners: map[HarnessEventType][]*listenerEntry{}}
}

// On subscribes to one event type.
func (b *HarnessEventBus) On(eventType HarnessEventType, listener EventListener) Unsubscribe {
	b.mu.Lock()
	if b.closedErr != nil {
		err := b.closedErr
		b.mu.Unlock()
		panic(err)
	}
	b.nextID++
	entry := &listenerEntry{id: b.nextID, fn: listener}
	b.listeners[eventType] = append(b.listeners[eventType], entry)
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		current := b.listeners[eventType]
		for index, candidate := range current {
			if candidate == entry {
				b.listeners[eventType] = append(current[:index], current[index+1:]...)
				return
			}
		}
	}
}

// Emit delivers one event.
func (b *HarnessEventBus) Emit(ctx Context, event HarnessEvent) error {
	return b.EmitBatch(ctx, []HarnessEvent{event})
}

// EmitBatch delivers a contiguous batch, snapshotting recipients per event
// before any handler runs.
func (b *HarnessEventBus) EmitBatch(ctx Context, events []HarnessEvent) error {
	if len(events) == 0 {
		return nil
	}
	type bound struct {
		payload    HarnessEvent
		recipients []EventListener
	}
	boundEvents := make([]bound, 0, len(events))
	for _, event := range events {
		payload := cloneEvent(event)
		boundEvents = append(boundEvents, bound{payload: payload, recipients: b.snapshotRecipients(payload)})
	}
	b.mu.Lock()
	if b.closedErr != nil {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	for _, item := range boundEvents {
		b.deliver(ctx, item.payload, item.recipients, true)
	}
	return nil
}

func (b *HarnessEventBus) snapshotRecipients(event HarnessEvent) []EventListener {
	b.mu.Lock()
	defer b.mu.Unlock()
	entries := b.listeners[event.Type]
	recipients := make([]EventListener, 0, len(entries)+len(b.watchListeners))
	for _, entry := range entries {
		recipients = append(recipients, entry.fn)
	}
	for _, watch := range b.watchListeners {
		recipients = append(recipients, watch.push)
	}
	return recipients
}

func (b *HarnessEventBus) deliver(ctx Context, event HarnessEvent, recipients []EventListener, reportErrors bool) {
	for _, listener := range recipients {
		err := callListener(listener, cloneEvent(event), ctx)
		if err == nil || !reportErrors || event.Type == "handler_error" {
			continue
		}
		handlerError := makeHandlerError(err, event)
		b.deliver(ctx, handlerError, b.snapshotRecipients(handlerError), false)
	}
}

// Close seals the bus. Listeners registered after close observe the error;
// already-registered listeners are cleared.
func (b *HarnessEventBus) Close(err error) {
	b.mu.Lock()
	if b.closedErr == nil {
		b.closedErr = err
	}
	b.mu.Unlock()
	b.mu.Lock()
	b.listeners = map[HarnessEventType][]*listenerEntry{}
	b.watchListeners = nil
	b.mu.Unlock()
}

// WatchHandle is a live subscription that starts from a snapshot.
type WatchHandle[T any] struct {
	mu           sync.Mutex
	snapshot     T
	buffer       []watchedEvent
	listener     EventListener
	started      bool
	unsubscribed bool
	resnapshotFn func(Context, func()) (T, error)
	onError      func(error, HarnessEvent, Context)
	unsubscribe  Unsubscribe
	state        *resnapshotState
}

type watchedEvent struct {
	event   HarnessEvent
	context Context
}

type resnapshotState struct {
	phase   string
	held    []watchedEvent
	reached chan struct{}
	once    sync.Once
}

// SetSnapshot replaces the watcher's baseline snapshot.
func (w *WatchHandle[T]) SetSnapshot(snapshot T) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.snapshot = snapshot
}

// Snapshot returns the current baseline snapshot.
func (w *WatchHandle[T]) Snapshot() T {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snapshot
}

// Start activates the watcher. It may be called only once.
func (w *WatchHandle[T]) Start(listener EventListener) error {
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return errors.New("WatchHandle.start() may be called only once")
	}
	if w.unsubscribed {
		w.mu.Unlock()
		return errors.New("WatchHandle is unsubscribed")
	}
	w.started = true
	w.listener = listener
	buffered := w.buffer
	w.buffer = nil
	w.mu.Unlock()
	for _, item := range buffered {
		w.deliver(item)
	}
	return nil
}

// Resnapshot captures a fresh baseline. Events are dropped until the capture
// marks its boundary, then held until the new baseline is installed.
func (w *WatchHandle[T]) Resnapshot(ctx Context) (T, error) {
	w.mu.Lock()
	if w.unsubscribed {
		w.mu.Unlock()
		var zero T
		return zero, errors.New("WatchHandle is unsubscribed")
	}
	if w.resnapshotFn == nil {
		w.mu.Unlock()
		var zero T
		return zero, errors.New("WatchHandle does not support resnapshot")
	}
	if w.state != nil {
		w.mu.Unlock()
		var zero T
		return zero, errors.New("WatchHandle resnapshot is already in progress")
	}
	state := &resnapshotState{phase: "dropping", reached: make(chan struct{})}
	w.state = state
	w.mu.Unlock()

	snapshot, err := w.resnapshotFn(ctx, func() {
		state.once.Do(func() {
			w.mu.Lock()
			if state.phase == "dropping" {
				state.phase = "holding"
			}
			w.mu.Unlock()
			close(state.reached)
		})
	})
	if err != nil {
		w.mu.Lock()
		w.state = nil
		held := state.held
		w.mu.Unlock()
		for _, item := range held {
			w.Push(item.event, item.context)
		}
		var zero T
		return zero, err
	}
	<-state.reached
	w.mu.Lock()
	w.snapshot = snapshot
	w.state = nil
	held := state.held
	w.mu.Unlock()
	for _, item := range held {
		w.Push(item.event, item.context)
	}
	return snapshot, nil
}

// Unsubscribe stops the watcher.
func (w *WatchHandle[T]) Unsubscribe() {
	w.mu.Lock()
	if w.unsubscribed {
		w.mu.Unlock()
		return
	}
	w.unsubscribed = true
	w.buffer = nil
	w.listener = nil
	unsubscribe := w.unsubscribe
	w.unsubscribe = nil
	w.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

// Push feeds one filtered event to the watcher.
func (w *WatchHandle[T]) Push(event HarnessEvent, ctx Context) {
	w.mu.Lock()
	if w.unsubscribed {
		w.mu.Unlock()
		return
	}
	if w.state != nil {
		if w.state.phase == "dropping" {
			w.mu.Unlock()
			return
		}
		w.state.held = append(w.state.held, watchedEvent{event: event, context: ctx})
		w.mu.Unlock()
		return
	}
	if !w.started {
		w.buffer = append(w.buffer, watchedEvent{event: event, context: ctx})
		w.mu.Unlock()
		return
	}
	listener := w.listener
	w.mu.Unlock()
	if listener == nil {
		return
	}
	if err := callListener(listener, cloneEvent(event), ctx); err != nil && w.onError != nil {
		w.onError(err, event, ctx)
	}
}

func (w *WatchHandle[T]) deliver(item watchedEvent) {
	w.mu.Lock()
	listener := w.listener
	w.mu.Unlock()
	if listener == nil {
		return
	}
	if err := callListener(listener, cloneEvent(item.event), item.context); err != nil && w.onError != nil {
		w.onError(err, item.event, item.context)
	}
}

// Watch installs a watcher with an initial snapshot. It is a package-level
// function because Go methods cannot declare their own type parameters.
func Watch[T any](
	bus *HarnessEventBus,
	snapshot T,
	filter func(HarnessEvent) bool,
	ctx Context,
	resnapshot func(Context, func()) (T, error),
) (*WatchHandle[T], error) {
	if bus == nil {
		return nil, errors.New("harnesevents: nil bus")
	}
	return installWatcher(bus, snapshot, filter, resnapshot)
}

// WatchFromSnapshot captures a baseline before installing the watcher.
func WatchFromSnapshot[T any](
	bus *HarnessEventBus,
	capture func(Context) (T, error),
	filter func(HarnessEvent) bool,
	ctx Context,
) (*WatchHandle[T], error) {
	if bus == nil {
		return nil, errors.New("harnesevents: nil bus")
	}
	watcher, err := installWatcher[T](bus, zeroValue[T](), filter, func(captureCtx Context, markBoundary func()) (T, error) {
		snapshot, captureErr := capture(captureCtx)
		if captureErr != nil {
			return snapshot, captureErr
		}
		markBoundary()
		return snapshot, nil
	})
	if err != nil {
		return nil, err
	}
	snapshot, err := capture(ctx)
	if err != nil {
		watcher.Unsubscribe()
		return nil, err
	}
	watcher.SetSnapshot(snapshot)
	return watcher, nil
}

func installWatcher[T any](b *HarnessEventBus,
	snapshot T,
	filter func(HarnessEvent) bool,
	resnapshot func(Context, func()) (T, error),
) (*WatchHandle[T], error) {
	b.mu.Lock()
	if b.closedErr != nil {
		err := b.closedErr
		b.mu.Unlock()
		return nil, err
	}
	watcher := &WatchHandle[T]{
		snapshot:     snapshot,
		resnapshotFn: resnapshot,
	}
	if resnapshot != nil {
		watcher.onError = func(err error, event HarnessEvent, ctx Context) {
			if event.Type == "handler_error" {
				return
			}
			handlerError := makeHandlerError(err, event)
			_ = b.Emit(ctx, handlerError)
		}
	}
	registration := &watchRegistration{push: func(event HarnessEvent, ctx Context) error {
		if filter == nil || filter(event) {
			watcher.Push(event, ctx)
		}
		return nil
	}}
	watcher.unsubscribe = func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for index, candidate := range b.watchListeners {
			if candidate == registration {
				b.watchListeners = append(b.watchListeners[:index], b.watchListeners[index+1:]...)
				return
			}
		}
	}
	b.watchListeners = append(b.watchListeners, registration)
	b.mu.Unlock()
	return watcher, nil
}

func zeroValue[T any]() T {
	var zero T
	return zero
}

func makeHandlerError(err error, event HarnessEvent) HarnessEvent {
	payload := map[string]any{
		"kind":  HandlerErrorKindEvent,
		"event": event.Type,
		"error": err.Error(),
	}
	encoded, _ := json.Marshal(payload)
	handlerError := HarnessEvent{
		Type:        "handler_error",
		OperationID: event.OperationID,
		Payload:     encoded,
	}
	if event.Lane != nil {
		lane := *event.Lane
		handlerError.Lane = &lane
	}
	return handlerError
}

func callListener(listener EventListener, event HarnessEvent, ctx Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return listener(event, ctx)
}

func cloneEvent(event HarnessEvent) HarnessEvent {
	clone := event
	if event.Lane != nil {
		lane := *event.Lane
		clone.Lane = &lane
	}
	if event.OperationID != nil {
		id := *event.OperationID
		clone.OperationID = &id
	}
	if event.Recovery != nil {
		recovery := *event.Recovery
		clone.Recovery = &recovery
	}
	if event.Payload != nil {
		clone.Payload = append(json.RawMessage(nil), event.Payload...)
	}
	return clone
}
