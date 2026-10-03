package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/env"
)

// Harness is the durable agent harness over one Session.
type Harness struct {
	storage   durable.Storage
	session   *durable.Session
	options   Options
	registry  RegistryReader
	models    ModelRunner
	now       func() int64
	report    func(error)
	scheduler *scheduler
	views     *viewManager
	graphs    *taskGraphManager

	mu         sync.Mutex
	closed     bool
	subWaiters *waiters[durable.SubmissionID, *durable.SubmissionRecord]
}

// Open opens a harness over storage.
func Open(ctx context.Context, storage durable.Storage, options Options) (*Harness, error) {
	if storage == nil {
		return nil, errors.New("harness.Open requires storage")
	}
	// An absent registry selects a fresh application registry holding exactly
	// the built-in tasks, matching the source default where a harness always has
	// a usable registry. Hosts may install extensions on the supplied registry.
	if options.Registry == nil {
		options.Registry = CreateRegistry()
	}
	snapshot := options.Registry.Snapshot()
	var missing []string
	for _, task := range BuiltinTasks() {
		if _, ok := snapshot.Task(task.Name); !ok {
			missing = append(missing, task.Name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("Registry lacks built-in tasks %v; create it with CreateRegistry()", missing)
	}
	models := options.Models
	if models == nil {
		models = unavailableModelRunner{}
	}
	report := options.OnReport
	if report == nil {
		report = func(error) {}
	}
	now := options.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	h := &Harness{
		storage:    storage,
		options:    options,
		registry:   options.Registry,
		models:     models,
		now:        now,
		report:     report,
		subWaiters: newWaiters[durable.SubmissionID, *durable.SubmissionRecord](),
	}
	h.session = durable.NewSession(storage)
	h.session.SetConversationCreated(h.conversationCreated)
	h.scheduler = newScheduler(h)
	h.views = newViewManager(h)
	h.graphs = newTaskGraphManager(h)
	h.session.SubscribeCommits(func(publication durable.CommitPublication) {
		for _, change := range publication.Changes {
			if change.Type != durable.WriteSubmission || change.Write == nil || change.Write.Submission == nil {
				continue
			}
			record := change.Write.Submission
			if isSettled(record) {
				h.subWaiters.resolve(record.ID, record)
			}
		}
		touched := map[durable.ConversationID]bool{}
		graphChanged := false
		for _, change := range publication.Changes {
			switch change.Type {
			case durable.WriteEntry:
				if change.Write != nil && change.Write.Entry != nil {
					touched[change.Write.Entry.ConversationID] = true
				}
			case durable.WriteConversation:
				if change.Write != nil && change.Write.Conversation != nil {
					touched[change.Write.Conversation.ID] = true
					graphChanged = true
				}
			case durable.WriteTask:
				if change.Write != nil && change.Write.Task != nil {
					touched[change.Write.Task.ConversationID] = true
					graphChanged = true
				}
			case durable.WriteSubmission:
				if change.Write != nil && change.Write.Submission != nil {
					touched[change.Write.Submission.ConversationID] = true
				}
			default:
				if change.ConversationID != nil {
					touched[*change.ConversationID] = true
				}
			}
		}
		for id := range touched {
			h.views.advanceConversation(context.Background(), id, publication)
		}
		if graphChanged {
			h.graphs.advance(context.Background())
		}
	})
	if err := h.scheduler.open(ctx); err != nil {
		h.session.Close(context.Background())
		return nil, err
	}
	return h, nil
}

func (h *Harness) settings() Settings {
	if h.options.Settings != nil {
		return h.options.Settings()
	}
	return DefaultSettings()
}

func (h *Harness) conversationCreated(tx *durable.Transaction, record durable.ConversationRecord) error {
	ctx := tx.Context()
	if _, err := tx.Doc(ctx, *LiveDoc, liveAddress(record.ID), nil); err != nil {
		return err
	}
	if _, err := tx.Doc(ctx, *InboxDoc, inboxAddress(record.ID), nil); err != nil {
		return err
	}
	if _, err := tx.Doc(ctx, *UsageDoc, usageAddress(record.ID), nil); err != nil {
		return err
	}
	if err := createAgent(ctx, tx, record); err != nil {
		return err
	}
	if h.options.ConversationCreated != nil {
		return h.options.ConversationCreated(ctx, tx, record)
	}
	return nil
}

// Resume enables task scheduling. It is idempotent; it rejects after close.
func (h *Harness) Resume() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return closedError()
	}
	h.scheduler.resume()
	return nil
}

