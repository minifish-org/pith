package harness

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/chord/delta"
	"github.com/minifish-org/pith/packages/durable"
)

// viewManager maintains one conversation view per observer.
type viewManager struct {
	h       *Harness
	mu      sync.Mutex
	watches map[durable.ConversationID][]*ConversationWatch
	states  map[durable.ConversationID][]*ConversationState
	events  []*agentEventStream
	last    map[durable.ConversationID]*ConversationView
}

func newViewManager(h *Harness) *viewManager {
	return &viewManager{
		h:       h,
		watches: map[durable.ConversationID][]*ConversationWatch{},
		states:  map[durable.ConversationID][]*ConversationState{},
		last:    map[durable.ConversationID]*ConversationView{},
	}
}

func (m *viewManager) state(ctx context.Context, id durable.ConversationID) (*ConversationState, error) {
	value, err := m.h.buildView(ctx, id)
	if err != nil {
		return nil, err
	}
	state := &ConversationState{h: m.h, id: id, value: value}
	m.mu.Lock()
	m.states[id] = append(m.states[id], state)
	m.mu.Unlock()
	return state, nil
}

func (m *viewManager) watch(ctx context.Context, id durable.ConversationID) (*ConversationWatch, error) {
	value, err := m.h.buildView(ctx, id)
	if err != nil {
		return nil, err
	}
	watch := newConversationWatch(value)
	m.mu.Lock()
	m.watches[id] = append(m.watches[id], watch)
	m.mu.Unlock()
	watch.setDetach(func() { m.removeWatch(id, watch) })
	return watch, nil
}

