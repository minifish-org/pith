package harness

import (
	"context"
	"sync"

	"github.com/minifish-org/pith/packages/durable"
)

// AgentEventStream is implemented by agentEventStream.
type agentEventStream struct {
	mu             sync.Mutex
	conversationID durable.ConversationID
	snapshot       AgentEvent
	listener       func(context.Context, []AgentEvent) error
	pending        [][]AgentEvent
	wake           chan struct{}
	closed         chan durable.WatchEnd
	stopped        bool
	ended          bool
	detach         func()
}

// WatchEvents attaches to one conversation's agent events.
func WatchEvents(ctx context.Context, h *Harness, conversationID durable.ConversationID) (AgentEventStream, error) {
	view, err := h.buildView(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if view == nil {
		return nil, errNotFound
	}
	stream := &agentEventStream{
		conversationID: conversationID,
		snapshot:       snapshotEvent(view),
		wake:           make(chan struct{}, 1),
		closed:         make(chan durable.WatchEnd, 1),
	}
	h.views.mu.Lock()
	h.views.events = append(h.views.events, stream)
	h.views.last[conversationID] = view
	h.views.mu.Unlock()
	stream.detach = func() { h.views.removeEvent(stream) }
	go stream.run()
	return stream, nil
}

func (m *viewManager) removeEvent(stream *agentEventStream) {
	m.mu.Lock()
	for i, candidate := range m.events {
		if candidate == stream {
			m.events = append(m.events[:i], m.events[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
}

// Snapshot returns the attachment snapshot event.
func (s *agentEventStream) Snapshot() AgentEvent { return s.snapshot }

// Start installs the listener.
func (s *agentEventStream) Start(listener func(context.Context, []AgentEvent) error) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return closedError()
	}
	s.listener = listener
	s.mu.Unlock()
	s.signal()
	return nil
}

// Stop stops the stream.
func (s *agentEventStream) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	s.mu.Unlock()
	if s.detach != nil {
		s.detach()
	}
	s.terminate(durable.WatchEnd{Reason: "stopped"})
	return nil
}

// Closed reports why the stream ended.
func (s *agentEventStream) Closed() <-chan durable.WatchEnd { return s.closed }

func (s *agentEventStream) push(events []AgentEvent) {
	if len(events) == 0 {
		return
	}
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	if len(s.pending) >= 100 {
		s.pending = [][]AgentEvent{events}
	} else {
		s.pending = append(s.pending, events)
	}
	s.mu.Unlock()
	s.signal()
}

func (s *agentEventStream) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *agentEventStream) run() {
	for range s.wake {
		for {
			s.mu.Lock()
			if s.stopped || len(s.pending) == 0 || s.listener == nil {
				s.mu.Unlock()
				break
			}
			batch := s.pending[0]
			s.pending = s.pending[1:]
			listener := s.listener
			s.mu.Unlock()
			if err := listener(context.Background(), batch); err != nil {
				s.terminate(durable.WatchEnd{Reason: "listener_error", Err: err})
				return
			}
		}
	}
}

func (s *agentEventStream) terminate(end durable.WatchEnd) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.stopped = true
	if s.detach != nil {
		s.detach()
	}
	s.mu.Unlock()
	select {
	case s.closed <- end:
	default:
	}
}

func (s *agentEventStream) closeSession() {
	s.terminate(durable.WatchEnd{Reason: "session_closed"})
}

func event(typeName string, payload any) AgentEvent {
	return AgentEvent{Type: typeName, Payload: mustJSON(payload)}
}

func snapshotEvent(view *ConversationView) AgentEvent {
	live := view.Docs["pi.live"]
	payload := map[string]any{
		"entries": view.Entries,
		"tools":   []any{},
		"agent":   map[string]any{},
		"usage":   map[string]any{"models": map[string]any{}, "tools": map[string]any{}},
	}
	if live != nil {
		if run := liveRun(live); run != nil {
			payload["run"] = run
		}
		if generation := liveGeneration(live); generation != nil {
			payload["generation"] = generation
		}
		if tools, ok := live["tools"]; ok {
			payload["tools"] = tools
		}
		if compactions, ok := live["compactions"]; ok {
			payload["compactions"] = compactions
		}
	}
	if inbox, ok := view.Docs["pi.inbox"]; ok {
		payload["inbox"] = queuedItems(inbox)
	}
	if agent, ok := view.Docs["pi.agent"]; ok {
		payload["agent"] = agent
	}
	if usage, ok := view.Docs["pi.usage"]; ok {
		payload["usage"] = usage
	}
	return event("snapshot", payload)
}