// Close seals admission, joins callbacks and watches, and closes storage.
func (h *Harness) Close(ctx context.Context) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	h.scheduler.seal()
	h.scheduler.join()
	return h.session.Close(ctx)
}

// Commit runs one Session commit.
func (h *Harness) Commit(ctx context.Context, change func(durable.Tx) error) error {
	return h.session.Commit(ctx, change)
}

// CommitConversation runs one Session commit scoped to a conversation.
func (h *Harness) CommitConversation(ctx context.Context, conversationID durable.ConversationID, change func(*durable.Transaction) error) error {
	return h.session.CommitTransaction(ctx, durable.TransactionScope{ConversationID: &conversationID}, change)
}

// Snapshot reads one committed document.
func (h *Harness) Snapshot(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress) (durable.JsonObject, error) {
	return h.session.Snapshot(ctx, definition, address)
}

// SnapshotAsOf reads one document as of a visible entry.
func (h *Harness) SnapshotAsOf(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress, at durable.EntryID) (durable.JsonObject, error) {
	return h.session.SnapshotAsOf(ctx, definition, address, at)
}

// WatchDoc acquires one document watch.
func (h *Harness) WatchDoc(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress) (*durable.DocumentWatch, error) {
	return h.session.WatchDoc(ctx, definition, address)
}

// DocumentState acquires one read-only replicated document state.
func (h *Harness) DocumentState(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress) (*chord.AttachedState, error) {
	return h.session.DocumentState(ctx, definition, address)
}

// SubscribeCommits registers a commit publication listener.
func (h *Harness) SubscribeCommits(listener func(durable.CommitPublication)) func() {
	return h.session.SubscribeCommits(listener)
}

// SubscribeClose registers a close listener.
func (h *Harness) SubscribeClose(listener func()) func() {
	return h.session.SubscribeClose(listener)
}

