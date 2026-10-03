package harness

import (
	"context"
	"sort"
	"strconv"
	"sync"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/chord/delta"
	"github.com/minifish-org/pith/packages/durable"
)

// taskGraphManager maintains task graph observers.
type taskGraphManager struct {
	h       *Harness
	mu      sync.Mutex
	watches []*TaskGraphWatch
	states  []*TaskGraphState
}

func newTaskGraphManager(h *Harness) *taskGraphManager {
	return &taskGraphManager{h: h}
}

func (m *taskGraphManager) state(ctx context.Context) (*TaskGraphState, error) {
	graph, err := m.h.buildTaskGraph(ctx)
	if err != nil {
		return nil, err
	}
	state := &TaskGraphState{h: m.h, value: graph}
	m.mu.Lock()
	m.states = append(m.states, state)
	m.mu.Unlock()
	return state, nil
}

func (m *taskGraphManager) watch(ctx context.Context) (*TaskGraphWatch, error) {
	graph, err := m.h.buildTaskGraph(ctx)
	if err != nil {
		return nil, err
	}
	watch := newTaskGraphWatch(graph)
	watch.detach = func() { m.removeWatch(watch) }
	m.mu.Lock()
	m.watches = append(m.watches, watch)
	m.mu.Unlock()
	return watch, nil
}