func queuedItems(inbox durable.JsonObject) []any {
	items := inboxItems(inbox)
	out := make([]any, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, map[string]any{"id": record["id"], "mode": record["mode"]})
	}
	return out
}

// translateEvents derives the agent events of one commit.
func translateEvents(conversationID durable.ConversationID, before, after *ConversationView, publication durable.CommitPublication) []AgentEvent {
	var entries []durable.EntryRecord
	var tasks []durable.TaskRecord
	var submissions []durable.SubmissionRecord
	for _, change := range publication.Changes {
		switch change.Type {
		case durable.WriteEntry:
			if change.Write != nil && change.Write.Entry != nil && change.Write.Entry.ConversationID == conversationID {
				entries = append(entries, *change.Write.Entry)
			}
		case durable.WriteTask:
			if change.Write != nil && change.Write.Task != nil && change.Write.Task.ConversationID == conversationID {
				tasks = append(tasks, *change.Write.Task)
			}
		case durable.WriteSubmission:
			if change.Write != nil && change.Write.Submission != nil && change.Write.Submission.ConversationID == conversationID {
				submissions = append(submissions, *change.Write.Submission)
			}
		}
	}
	if len(entries) == 0 && len(tasks) == 0 && len(submissions) == 0 && before == after {
		return nil
	}
	var events []AgentEvent
	// Run lifecycle first.
	var runBefore, runAfter map[string]any
	if before != nil {
		if live, ok := before.Docs["pi.live"]; ok {
			runBefore = liveRun(live)
		}
	}
	if after != nil {
		if live, ok := after.Docs["pi.live"]; ok {
			runAfter = liveRun(live)
		}
	}
	runChanged := !jsonEqual(runBefore, runAfter)
	if runBefore != nil && runChanged {
		events = append(events, event("run_end", map[string]any{"inputs": runBefore["inputs"]}))
	}
	for _, task := range tasks {
		if task.Kind != generationTaskName {
			continue
		}
		if task.State.Outcome != nil && (task.State.Outcome.Status == durable.OutcomeFaulted || task.State.Outcome.Status == durable.OutcomeOrphaned) {
			message := task.State.Outcome.Reason
			if task.State.Outcome.Error != nil {
				message = task.State.Outcome.Error.Message
			}
			events = append(events, event("task_failed", map[string]any{"taskId": int64(task.ID), "kind": task.Kind, "message": message}))
		}
	}
	for _, entry := range entries {
		if len(entry.Model) > 0 {
			events = append(events, event("message_start", map[string]any{"message": entry.Model[0]}))
		}
		events = append(events, event("message_end", map[string]any{"entry": entry}))
	}
	for _, record := range submissions {
		events = append(events, event("submission", map[string]any{"record": record}))
	}
	if after != nil {
		if live, ok := after.Docs["pi.live"]; ok {
			if generation := liveGeneration(live); generation != nil {
				if retry, ok := generation["retry"]; ok {
					events = append(events, event("auto_retry_start", map[string]any{"attempt": generation["attempt"], "retry": retry}))
				}
				if deferred, ok := generation["deferred"]; ok {
					events = append(events, event("deferred_poll", deferred))
				}
			}
		}
	}
	// Document state changes.
	if after != nil {
		if inbox, ok := after.Docs["pi.inbox"]; ok {
			events = append(events, event("inbox_update", map[string]any{"items": queuedItems(inbox)}))
		}
		if agent, ok := after.Docs["pi.agent"]; ok {
			events = append(events, event("agent_changed", map[string]any{"agent": agent}))
		}
		if usage, ok := after.Docs["pi.usage"]; ok {
			events = append(events, event("usage_changed", map[string]any{"usage": usage}))
		}
	}
	if runAfter != nil && runChanged {
		events = append(events, event("run_start", map[string]any{"inputs": runAfter["inputs"]}))
	}
	return events
}

var _ AgentEventStream = (*agentEventStream)(nil)