// Root returns the reserved root conversation, creating it lazily.
func (h *Harness) Root(ctx context.Context, options RootOptions) (*Conversation, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, closedError()
	}
	h.mu.Unlock()
	var id durable.ConversationID
	err := h.session.CommitTransaction(ctx, durable.TransactionScope{}, func(tx *durable.Transaction) error {
		existing, err := tx.Conversation(ctx, durable.RootConversationID)
		if err != nil {
			return err
		}
		if existing != nil {
			id = existing.ID
			return nil
		}
		record, err := tx.CreateRootConversation(ctx)
		if err != nil {
			return err
		}
		if len(options.Agent) > 0 {
			if err := configure(ctx, tx, record.ID, options.Agent); err != nil {
				return err
			}
		}
		if options.Init != nil {
			if err := options.Init(ctx, tx, record.ID); err != nil {
				return err
			}
		}
		id = record.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Conversation{id: id, h: h}, nil
}

// Conversation returns a handle for an existing conversation, or nil.
func (h *Harness) Conversation(ctx context.Context, id durable.ConversationID) (*Conversation, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, closedError()
	}
	h.mu.Unlock()
	record, err := h.storage.Conversation(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	return &Conversation{id: record.ID, h: h}, nil
}

// CreateConversation creates an independent conversation.
func (h *Harness) CreateConversation(ctx context.Context, options CreateOptions) (*Conversation, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, closedError()
	}
	h.mu.Unlock()
	var id durable.ConversationID
	err := h.session.CommitTransaction(ctx, durable.TransactionScope{}, func(tx *durable.Transaction) error {
		record, err := tx.CreateConversation(ctx, options.Ownership)
		if err != nil {
			return err
		}
		if len(options.Agent) > 0 {
			if err := configure(ctx, tx, record.ID, options.Agent); err != nil {
				return err
			}
		}
		if options.Init != nil {
			if err := options.Init(ctx, tx, record.ID); err != nil {
				return err
			}
		}
		id = record.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Conversation{id: id, h: h}, nil
}

// GetTask returns the latest committed task record, or nil.
func (h *Harness) GetTask(ctx context.Context, id durable.TaskID) (*durable.TaskRecord, error) {
	return h.storage.Task(ctx, id)
}

// Inspect returns live tasks and unsettled submissions without running code.
func (h *Harness) Inspect(ctx context.Context) (Inspection, error) {
	scheduling, tasks := h.scheduler.inspect(h.registry.Snapshot())
	var submissions []durable.SubmissionRecord
	for _, status := range []string{durable.SubmissionStatusQueued, durable.SubmissionStatusPlaced} {
		records, err := scanAll(func(cursor durable.Cursor) (durable.Page[durable.SubmissionRecord], error) {
			return h.storage.ScanSubmissions(ctx, durable.SubmissionQuery{Status: status}, scanPageSize, cursor)
		})
		if err != nil {
			return Inspection{}, err
		}
		submissions = append(submissions, records...)
	}
	sortSubmissions(submissions)
	return Inspection{Scheduling: scheduling, Tasks: tasks, Submissions: submissions}, nil
}

func sortSubmissions(submissions []durable.SubmissionRecord) {
	for i := 1; i < len(submissions); i++ {
		for j := i; j > 0 && submissions[j-1].ID > submissions[j].ID; j-- {
			submissions[j-1], submissions[j] = submissions[j], submissions[j-1]
		}
	}
}

// Submission reacquires a submission handle.
func (h *Harness) Submission(ctx context.Context, id durable.SubmissionID) (*Submission, error) {
	record, err := h.storage.Submission(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	return &Submission{id: record.ID, h: h}, nil
}

// AbortSubmission aborts a submission by ID.
func (h *Harness) AbortSubmission(ctx context.Context, id durable.SubmissionID, conversationID durable.ConversationID) (string, error) {
	return h.abortSubmission(ctx, id, conversationID)
}

func (h *Harness) abortSubmission(ctx context.Context, id durable.SubmissionID, conversationID durable.ConversationID) (string, error) {
	var result string
	err := h.session.CommitTransaction(ctx, durable.TransactionScope{}, func(tx *durable.Transaction) error {
		current, err := h.storage.Submission(ctx, id)
		if err != nil {
			return err
		}
		if current == nil || (conversationID != 0 && current.ConversationID != conversationID) {
			result = "not_found"
			return nil
		}
		if current.Status == durable.SubmissionStatusQueued {
			if err := tx.SettleSubmission(id, durable.SubmissionSettlement{Status: durable.SubmissionStatusUnanswered, Reason: "aborted"}); err != nil {
				return err
			}
			if err := removeInboxItem(ctx, tx, current.ConversationID, id); err != nil {
				return err
			}
			result = "aborted"
			return nil
		}
		if current.Status == durable.SubmissionStatusPlaced {
			result = "already_placed"
			return nil
		}
		result = "settled"
		return nil
	})
	return result, err
}

// AbortTask commits an abort mark or settles an orphan.
func (h *Harness) AbortTask(ctx context.Context, id durable.TaskID) (string, error) {
	return h.scheduler.abort(id, ctx)
}

// WaitForTask resolves with the task's terminal receipt.
func (h *Harness) WaitForTask(ctx context.Context, id durable.TaskID) (*durable.TaskRecord, error) {
	h.scheduler.resume()
	return h.scheduler.waitForTask(id, ctx)
}

// WaitForIdle resolves when no ordinary non-background work is live.
func (h *Harness) WaitForIdle(ctx context.Context) error {
	h.scheduler.resume()
	return h.scheduler.waitForIdle(0, false, ctx)
}

// Usage sums every conversation's pi.usage.
func (h *Harness) Usage(ctx context.Context) (UsageState, error) {
	conversations, err := scanAll(func(cursor durable.Cursor) (durable.Page[durable.ConversationRecord], error) {
		return h.storage.ScanConversations(ctx, durable.ConversationQuery{}, scanPageSize, cursor)
	})
	if err != nil {
		return UsageState{}, err
	}
	total := UsageState{Models: map[string]types.Usage{}, Tools: map[string]types.Usage{}}
	for _, conversation := range conversations {
		state, err := h.session.Snapshot(ctx, *UsageDoc, usageAddress(conversation.ID))
		if err != nil {
			return UsageState{}, err
		}
		if state == nil {
			continue
		}
		addUsageState(total, decodeUsageState(state))
	}
	return total, nil
}

// TaskGraph returns a read-only task graph state.
func (h *Harness) TaskGraph(ctx context.Context) (*TaskGraphState, error) {
	return h.graphs.state(ctx)
}

// WatchTaskGraph returns a serialized task graph watch.
func (h *Harness) WatchTaskGraph(ctx context.Context) (*TaskGraphWatch, error) {
	return h.graphs.watch(ctx)
}

// conversationAgent resolves a conversation's agent against a registry snapshot.
func (h *Harness) conversationAgent(ctx context.Context, id durable.ConversationID, snapshot RegistrySnapshot) (Agent, error) {
	state, err := h.session.Snapshot(ctx, *AgentDoc, agentAddress(id))
	if err != nil {
		return Agent{}, err
	}
	return resolveAgent(state, snapshot, h.settings(), h.report), nil
}

func (h *Harness) buildEnv(ctx context.Context, id durable.ConversationID) (env.ExecutionEnv, error) {
	if h.options.Env == nil {
		return nil, nil
	}
	state, err := h.session.Snapshot(ctx, *AgentDoc, agentAddress(id))
	if err != nil {
		return nil, err
	}
	cwd := ""
	if state != nil {
		cwd = str(state["cwd"])
	}
	return h.options.Env(ctx, EnvTarget{ConversationID: id, CWD: cwd, Read: h.session})
}

func (h *Harness) submissionStatus(ctx context.Context, id durable.SubmissionID) (*durable.SubmissionRecord, error) {
	record, err := h.storage.Submission(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("Submission %d does not exist", id)
	}
	return record, nil
}

func (h *Harness) waitSubmission(ctx context.Context, id durable.SubmissionID) (*durable.SubmissionRecord, error) {
	h.scheduler.resume()
	var ch <-chan *durable.SubmissionRecord
	var cancel func()
	var settled *durable.SubmissionRecord
	// Check and register on the line so no settling publication falls between
	// them.
	err := h.session.ReadOnLine(ctx, func(tx *durable.Transaction) error {
		record, err := h.storage.Submission(ctx, id)
		if err != nil {
			return err
		}
		if record == nil {
			return fmt.Errorf("Submission %d does not exist", id)
		}
		if isSettled(record) {
			settled = record
			return nil
		}
		ch, cancel = h.subWaiters.add(id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if settled != nil {
		return settled, nil
	}
	defer cancel()
	select {
	case result := <-ch:
		return result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Conversation is a handle for one conversation.
type Conversation struct {
	id durable.ConversationID
	h  *Harness
}

// ID returns the conversation ID.
func (c *Conversation) ID() durable.ConversationID { return c.id }

// Agent resolves the conversation's agent.
func (c *Conversation) Agent(ctx context.Context) (Agent, error) {
	return c.h.conversationAgent(ctx, c.id, c.h.registry.Snapshot())
}

// Configure applies one agent change in its own commit.
func (c *Conversation) Configure(ctx context.Context, change json.RawMessage) error {
	return c.h.CommitConversation(ctx, c.id, func(tx *durable.Transaction) error {
		return configure(ctx, tx, c.id, change)
	})
}

// Submit durably admits a submission.
func (c *Conversation) Submit(ctx context.Context, draft SubmissionDraft) (*Submission, error) {
	return c.h.submit(ctx, c.id, draft)
}

// Reset admits a pi.reset write that starts a new context.
func (c *Conversation) Reset(ctx context.Context, handoff string) error {
	entry := durable.EntryDraft{Kind: entryKindReset, HeadSelf: true}
	if handoff != "" {
		entry.Model = []types.Message{types.NewUserMessageVariant(types.NewUserMessage(handoff, float64(c.h.now())))}
	}
	_, err := c.h.submit(ctx, c.id, SubmissionDraft{Type: durable.SubmissionTypeWrite, Entry: &entry})
	return err
}

// Compact admits a manual compaction task and returns its ID.
func (c *Conversation) Compact(ctx context.Context, instructions string) (durable.TaskID, error) {
	c.h.scheduler.resume()
	input := map[string]any{"reason": "manual"}
	if instructions != "" {
		input["instructions"] = instructions
	}
	var id durable.TaskID
	err := c.h.CommitConversation(ctx, c.id, func(tx *durable.Transaction) error {
		var err error
		id, err = createCompaction(ctx, tx, c.id, mustJSON(input), nil)
		return err
	})
	return id, err
}

// Commit runs one commit scoped to this conversation.
func (c *Conversation) Commit(ctx context.Context, change func(durable.Tx) error) error {
	return c.h.CommitConversation(ctx, c.id, func(tx *durable.Transaction) error { return change(tx) })
}

// Context returns the conversation's derived model context.
func (c *Conversation) Context(ctx context.Context) (ContextView, error) {
	return readContext(ctx, c.h.session, c.h.storage, c.id, nil)
}

// Entries scans the conversation's fork-aware history newest-first.
func (c *Conversation) Entries(ctx context.Context, query durable.EntryQuery, limit int, cursor durable.Cursor) (durable.Page[durable.EntryRecord], error) {
	query.ConversationID = c.id
	return c.h.storage.ScanEntries(ctx, query, limit, cursor)
}

// Fork forks the conversation at a concrete visible entry.
func (c *Conversation) Fork(ctx context.Context, at durable.EntryID, options CreateOptions) (*Conversation, error) {
	var id durable.ConversationID
	err := c.h.session.CommitTransaction(ctx, durable.TransactionScope{ConversationID: &c.id}, func(tx *durable.Transaction) error {
		record, err := tx.ForkConversation(ctx, c.id, at, options.Ownership)
		if err != nil {
			return err
		}
		if len(options.Agent) > 0 {
			if err := configure(ctx, tx, record.ID, options.Agent); err != nil {
				return err
			}
		}
		if options.Init != nil {
			if err := options.Init(ctx, tx, record.ID); err != nil {
				return err
			}
		}
		id = record.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Conversation{id: id, h: c.h}, nil
}

// Abort withdraws queued inputs and marks the ordinary ownership scope.
func (c *Conversation) Abort(ctx context.Context, options AbortOptions) error {
	c.h.scheduler.resume()
	return c.h.scheduler.abortConversation(c.id, options.Background, ctx)
}

// WaitForIdle resolves when the conversation's ordinary scope is idle.
func (c *Conversation) WaitForIdle(ctx context.Context) error {
	c.h.scheduler.resume()
	return c.h.scheduler.waitForIdle(c.id, true, ctx)
}

// ViewState returns the structural view as a read-only state.
func (c *Conversation) ViewState(ctx context.Context) (*ConversationState, error) {
	return c.h.views.state(ctx, c.id)
}

// Watch returns the structural view as a serialized exact-frame watch.
func (c *Conversation) Watch(ctx context.Context) (*ConversationWatch, error) {
	return c.h.views.watch(ctx, c.id)
}

// UnavailableModelRunner reports that no model runner is configured.
type unavailableModelRunner struct{}

func (unavailableModelRunner) Resolve(context.Context, ModelRef) (*types.Model, error) {
	return nil, errors.New("no model runner is configured")
}

func (unavailableModelRunner) Run(context.Context, ModelRequest, func(types.AssistantMessage) error) (types.AssistantMessage, error) {
	return types.AssistantMessage{}, errors.New("no model runner is configured")
}

// ─── Task runtime, tool API and bound conversation (merged from the harness
// runtime unit into this file to keep the declared output set). ───

// taskRuntime is the scheduler's extended task runtime used by the built-in
// generation, tool and compaction tasks.
type taskRuntime interface {
	durable.TaskRuntime
	durable.DocumentReader
	TaskID() durable.TaskID
	ConversationID() durable.ConversationID
	agent(context.Context) (Agent, error)
	settings() Settings
	modelRunner() ModelRunner
	environment(context.Context) (env.ExecutionEnv, error)
	registrySnapshot() RegistrySnapshot
	contextView(context.Context, durable.ConversationID, *durable.EntryID) (ContextView, error)
	entryOf(context.Context, string, durable.EntryID) (*durable.EntryRecord, error)
	snapshotDoc(context.Context, durable.DocumentDefinition, durable.ConversationID) (durable.JsonObject, error)
	eachHook(ctx context.Context, taskName, name string, invoke func(HookHandler) error) error
}

type taskRuntimeImpl struct {
	s        *scheduler
	inv      *invocation
	snapshot RegistrySnapshot
	task     durable.TaskDefinition
}

func (r *taskRuntimeImpl) TaskID() durable.TaskID                 { return r.inv.taskID }
func (r *taskRuntimeImpl) ConversationID() durable.ConversationID { return r.inv.conversationID }
func (r *taskRuntimeImpl) Now() int64                             { return r.s.now() }
func (r *taskRuntimeImpl) settings() Settings                     { return r.s.h.settings() }
func (r *taskRuntimeImpl) modelRunner() ModelRunner               { return r.s.models }
func (r *taskRuntimeImpl) registrySnapshot() RegistrySnapshot     { return r.snapshot }

func (r *taskRuntimeImpl) Report(err error) {
	if err == nil || r.inv.isEnded() {
		return
	}
	r.s.report(err)
}

func (r *taskRuntimeImpl) agent(ctx context.Context) (Agent, error) {
	if r.inv.isEnded() {
		return Agent{}, endedError(r.inv)
	}
	return r.s.h.conversationAgent(ctx, r.inv.conversationID, r.snapshot)
}

func (r *taskRuntimeImpl) environment(ctx context.Context) (env.ExecutionEnv, error) {
	if r.inv.isEnded() {
		return nil, endedError(r.inv)
	}
	return r.s.h.buildEnv(ctx, r.inv.conversationID)
}

func (r *taskRuntimeImpl) contextView(ctx context.Context, conversationID durable.ConversationID, at *durable.EntryID) (ContextView, error) {
	if r.inv.isEnded() {
		return ContextView{}, endedError(r.inv)
	}
	return readContext(ctx, r.s.h.session, r.s.h.storage, conversationID, at)
}

func (r *taskRuntimeImpl) entryOf(ctx context.Context, kind string, id durable.EntryID) (*durable.EntryRecord, error) {
	stored, err := r.s.h.storage.VisibleEntry(ctx, r.inv.conversationID, id)
	if err != nil {
		return nil, err
	}
	if stored == nil || stored.Entry.Kind != kind {
		return nil, nil
	}
	entry := stored.Entry
	return &entry, nil
}

func (r *taskRuntimeImpl) snapshotDoc(ctx context.Context, definition durable.DocumentDefinition, conversationID durable.ConversationID) (durable.JsonObject, error) {
	return r.s.h.session.Snapshot(ctx, definition, durable.ConversationAddress(&definition, conversationID, nil))
}

func (r *taskRuntimeImpl) eachHook(ctx context.Context, taskName, name string, invoke func(HookHandler) error) error {
	for _, set := range agentHooks(ctx, r, taskName) {
		handler, ok := set[name]
		if !ok {
			continue
		}
		if err := invoke(handler); err != nil {
			if r.inv.ctx.Err() != nil {
				return err
			}
			r.s.report(err)
		}
	}
	return nil
}

func (r *taskRuntimeImpl) Commit(ctx context.Context, change func(durable.Tx, *durable.TaskRecord) error) error {
	if r.inv.isEnded() {
		return endedError(r.inv)
	}
	err := r.s.session.CommitTransaction(ctx, durable.TransactionScope{
		ConversationID: &r.inv.conversationID,
		TaskID:         &r.inv.taskID,
	}, func(tx *durable.Transaction) error {
		if r.inv.isEnded() {
			return endedError(r.inv)
		}
		r.s.mu.Lock()
		defer r.s.mu.Unlock()
		if r.s.closing {
			return closedError()
		}
		found := r.s.live[r.inv.taskID]
		if found == nil {
			return fmt.Errorf("Task %d is terminal", r.inv.taskID)
		}
		if found.State.Status != durable.TaskStatusRunning {
			return fmt.Errorf("Task %d is %s", r.inv.taskID, found.State.Status)
		}
		if r.inv.mode == "run" && found.AbortRequested {
			return fmt.Errorf("Task %d has a durable abort mark", r.inv.taskID)
		}
		current := cloneTask(*found)
		if err := change(tx, &current); err != nil {
			return err
		}
		return r.s.commitState(tx, r.inv, current)
	})
	return err
}

func (r *taskRuntimeImpl) Memo(ctx context.Context, name string, candidate json.RawMessage) (json.RawMessage, error) {
	if r.inv.isEnded() {
		return nil, endedError(r.inv)
	}
	if candidate == nil {
		r.s.mu.Lock()
		defer r.s.mu.Unlock()
		record := r.s.live[r.inv.taskID]
		if record == nil || record.Memos == nil {
			return nil, nil
		}
		value, ok := record.Memos[name]
		if !ok {
			return nil, nil
		}
		return value, nil
	}
	var winner json.RawMessage
	err := r.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		if current.Memos != nil {
			if existing, ok := current.Memos[name]; ok {
				winner = existing
				return nil
			}
		}
		if current.Memos == nil {
			current.Memos = map[string]json.RawMessage{}
		}
		current.Memos[name] = candidate
		winner = candidate
		return nil
	})
	if err != nil {
		return nil, err
	}
	return winner, nil
}

func (r *taskRuntimeImpl) GetTask(ctx context.Context, id durable.TaskID) (*durable.TaskRecord, error) {
	if r.inv.isEnded() {
		return nil, endedError(r.inv)
	}
	return r.s.h.storage.Task(ctx, id)
}

func (r *taskRuntimeImpl) WaitForTask(ctx context.Context, id durable.TaskID) (durable.TaskOutcome, error) {
	if r.inv.isEnded() {
		return durable.TaskOutcome{}, endedError(r.inv)
	}
	record, err := r.s.waitForTask(id, ctx)
	if err != nil {
		return durable.TaskOutcome{}, err
	}
	if record.State.Status != durable.TaskStatusTerminal || record.State.Outcome == nil {
		return durable.TaskOutcome{}, fmt.Errorf("Task %d is not terminal", id)
	}
	return *record.State.Outcome, nil
}

func (r *taskRuntimeImpl) Outcomes(ctx context.Context, ids []durable.TaskID) ([]durable.TaskOutcome, error) {
	if r.inv.isEnded() {
		return nil, endedError(r.inv)
	}
	outcomes := make([]durable.TaskOutcome, 0, len(ids))
	for _, id := range ids {
		record, err := r.s.h.storage.Task(ctx, id)
		if err != nil {
			return nil, err
		}
		if record == nil || record.State.Status != durable.TaskStatusTerminal || record.State.Outcome == nil {
			return nil, fmt.Errorf("Task %d is not terminal", id)
		}
		outcomes = append(outcomes, *record.State.Outcome)
	}
	return outcomes, nil
}

func (r *taskRuntimeImpl) Sleep(ctx context.Context, until int64) error {
	for {
		if r.inv.isEnded() {
			return endedError(r.inv)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := until - r.s.now()
		if remaining <= 0 {
			return nil
		}
		timer := time.NewTimer(time.Duration(remaining) * time.Millisecond)
		select {
		case <-timer.C:
		case <-r.inv.ctx.Done():
			timer.Stop()
			return endedError(r.inv)
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

func endedError(inv *invocation) error {
	return fmt.Errorf("Task %d invocation has ended", inv.taskID)
}

func cloneTask(record durable.TaskRecord) durable.TaskRecord {
	data, err := json.Marshal(record)
	if err != nil {
		return record
	}
	var clone durable.TaskRecord
	if err := json.Unmarshal(data, &clone); err != nil {
		return record
	}
	return clone
}

// asEnv converts an ExecutionEnv-like value.
func asEnv(value interface{}) env.ExecutionEnv {
	if value == nil {
		return nil
	}
	if executionEnv, ok := value.(env.ExecutionEnv); ok {
		return executionEnv
	}
	return nil
}

// toolAPI is the invocation-bound tool execution surface.
type toolAPI struct {
	runtime  taskRuntime
	call     types.ToolCall
	reported *reportedOutput
	progress *Progress
	execEnv  env.ExecutionEnv
	ended    bool
}

func (a *toolAPI) markEnded() { a.ended = true }

func (a *toolAPI) assertLive() error {
	if a.ended {
		return fmt.Errorf("tool call %s has settled", a.call.Id)
	}
	return nil
}

func (a *toolAPI) Snapshot(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress) (durable.JsonObject, error) {
	return a.runtime.(*taskRuntimeImpl).snapshotRaw(ctx, definition, address)
}

func (a *toolAPI) SnapshotAsOf(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress, at durable.EntryID) (durable.JsonObject, error) {
	return a.runtime.(*taskRuntimeImpl).snapshotAsOfRaw(ctx, definition, address, at)
}

func (a *toolAPI) WatchDoc(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress) (*durable.DocumentWatch, error) {
	return a.runtime.(*taskRuntimeImpl).watchRaw(ctx, definition, address)
}

func (a *toolAPI) TaskID() durable.TaskID                 { return a.runtime.TaskID() }
func (a *toolAPI) ConversationID() durable.ConversationID { return a.runtime.ConversationID() }
func (a *toolAPI) CallID() string                         { return a.call.Id }
func (a *toolAPI) Registry() RegistrySnapshot             { return a.runtime.registrySnapshot() }

func (a *toolAPI) Agent(ctx context.Context) (Agent, error) { return a.runtime.agent(ctx) }

func (a *toolAPI) Env() env.ExecutionEnv { return a.execEnv }

func (a *toolAPI) Output(chunk []byte) error {
	if err := a.assertLive(); err != nil {
		return err
	}
	if a.reported.output.Push(chunk) {
		a.progress.Mark()
	}
	return nil
}

func (a *toolAPI) Diagnostic(diagnostic ToolDiagnostic) error {
	if err := a.assertLive(); err != nil {
		return err
	}
	a.reported.diagnostics = append(a.reported.diagnostics, diagnostic)
	a.progress.Mark()
	return nil
}

func (a *toolAPI) Details(ctx context.Context, value json.RawMessage) error {
	if err := a.assertLive(); err != nil {
		return err
	}
	a.reported.details = value
	return a.progress.MarkAndWait()
}

func (a *toolAPI) Commit(ctx context.Context, change func(durable.Tx) error) error {
	if err := a.assertLive(); err != nil {
		return err
	}
	return a.runtime.Commit(ctx, func(tx durable.Tx, _ *durable.TaskRecord) error { return change(tx) })
}

func (a *toolAPI) Memo(ctx context.Context, name string, candidate json.RawMessage) (json.RawMessage, error) {
	return a.runtime.Memo(ctx, name, candidate)
}

func (a *toolAPI) CreateTask(ctx context.Context, definition durable.TaskDefinition, input json.RawMessage, options durable.TaskOptions) (durable.TaskID, error) {
	var id durable.TaskID
	err := a.runtime.Commit(ctx, func(tx durable.Tx, _ *durable.TaskRecord) error {
		var err error
		id, err = tx.CreateTask(ctx, definition, input, options)
		return err
	})
	return id, err
}

func (a *toolAPI) GetTask(ctx context.Context, id durable.TaskID) (*durable.TaskRecord, error) {
	return a.runtime.GetTask(ctx, id)
}

func (a *toolAPI) WaitForTask(ctx context.Context, id durable.TaskID) (*durable.TaskRecord, error) {
	if err := a.assertLive(); err != nil {
		return nil, err
	}
	return a.runtime.(*taskRuntimeImpl).s.waitForTask(id, ctx)
}

func (a *toolAPI) Conversation(ctx context.Context, id durable.ConversationID) (ConversationHandle, error) {
	record, err := a.runtime.(*taskRuntimeImpl).s.h.storage.Conversation(ctx, id)
	if err != nil || record == nil {
		return nil, err
	}
	return &boundConversation{id: id, h: a.runtime.(*taskRuntimeImpl).s.h}, nil
}

func (r *taskRuntimeImpl) snapshotRaw(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress) (durable.JsonObject, error) {
	return r.s.h.session.Snapshot(ctx, definition, address)
}

// Snapshot reads one committed document.
func (r *taskRuntimeImpl) Snapshot(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress) (durable.JsonObject, error) {
	return r.s.h.session.Snapshot(ctx, definition, address)
}

// SnapshotAsOf reads one document as of a visible entry.
func (r *taskRuntimeImpl) SnapshotAsOf(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress, at durable.EntryID) (durable.JsonObject, error) {
	return r.s.h.session.SnapshotAsOf(ctx, definition, address, at)
}

func (r *taskRuntimeImpl) snapshotAsOfRaw(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress, at durable.EntryID) (durable.JsonObject, error) {
	return r.s.h.session.SnapshotAsOf(ctx, definition, address, at)
}

func (r *taskRuntimeImpl) watchRaw(ctx context.Context, definition durable.DocumentDefinition, address durable.DocumentAddress) (*durable.DocumentWatch, error) {
	return r.s.h.session.WatchDoc(ctx, definition, address)
}

// boundConversation is an invocation-bound conversation handle.
type boundConversation struct {
	id durable.ConversationID
	h  *Harness
}

func (b *boundConversation) ID() durable.ConversationID { return b.id }

func (b *boundConversation) Submit(ctx context.Context, draft SubmissionDraft) (*Submission, error) {
	if draft.Type != durable.SubmissionTypeInput {
		return nil, errors.New("conversation handle accepts input submissions only")
	}
	return b.h.submit(ctx, b.id, draft)
}

func (b *boundConversation) Abort(ctx context.Context, options AbortOptions) error {
	return b.h.scheduler.abortConversation(b.id, options.Background, ctx)
}

func (b *boundConversation) WaitForIdle(ctx context.Context) error {
	return b.h.scheduler.waitForIdle(b.id, true, ctx)
}

var _ taskRuntime = (*taskRuntimeImpl)(nil)
var _ ToolAPI = (*toolAPI)(nil)
var _ HookAPI = (*taskRuntimeImpl)(nil)
var _ ConversationHandle = (*boundConversation)(nil)