func (m *viewManager) removeWatch(id durable.ConversationID, watch *ConversationWatch) {
	m.mu.Lock()
	list := m.watches[id]
	for i, candidate := range list {
		if candidate == watch {
			m.watches[id] = append(list[:i], list[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

func (m *viewManager) removeState(id durable.ConversationID, state *ConversationState) {
	m.mu.Lock()
	list := m.states[id]
	for i, candidate := range list {
		if candidate == state {
			m.states[id] = append(list[:i], list[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

// advanceConversation rebuilds and delivers each live view of a conversation.
func (m *viewManager) advanceConversation(ctx context.Context, id durable.ConversationID, publication durable.CommitPublication) {
	m.mu.Lock()
	watches := append([]*ConversationWatch(nil), m.watches[id]...)
	states := append([]*ConversationState(nil), m.states[id]...)
	events := append([]*agentEventStream(nil), m.events...)
	before := m.last[id]
	m.mu.Unlock()
	if len(watches) == 0 && len(states) == 0 && len(events) == 0 {
		return
	}
	value, err := m.h.buildView(ctx, id)
	if err != nil || value == nil {
		return
	}
	m.mu.Lock()
	m.last[id] = value
	m.mu.Unlock()
	for _, watch := range watches {
		watch.advance(value, ctx)
	}
	for _, state := range states {
		state.advance(value)
	}
	for _, stream := range events {
		if stream.conversationID != id {
			continue
		}
		if translated := translateEvents(id, before, value, publication); len(translated) > 0 {
			stream.push(translated)
		}
	}
}

func (h *Harness) buildView(ctx context.Context, id durable.ConversationID) (*ConversationView, error) {
	record, err := h.storage.Conversation(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	bounds, err := captureContextBounds(ctx, h.storage, id, nil)
	if err != nil {
		return nil, err
	}
	entries, err := activeEntries(ctx, h.storage, id, bounds)
	if err != nil {
		return nil, err
	}
	docs := map[string]durable.JsonObject{}
	for _, definition := range []*durable.DocumentDefinition{AgentDoc, LiveDoc, InboxDoc, UsageDoc} {
		value, err := loadDocValue(ctx, h.storage, *definition, durable.ConversationAddress(definition, id, nil))
		if err != nil {
			return nil, err
		}
		if value != nil {
			docs[definition.Kind] = value
		}
	}
	return &ConversationView{Conversation: *record, Entries: entries, Docs: docs}, nil
}

func loadDocValue(ctx context.Context, storage durable.Storage, definition durable.DocumentDefinition, address durable.DocumentAddress) (durable.JsonObject, error) {
	record, err := storage.FindDocument(ctx, address, durable.CurrentDocument())
	if err != nil || record == nil {
		return nil, err
	}
	stored, err := storage.Document(ctx, record.ID, durable.CurrentDocument())
	if err != nil || stored == nil {
		return nil, err
	}
	return durable.MaterializeDocument(&definition, stored.Record.Kind, stored.Record.Scope, stored.Record.History, stored.Record.Fork, stored.Record.ID, stored.Version, stored.Value)
}

// ConversationState is a read-only replicated conversation view.
type ConversationState struct {
	h        *Harness
	id       durable.ConversationID
	mu       sync.Mutex
	value    *ConversationView
	disposed bool
}

// Value returns the latest committed view.
func (s *ConversationState) Value() ConversationView {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value == nil {
		return ConversationView{}
	}
	return *s.value
}

func (s *ConversationState) advance(value *ConversationView) {
	s.mu.Lock()
	s.value = value
	s.mu.Unlock()
}

// Dispose releases the state.
func (s *ConversationState) Dispose() error {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return nil
	}
	s.disposed = true
	s.mu.Unlock()
	s.h.views.removeState(s.id, s)
	return nil
}

type conversationFrame struct {
	value *ConversationView
	ops   []chord.Op
	ctx   context.Context
}

// ConversationWatch is a serialized exact-frame conversation watch.
type ConversationWatch struct {
	mu       sync.Mutex
	value    *ConversationView
	listener func(context.Context, ConversationView, []chord.Op) error
	pending  []conversationFrame
	wake     chan struct{}
	closed   chan durable.WatchEnd
	stopped  bool
	detach   func()
	ended    bool
}

func newConversationWatch(value *ConversationView) *ConversationWatch {
	watch := &ConversationWatch{value: value, wake: make(chan struct{}, 1), closed: make(chan durable.WatchEnd, 1)}
	go watch.run()
	return watch
}

func (w *ConversationWatch) setDetach(detach func()) { w.detach = detach }

// Value returns the latest committed view.
func (w *ConversationWatch) Value() ConversationView {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.value == nil {
		return ConversationView{}
	}
	return *w.value
}

// Start installs the frame listener.
func (w *ConversationWatch) Start(listener func(context.Context, ConversationView, []chord.Op) error) error {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return closedError()
	}
	w.listener = listener
	w.mu.Unlock()
	w.signal()
	return nil
}

// Stop stops the watch. It is idempotent.
func (w *ConversationWatch) Stop() error {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return nil
	}
	w.stopped = true
	w.mu.Unlock()
	if w.detach != nil {
		w.detach()
	}
	w.terminate(durable.WatchEnd{Reason: "stopped"})
	return nil
}

// Closed reports why the watch ended.
func (w *ConversationWatch) Closed() <-chan durable.WatchEnd { return w.closed }

func (w *ConversationWatch) advance(value *ConversationView, ctx context.Context) {
	w.mu.Lock()
	previous := w.value
	ops, _ := diffViews(previous, value)
	w.value = value
	if len(ops) == 0 {
		w.mu.Unlock()
		return
	}
	frame := conversationFrame{value: value, ops: ops, ctx: context.WithoutCancel(ctx)}
	if len(w.pending) >= 100 {
		// Coalesce to the newest complete frame.
		w.pending = []conversationFrame{frame}
	} else {
		w.pending = append(w.pending, frame)
	}
	w.mu.Unlock()
	w.signal()
}

func (w *ConversationWatch) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *ConversationWatch) run() {
	for range w.wake {
		for {
			w.mu.Lock()
			if w.stopped || len(w.pending) == 0 || w.listener == nil {
				w.mu.Unlock()
				break
			}
			frame := w.pending[0]
			w.pending = w.pending[1:]
			listener := w.listener
			w.mu.Unlock()
			value := ConversationView{}
			if frame.value != nil {
				value = *frame.value
			}
			frameCtx := frame.ctx
			if frameCtx == nil {
				frameCtx = context.Background()
			}
			if err := listener(frameCtx, value, frame.ops); err != nil {
				w.terminate(durable.WatchEnd{Reason: "listener_error", Err: err})
				return
			}
		}
	}
}

func (w *ConversationWatch) terminate(end durable.WatchEnd) {
	w.mu.Lock()
	if w.ended {
		w.mu.Unlock()
		return
	}
	w.ended = true
	w.stopped = true
	if w.detach != nil {
		w.detach()
	}
	w.mu.Unlock()
	select {
	case w.closed <- end:
	default:
	}
}

func (w *ConversationWatch) closeSession() {
	w.terminate(durable.WatchEnd{Reason: "session_closed"})
}

// diffViews computes the Chord operations between two views.
func diffViews(previous, next *ConversationView) ([]chord.Op, error) {
	var before any
	if previous != nil {
		before = viewJSON(previous)
	}
	after := viewJSON(next)
	if before == nil {
		before = map[string]any{}
	}
	return delta.DiffRevisions(before, after)
}

func viewJSON(value *ConversationView) any {
	if value == nil {
		return map[string]any{}
	}
	return map[string]any{
		"conversation": jsonValueOfView(value.Conversation),
		"entries":      entriesJSON(value.Entries),
		"docs":         docsJSON(value.Docs),
	}
}

func entriesJSON(entries []durable.EntryRecord) []any {
	out := make([]any, 0, len(entries))
	for i := range entries {
		out = append(out, rawToValue(mustJSON(entries[i])))
	}
	return out
}

func docsJSON(docs map[string]durable.JsonObject) map[string]any {
	out := map[string]any{}
	for key, value := range docs {
		out[key] = map[string]any(value)
	}
	return out
}

func jsonValueOfView(value durable.ConversationRecord) any {
	return rawToValue(mustJSON(value))
}

var _ = json.Marshal