func (m *taskGraphManager) removeWatch(watch *TaskGraphWatch) {
	m.mu.Lock()
	for i, candidate := range m.watches {
		if candidate == watch {
			m.watches = append(m.watches[:i], m.watches[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

func (m *taskGraphManager) removeState(state *TaskGraphState) {
	m.mu.Lock()
	for i, candidate := range m.states {
		if candidate == state {
			m.states = append(m.states[:i], m.states[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

func (m *taskGraphManager) advance(ctx context.Context) {
	m.mu.Lock()
	watches := append([]*TaskGraphWatch(nil), m.watches...)
	states := append([]*TaskGraphState(nil), m.states...)
	m.mu.Unlock()
	if len(watches) == 0 && len(states) == 0 {
		return
	}
	graph, err := m.h.buildTaskGraph(ctx)
	if err != nil || graph == nil {
		return
	}
	for _, watch := range watches {
		watch.advance(graph)
	}
	for _, state := range states {
		state.advance(graph)
	}
}

func (h *Harness) buildTaskGraph(ctx context.Context) (*TaskGraph, error) {
	nodes := map[string]TaskGraphNode{}
	var records []durable.TaskRecord
	for _, status := range []string{durable.TaskStatusPending, durable.TaskStatusRunning, durable.TaskStatusWaiting, durable.TaskStatusCompleting} {
		found, err := scanAll(func(cursor durable.Cursor) (durable.Page[durable.TaskRecord], error) {
			return h.storage.ScanTasks(ctx, durable.TaskQuery{Status: status}, scanPageSize, cursor)
		})
		if err != nil {
			return nil, err
		}
		records = append(records, found...)
	}
	sort.Slice(records, func(a, b int) bool { return records[a].ID < records[b].ID })
	for _, record := range records {
		conversations, err := scanAll(func(cursor durable.Cursor) (durable.Page[durable.ConversationRecord], error) {
			return h.storage.ScanConversations(ctx, durable.ConversationQuery{OwnerTaskID: &record.ID}, scanPageSize, cursor)
		})
		if err != nil {
			return nil, err
		}
		var ids []durable.ConversationID
		for _, conversation := range conversations {
			ids = append(ids, conversation.ID)
		}
		sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
		nodes[strconv.FormatInt(int64(record.ID), 10)] = taskGraphNodeOf(record, ids)
	}
	return &TaskGraph{Tasks: nodes}, nil
}

func taskGraphNodeOf(record durable.TaskRecord, conversations []durable.ConversationID) TaskGraphNode {
	node := TaskGraphNode{
		ID:             record.ID,
		Kind:           record.Kind,
		ConversationID: record.ConversationID,
		Owner:          record.Owner,
		Background:     record.Background,
		AbortRequested: record.AbortRequested,
		State:          taskGraphStateOf(record),
		Conversations:  conversations,
	}
	return node
}

func taskGraphStateOf(record durable.TaskRecord) TaskGraphTaskState {
	state := record.State
	switch state.Status {
	case durable.TaskStatusPending, durable.TaskStatusRunning:
		return TaskGraphTaskState{Status: state.Status, Phase: phaseOf(state.Checkpoint)}
	case durable.TaskStatusWaiting:
		return TaskGraphTaskState{Status: state.Status, Phase: phaseOf(state.Checkpoint), On: state.On, Policy: state.Policy}
	default:
		outcome := ""
		if state.Outcome != nil {
			outcome = state.Outcome.Status
		}
		return TaskGraphTaskState{Status: durable.TaskStatusCompleting, Outcome: outcome}
	}
}

// TaskGraphState is a read-only replicated task graph.
type TaskGraphState struct {
	h        *Harness
	mu       sync.Mutex
	value    *TaskGraph
	disposed bool
}

// Value returns the latest committed graph.
func (s *TaskGraphState) Value() TaskGraph {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value == nil {
		return TaskGraph{Tasks: map[string]TaskGraphNode{}}
	}
	return *s.value
}

func (s *TaskGraphState) advance(value *TaskGraph) {
	s.mu.Lock()
	s.value = value
	s.mu.Unlock()
}

// Dispose releases the state.
func (s *TaskGraphState) Dispose() error {
	s.mu.Lock()
	if s.disposed {
		s.mu.Unlock()
		return nil
	}
	s.disposed = true
	s.mu.Unlock()
	s.h.graphs.removeState(s)
	return nil
}

type taskGraphFrame struct {
	value *TaskGraph
	ops   []chord.Op
}

// TaskGraphWatch is a serialized exact-frame task graph watch.
type TaskGraphWatch struct {
	mu       sync.Mutex
	value    *TaskGraph
	listener func(context.Context, any, []chord.Op) error
	pending  []taskGraphFrame
	wake     chan struct{}
	closed   chan durable.WatchEnd
	stopped  bool
	ended    bool
	detach   func()
}

func newTaskGraphWatch(value *TaskGraph) *TaskGraphWatch {
	watch := &TaskGraphWatch{value: value, wake: make(chan struct{}, 1), closed: make(chan durable.WatchEnd, 1)}
	go watch.run()
	return watch
}

// Value returns the latest committed graph.
func (w *TaskGraphWatch) Value() TaskGraph {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.value == nil {
		return TaskGraph{Tasks: map[string]TaskGraphNode{}}
	}
	return *w.value
}

// Start installs the frame listener. The callback receives the TaskGraph.
func (w *TaskGraphWatch) Start(listener func(context.Context, TaskGraph, []chord.Op) error) error {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		return closedError()
	}
	w.listener = func(ctx context.Context, value any, ops []chord.Op) error {
		graph := TaskGraph{Tasks: map[string]TaskGraphNode{}}
		if typed, ok := value.(TaskGraph); ok {
			graph = typed
		}
		return listener(ctx, graph, ops)
	}
	w.mu.Unlock()
	w.signal()
	return nil
}

// Stop stops the watch.
func (w *TaskGraphWatch) Stop() error {
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
func (w *TaskGraphWatch) Closed() <-chan durable.WatchEnd { return w.closed }

func (w *TaskGraphWatch) advance(value *TaskGraph) {
	w.mu.Lock()
	previous := w.value
	ops, _ := diffGraphs(previous, value)
	w.value = value
	if len(ops) == 0 {
		w.mu.Unlock()
		return
	}
	frame := taskGraphFrame{value: value, ops: ops}
	if len(w.pending) >= 100 {
		w.pending = []taskGraphFrame{frame}
	} else {
		w.pending = append(w.pending, frame)
	}
	w.mu.Unlock()
	w.signal()
}

func (w *TaskGraphWatch) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *TaskGraphWatch) run() {
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
			graph := TaskGraph{Tasks: map[string]TaskGraphNode{}}
			if frame.value != nil {
				graph = *frame.value
			}
			if err := listener(context.Background(), graph, frame.ops); err != nil {
				w.terminate(durable.WatchEnd{Reason: "listener_error", Err: err})
				return
			}
		}
	}
}

func (w *TaskGraphWatch) terminate(end durable.WatchEnd) {
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

func diffGraphs(previous, next *TaskGraph) ([]chord.Op, error) {
	var before any = map[string]any{}
	if previous != nil {
		before = rawToValue(mustJSON(previous))
	}
	return delta.DiffRevisions(before, rawToValue(mustJSON(next)))
}
