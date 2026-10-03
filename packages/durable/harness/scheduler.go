package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/minifish-org/pith/packages/durable"
)

var runnableStatuses = []string{
	durable.TaskStatusPending,
	durable.TaskStatusRunning,
	durable.TaskStatusWaiting,
	durable.TaskStatusCompleting,
}

// taskNode is the immutable ownership fields of a task.
type taskNode struct {
	conversationID durable.ConversationID
	owner          *durable.TaskID
	background     bool
}

type failedMigration struct {
	task durable.TaskDefinition
	err  error
}

// invocation is one in-memory execution of a task.
type invocation struct {
	taskID         durable.TaskID
	conversationID durable.ConversationID
	mode           string
	ctx            context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	ended          bool
	closed         bool
	done           chan struct{}
	snapshot       RegistrySnapshot
}

func (i *invocation) isEnded() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.ended
}

// markEnded marks the invocation ended without cancelling its context, so a
// commit already inside the mutation line still settles. end() also cancels.
func (i *invocation) markEnded() {
	i.mu.Lock()
	i.ended = true
	i.mu.Unlock()
}

func (i *invocation) end() {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return
	}
	i.closed = true
	i.ended = true
	i.mu.Unlock()
	i.cancel()
	close(i.done)
}

type reservation struct {
	inv    *invocation
	task   durable.TaskDefinition
	record durable.TaskRecord
}

// scheduler is the durable task scheduler of one harness.
type scheduler struct {
	h        *Harness
	session  *durable.Session
	storage  durable.Storage
	registry RegistryReader
	models   ModelRunner
	now      func() int64
	report   func(error)
	ctx      context.Context

	mu               sync.Mutex
	live             map[durable.TaskID]*durable.TaskRecord
	settled          map[durable.TaskID]taskNode
	failed           map[durable.TaskID]bool
	invocations      map[durable.TaskID]*invocation
	failedMigrations map[durable.TaskID]failedMigration
	taskWaiters      *waiters[durable.TaskID, *durable.TaskRecord]
	idleWaiters      *waiters[string, struct{}]
	enabled          bool
	closing          bool
	dirty            bool
	draining         bool
	unsubscribeReg   func()
}

func newScheduler(h *Harness) *scheduler {
	return &scheduler{
		h:                h,
		session:          h.session,
		storage:          h.storage,
		registry:         h.registry,
		models:           h.models,
		now:              h.now,
		report:           h.report,
		ctx:              context.Background(),
		live:             map[durable.TaskID]*durable.TaskRecord{},
		settled:          map[durable.TaskID]taskNode{},
		failed:           map[durable.TaskID]bool{},
		invocations:      map[durable.TaskID]*invocation{},
		failedMigrations: map[durable.TaskID]failedMigration{},
		taskWaiters:      newWaiters[durable.TaskID, *durable.TaskRecord](),
		idleWaiters:      newWaiters[string, struct{}](),
	}
}

func (s *scheduler) open(ctx context.Context) error {
	s.session.SubscribeCommits(func(publication durable.CommitPublication) { s.observe(publication) })
	s.session.SubscribeClose(func() { s.seal() })
	s.unsubscribeReg = s.registry.Subscribe(func() { s.kick() })
	return s.session.CommitTransaction(ctx, durable.TransactionScope{}, func(tx *durable.Transaction) error {
		var pending []durable.TaskRecord
		for _, status := range runnableStatuses {
			records, err := scanAll(func(cursor durable.Cursor) (durable.Page[durable.TaskRecord], error) {
				return tx.ScanTasks(ctx, durable.TaskQuery{Status: status}, scanPageSize, cursor)
			})
			if err != nil {
				return err
			}
			for i := range records {
				record := records[i]
				s.live[record.ID] = &record
				if record.State.Status == durable.TaskStatusRunning {
					pending = append(pending, record)
				}
			}
		}
		for _, record := range pending {
			if err := tx.SetTask(withState(record, durable.TaskState{Status: durable.TaskStatusPending, Checkpoint: record.State.Checkpoint})); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *scheduler) resume() {
	s.mu.Lock()
	s.enabled = true
	s.mu.Unlock()
	s.kick()
}

func (s *scheduler) join() {
	s.mu.Lock()
	invs := make([]*invocation, 0, len(s.invocations))
	for _, inv := range s.invocations {
		invs = append(invs, inv)
	}
	s.mu.Unlock()
	for _, inv := range invs {
		<-inv.done
	}
}

func (s *scheduler) seal() {
	s.mu.Lock()
	s.closing = true
	if s.unsubscribeReg != nil {
		s.unsubscribeReg()
	}
	invs := make([]*invocation, 0, len(s.invocations))
	for _, inv := range s.invocations {
		invs = append(invs, inv)
	}
	s.mu.Unlock()
	s.taskWaiters.resolveAll(nil)
	s.idleWaiters.resolveAll(struct{}{})
	for _, inv := range invs {
		inv.cancel()
	}
}

func (s *scheduler) observe(publication durable.CommitPublication) {
	s.mu.Lock()
	changed := false
	for _, change := range publication.Changes {
		if change.Type != durable.WriteTask || change.Write == nil || change.Write.Task == nil {
			continue
		}
		changed = true
		record := *change.Write.Task
		previous := s.live[record.ID]
		if record.State.Status == durable.TaskStatusTerminal {
			delete(s.live, record.ID)
			delete(s.failedMigrations, record.ID)
			s.settled[record.ID] = nodeOf(record)
			if record.State.Outcome != nil && record.State.Outcome.Status != durable.OutcomeCompleted {
				s.failed[record.ID] = true
			} else {
				delete(s.failed, record.ID)
			}
			recordCopy := record
			s.taskWaiters.resolve(record.ID, &recordCopy)
			continue
		}
		if record.AbortRequested && (previous == nil || !previous.AbortRequested) {
			if inv := s.invocations[record.ID]; inv != nil && inv.mode == "run" {
				inv.cancel()
			}
		}
		s.live[record.ID] = &record
	}
	for _, key := range s.idleWaiters.keys() {
		if s.idleLocked(key) {
			s.idleWaiters.resolve(key, struct{}{})
		}
	}
	s.mu.Unlock()
	if changed {
		s.kick()
	}
}

func (s *scheduler) idleLocked(conversation string) bool {
	for _, record := range s.live {
		if record.Background {
			continue
		}
		if conversation == "" || fmt.Sprint(int64(record.ConversationID)) == conversation {
			return false
		}
	}
	return true
}

func (s *scheduler) kick() {
	s.mu.Lock()
	s.dirty = true
	if s.draining || !s.enabled || s.closing {
		s.mu.Unlock()
		return
	}
	s.draining = true
	s.mu.Unlock()
	go s.drain()
}

func (s *scheduler) drain() {
	for {
		s.mu.Lock()
		run := s.dirty && s.enabled && !s.closing
		if run {
			s.dirty = false
		}
		s.mu.Unlock()
		if !run {
			break
		}
		if err := s.reconcile(); err != nil {
			if !s.isClosing() {
				s.report(err)
			}
		}
		reservations, err := s.reserve()
		if err != nil {
			if !s.isClosing() {
				s.report(err)
			}
			continue
		}
		for _, res := range reservations {
			s.start(res)
		}
	}
	s.mu.Lock()
	s.draining = false
	dirty := s.dirty
	s.mu.Unlock()
	if dirty {
		s.kick()
	}
}

func (s *scheduler) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

// overlay is a snapshot of the task nodes and records visible during one
// commit: committed live tasks overlaid with the commit's staged candidates.
type overlay struct {
	nodes   map[durable.TaskID]taskNode
	records map[durable.TaskID]*durable.TaskRecord
}

func (s *scheduler) overlayLocked(tx *durable.Transaction) overlay {
	o := overlay{nodes: map[durable.TaskID]taskNode{}, records: map[durable.TaskID]*durable.TaskRecord{}}
	for id, node := range s.settled {
		o.nodes[id] = node
	}
	for id, record := range s.live {
		o.nodes[id] = nodeOf(*record)
		o.records[id] = record
	}
	if tx != nil {
		for _, record := range tx.StagedTasks() {
			o.nodes[record.ID] = nodeOf(*record)
			o.records[record.ID] = record
		}
	}
	return o
}

func (s *scheduler) reserve() ([]*reservation, error) {
	var reservations []*reservation
	err := s.session.CommitTransaction(s.ctx, durable.TransactionScope{}, func(tx *durable.Transaction) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.enabled || s.closing {
			return nil
		}
		o := s.overlayLocked(tx)
		owned := ownedLive(o)
		ids := make([]durable.TaskID, 0, len(s.live))
		for id := range s.live {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
		var snapshot RegistrySnapshot
		for _, id := range ids {
			record := s.live[id]
			if record == nil {
				continue
			}
			if _, ok := s.invocations[id]; ok {
				continue
			}
			if len(waitingOn(record, o, owned)) > 0 {
				continue
			}
			if record.State.Status == durable.TaskStatusCompleting {
				continue
			}
			mode := "run"
			if record.AbortRequested {
				mode = "abort"
			}
			if snapshot == nil {
				snapshot = s.registry.Snapshot()
			}
			res := s.resolveLocked(*record, snapshot)
			if res.blocked {
				if mode == "abort" {
					if err := s.terminate(tx, o, *record, durable.TaskOutcome{Status: durable.OutcomeOrphaned, Reason: res.reason}); err != nil {
						return err
					}
				}
				continue
			}
			if err := tx.SetTask(withState(res.record, durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: res.record.State.Checkpoint})); err != nil {
				return err
			}
			inv := s.createInvocationLocked(record.ID, record.ConversationID, mode, snapshot)
			reservations = append(reservations, &reservation{inv: inv, task: res.task, record: res.record})
		}
		return nil
	})
	if err != nil {
		for _, res := range reservations {
			res.inv.end()
		}
		return nil, err
	}
	return reservations, nil
}

type resolution struct {
	blocked bool
	reason  string
	task    durable.TaskDefinition
	record  durable.TaskRecord
}

func (s *scheduler) resolveLocked(record durable.TaskRecord, snapshot RegistrySnapshot) resolution {
	task, ok := snapshot.Task(record.Kind)
	if !ok {
		return resolution{blocked: true, reason: "missing_task", record: record}
	}
	if task.Version == record.Version {
		return resolution{task: task, record: record}
	}
	if task.Version < record.Version {
		return resolution{blocked: true, reason: "task_too_old", record: record}
	}
	if failed, ok := s.failedMigrations[record.ID]; ok && failed.task.Name == task.Name && failed.task.Version == task.Version {
		return resolution{blocked: true, reason: "migration_failed", record: record}
	}
	if task.Migrate == nil {
		err := fmt.Errorf("Task %s version %d has no migration from %d", record.Kind, task.Version, record.Version)
		s.failedMigrations[record.ID] = failedMigration{task: task, err: err}
		s.report(err)
		return resolution{blocked: true, reason: "migration_failed", record: record}
	}
	migratedInput, migratedCheckpoint, err := task.Migrate(record.Input, record.State.Checkpoint, record.Version)
	if err != nil {
		s.failedMigrations[record.ID] = failedMigration{task: task, err: err}
		s.report(err)
		return resolution{blocked: true, reason: "migration_failed", record: record}
	}
	updated := record
	updated.Version = task.Version
	updated.Input = migratedInput
	updated.State.Checkpoint = migratedCheckpoint
	return resolution{task: task, record: updated}
}

// reconcile applies what committed records imply: failFast marks for a failed
// member of a failFast join, and the terminal record of every completing task
// whose ordinary owned work is gone.
func (s *scheduler) reconcile() error {
	return s.session.CommitTransaction(s.ctx, durable.TransactionScope{}, func(tx *durable.Transaction) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.closing || !s.enabled {
			return nil
		}
		for _, waiter := range s.live {
			if waiter.State.Status != durable.TaskStatusWaiting || waiter.State.Policy != durable.PolicyFailFast {
				continue
			}
			failedSeen := false
			for _, member := range waiter.State.On {
				if s.failed[member] {
					failedSeen = true
					break
				}
			}
			if !failedSeen {
				continue
			}
			for _, member := range waiter.State.On {
				record := s.live[member]
				if record == nil || s.failed[member] || record.AbortRequested {
					continue
				}
				updated := *record
				updated.AbortRequested = true
				if err := tx.SetTask(withState(updated, updated.State)); err != nil {
					return err
				}
			}
		}
		o := s.overlayLocked(tx)
		owned := ownedLive(o)
		var done []durable.TaskRecord
		for _, record := range s.live {
			if record.State.Status == durable.TaskStatusCompleting && owned[record.ID] == nil {
				done = append(done, *record)
			}
		}
		for _, record := range done {
			if err := s.terminateTerminal(tx, record, derefOutcome(record.State.Outcome)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *scheduler) createInvocationLocked(taskID durable.TaskID, conversationID durable.ConversationID, mode string, snapshot RegistrySnapshot) *invocation {
	ctx, cancel := context.WithCancel(s.ctx)
	inv := &invocation{taskID: taskID, conversationID: conversationID, mode: mode, ctx: ctx, cancel: cancel, done: make(chan struct{}), snapshot: snapshot}
	s.invocations[taskID] = inv
	return inv
}

func (s *scheduler) start(res *reservation) {
	go func() {
		defer func() {
			res.inv.end()
			s.mu.Lock()
			if s.invocations[res.inv.taskID] == res.inv {
				delete(s.invocations, res.inv.taskID)
			}
			s.mu.Unlock()
			s.kick()
		}()
		defer func() {
			if r := recover(); r != nil {
				s.report(fmt.Errorf("task %d panicked: %v", res.inv.taskID, r))
			}
		}()
		if res.inv.mode == "run" {
			s.run(res)
		} else {
			s.runAbort(res)
		}
	}()
}

type phaseResult struct {
	checkpoint []byte
	err        error
}

type runState struct {
	task     durable.TaskDefinition
	snapshot RegistrySnapshot
}

func (s *scheduler) run(res *reservation) {
	inv := res.inv
	runtime := &taskRuntimeImpl{s: s, inv: inv, snapshot: inv.snapshot, task: res.task}
	state := &runState{task: res.task, snapshot: inv.snapshot}
	var previous *phaseResult
	for {
		current, ok := s.step(inv, func(tx *durable.Transaction, record durable.TaskRecord) decision {
			return s.decide(tx, record, previous, state)
		})
		if !ok || s.isClosing() {
			return
		}
		checkpoint := current.State.Checkpoint
		phase := phaseOf(checkpoint)
		handler, ok := state.task.Phases[phase]
		if !ok || handler == nil {
			previous = &phaseResult{checkpoint: checkpoint, err: fmt.Errorf("Task %s has no phase %q", current.Kind, phase)}
			continue
		}
		runtime.snapshot = state.snapshot
		runtime.task = state.task
		err := handler(inv.ctx, current, runtime)
		previous = &phaseResult{checkpoint: checkpoint, err: err}
	}
}

func (s *scheduler) runAbort(res *reservation) {
	inv := res.inv
	s.mu.Lock()
	record := s.live[inv.taskID]
	s.mu.Unlock()
	if record == nil || s.isClosing() {
		return
	}
	runtime := &taskRuntimeImpl{s: s, inv: inv, snapshot: inv.snapshot, task: res.task}
	var failure error
	if res.task.Abort == nil {
		failure = fmt.Errorf("Task %s has no abort handler", res.task.Name)
	} else {
		failure = res.task.Abort(inv.ctx, *record, runtime)
	}
	_, _ = s.step(inv, func(tx *durable.Transaction, current durable.TaskRecord) decision {
		if failure != nil {
			return decision{fault: failure}
		}
		return decision{fault: fmt.Errorf("Abort handler of task %d returned without a terminal outcome", inv.taskID)}
	})
}

type decision struct {
	cont  bool
	fault error
}

func (s *scheduler) decide(tx *durable.Transaction, current durable.TaskRecord, previous *phaseResult, state *runState) decision {
	if current.AbortRequested {
		return decision{}
	}
	if previous == nil {
		return decision{cont: true}
	}
	if previous.err != nil {
		return decision{fault: previous.err}
	}
	if jsonEqual(rawValue(current.State.Checkpoint), rawValue(previous.checkpoint)) {
		return decision{fault: fmt.Errorf("Task %s phase %s returned without durable progress", current.Kind, phaseOf(previous.checkpoint))}
	}
	state.snapshot = s.registry.Snapshot()
	next, ok := state.snapshot.Task(current.Kind)
	if !ok {
		return decision{fault: fmt.Errorf("Task %d lost its %s definition", current.ID, current.Kind)}
	}
	if next.Version != state.task.Version {
		if canReserve(next, current) {
			_ = tx.SetTask(withState(current, durable.TaskState{Status: durable.TaskStatusPending, Checkpoint: current.State.Checkpoint}))
			return decision{}
		}
		return decision{cont: true}
	}
	return decision{cont: true}
}

func canReserve(task durable.TaskDefinition, record durable.TaskRecord) bool {
	return task.Version == record.Version || (task.Version > record.Version && task.Migrate != nil)
}

func (s *scheduler) step(inv *invocation, decide func(*durable.Transaction, durable.TaskRecord) decision) (durable.TaskRecord, bool) {
	var current durable.TaskRecord
	var cont bool
	err := s.session.CommitTransaction(inv.ctx, durable.TransactionScope{
		ConversationID: &inv.conversationID,
		TaskID:         &inv.taskID,
	}, func(tx *durable.Transaction) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		found := s.live[inv.taskID]
		if found == nil || found.State.Status != durable.TaskStatusRunning || s.closing {
			inv.markEnded()
			return nil
		}
		current = *found
		d := decide(tx, current)
		if d.cont {
			cont = true
			return nil
		}
		inv.markEnded()
		if d.fault != nil {
			return s.terminate(tx, s.overlayLocked(tx), current, durable.TaskOutcome{Status: durable.OutcomeFaulted, Error: &durable.TaskOutcomeError{Message: d.fault.Error()}})
		}
		return nil
	})
	if err != nil {
		inv.end()
		if !s.isClosing() {
			s.report(err)
		}
		return durable.TaskRecord{}, false
	}
	return current, cont
}

func (s *scheduler) terminate(tx *durable.Transaction, o overlay, record durable.TaskRecord, outcome durable.TaskOutcome) error {
	if ownedLive(mergeOverlay(o, tx))[record.ID] != nil {
		return tx.SetTask(withState(record, durable.TaskState{Status: durable.TaskStatusCompleting, Outcome: &outcome}))
	}
	return s.terminateTerminal(tx, record, outcome)
}

func (s *scheduler) terminateTerminal(tx *durable.Transaction, record durable.TaskRecord, outcome durable.TaskOutcome) error {
	if err := tx.SetTask(withState(record, durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &outcome})); err != nil {
		return err
	}
	if outcome.Status == durable.OutcomeFaulted || outcome.Status == durable.OutcomeOrphaned {
		return settleSchedulerOutcome(tx.Context(), tx, record, outcome)
	}
	return nil
}

func mergeOverlay(o overlay, tx *durable.Transaction) overlay {
	merged := overlay{nodes: map[durable.TaskID]taskNode{}, records: map[durable.TaskID]*durable.TaskRecord{}}
	for id, node := range o.nodes {
		merged.nodes[id] = node
	}
	for id, record := range o.records {
		merged.records[id] = record
	}
	for _, record := range tx.StagedTasks() {
		merged.nodes[record.ID] = nodeOf(*record)
		merged.records[record.ID] = record
	}
	return merged
}

// commitState persists what a task committed.
func (s *scheduler) commitState(tx *durable.Transaction, inv *invocation, current durable.TaskRecord) error {
	o := s.overlayLocked(tx)
	if current.State.Status == durable.TaskStatusWaiting {
		if err := s.validateWait(tx, o, inv, current); err != nil {
			return err
		}
	}
	o = mergeOverlay(s.overlayLocked(tx), tx)
	if current.State.Status == durable.TaskStatusTerminal {
		if ownedLive(o)[current.ID] != nil {
			return tx.SetTask(withState(current, durable.TaskState{Status: durable.TaskStatusCompleting, Outcome: current.State.Outcome}))
		}
		return s.terminateTerminal(tx, current, derefOutcome(current.State.Outcome))
	}
	return tx.SetTask(current)
}

func derefOutcome(outcome *durable.TaskOutcome) durable.TaskOutcome {
	if outcome == nil {
		return durable.TaskOutcome{Status: durable.OutcomeCompleted}
	}
	return *outcome
}

func (s *scheduler) validateWait(tx *durable.Transaction, o overlay, inv *invocation, current durable.TaskRecord) error {
	if inv.mode == "abort" {
		return fmt.Errorf("Abort handler of task %d cannot wait", current.ID)
	}
	owners := map[durable.TaskID]bool{}
	at := o.nodes[current.ID]
	for at.owner != nil {
		owners[*at.owner] = true
		parent, ok := o.nodes[*at.owner]
		if !ok {
			break
		}
		at = parent
	}
	for _, id := range current.State.On {
		if id == current.ID || owners[id] {
			return fmt.Errorf("Task %d cannot wait on itself or its owner %d", current.ID, id)
		}
		member, ok := o.records[id]
		if !ok {
			stored, err := s.storage.Task(tx.Context(), id)
			if err != nil {
				return err
			}
			member = stored
		}
		if member == nil {
			return fmt.Errorf("Task %d does not exist", id)
		}
		if current.State.Policy == durable.PolicyFailFast {
			if member.Owner == nil || *member.Owner != current.ID {
				return fmt.Errorf("Task %d can wait failFast only on tasks it owns; %d is not one", current.ID, id)
			}
		}
	}
	return nil
}

// ownedLive maps each owner task to the live non-background tasks that hold it.
func ownedLive(o overlay) map[durable.TaskID][]durable.TaskID {
	owned := map[durable.TaskID][]durable.TaskID{}
	for id, record := range o.records {
		if record.State.Status == durable.TaskStatusTerminal || record.Background {
			continue
		}
		at := o.nodes[id]
		for at.owner != nil {
			owner, ok := o.nodes[*at.owner]
			if !ok {
				break
			}
			owned[*at.owner] = append(owned[*at.owner], id)
			if owner.background {
				break
			}
			at = owner
		}
	}
	return owned
}

func waitingOn(record *durable.TaskRecord, o overlay, owned map[durable.TaskID][]durable.TaskID) []durable.TaskID {
	if record.AbortRequested {
		return owned[record.ID]
	}
	if record.State.Status != durable.TaskStatusWaiting {
		return nil
	}
	var live []durable.TaskID
	for _, id := range record.State.On {
		if member, ok := o.records[id]; ok && member.State.Status != durable.TaskStatusTerminal {
			live = append(live, id)
		}
	}
	return live
}

// ─── Public operations ──────────────────────────────────────────────────────

func (s *scheduler) waitForTask(id durable.TaskID, ctx context.Context) (*durable.TaskRecord, error) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return nil, closedError()
	}
	if _, ok := s.live[id]; ok {
		ch, cancel := s.taskWaiters.add(id)
		s.mu.Unlock()
		select {
		case record := <-ch:
			cancel()
			return record, nil
		case <-ctx.Done():
			cancel()
			return nil, ctx.Err()
		}
	}
	s.mu.Unlock()
	record, err := s.storage.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("Task %d does not exist", id)
	}
	return record, nil
}

func (s *scheduler) waitForIdle(conversationID durable.ConversationID, scoped bool, ctx context.Context) error {
	key := ""
	if scoped {
		key = fmt.Sprint(int64(conversationID))
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return closedError()
	}
	if s.idleLocked(key) {
		s.mu.Unlock()
		return nil
	}
	ch, cancel := s.idleWaiters.add(key)
	s.mu.Unlock()
	select {
	case <-ch:
		cancel()
		return nil
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}

func (s *scheduler) abort(id durable.TaskID, ctx context.Context) (string, error) {
	var result string
	var runInv *invocation
	err := s.session.CommitTransaction(ctx, durable.TransactionScope{}, func(tx *durable.Transaction) error {
		current, err := tx.Task(ctx, id)
		if err != nil {
			return err
		}
		if current == nil {
			current = s.recordByID(id)
		}
		if current == nil {
			return fmt.Errorf("Task %d does not exist", id)
		}
		if current.State.Status == durable.TaskStatusTerminal {
			result = "terminal"
			return nil
		}
		s.mu.Lock()
		inv := s.invocations[id]
		o := s.overlayLocked(tx)
		owned := ownedLive(o)
		s.mu.Unlock()
		if inv == nil && current.State.Status != durable.TaskStatusCompleting && owned[id] == nil {
			res := s.resolveLocked(*current, s.registry.Snapshot())
			if res.blocked {
				if err := s.terminate(tx, overlay{}, *current, durable.TaskOutcome{Status: durable.OutcomeOrphaned, Reason: res.reason}); err != nil {
					return err
				}
				result = "marked"
				return nil
			}
		}
		if !current.AbortRequested {
			updated := *current
			updated.AbortRequested = true
			if err := tx.SetTask(withState(updated, updated.State)); err != nil {
				return err
			}
		}
		result = "marked"
		if inv != nil && inv.mode == "run" {
			runInv = inv
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if runInv != nil {
		select {
		case <-runInv.done:
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}
	return result, nil
}

func (s *scheduler) recordByID(id durable.TaskID) *durable.TaskRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live[id]
}

func (s *scheduler) abortConversation(conversationID durable.ConversationID, background bool, ctx context.Context) error {
	var reached []durable.TaskID
	err := s.session.CommitTransaction(ctx, durable.TransactionScope{ConversationID: &conversationID}, func(tx *durable.Transaction) error {
		subs, err := scanAll(func(cursor durable.Cursor) (durable.Page[durable.SubmissionRecord], error) {
			return s.storage.ScanSubmissions(ctx, durable.SubmissionQuery{Status: durable.SubmissionStatusQueued}, scanPageSize, cursor)
		})
		if err != nil {
			return err
		}
		seen := map[durable.ConversationID]bool{}
		var queued []durable.ConversationID
		for _, sub := range subs {
			if !seen[sub.ConversationID] {
				seen[sub.ConversationID] = true
				queued = append(queued, sub.ConversationID)
			}
		}
		s.mu.Lock()
		o := s.overlayLocked(tx)
		for _, record := range s.live {
			if record.Background && !background {
				continue
			}
			if !inScope(o, record, conversationID, background) {
				continue
			}
			reached = append(reached, record.ID)
			if !record.AbortRequested {
				updated := *record
				updated.AbortRequested = true
				if err := tx.SetTask(withState(updated, updated.State)); err != nil {
					s.mu.Unlock()
					return err
				}
			}
		}
		s.mu.Unlock()
		for _, id := range queued {
			if id == conversationID || background {
				if err := withdrawQueuedInputs(ctx, tx, id); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if background {
		for _, id := range reached {
			if _, err := s.waitForTask(id, ctx); err != nil {
				return err
			}
		}
	}
	return s.waitForIdle(conversationID, true, ctx)
}

func inScope(o overlay, record *durable.TaskRecord, conversationID durable.ConversationID, cross bool) bool {
	if record.ConversationID != conversationID {
		return false
	}
	if cross {
		return true
	}
	at := nodeOf(*record)
	for at.owner != nil {
		owner, ok := o.nodes[*at.owner]
		if !ok {
			return true
		}
		if owner.background {
			return false
		}
		at = owner
	}
	return true
}

func (s *scheduler) inspect(snapshot RegistrySnapshot) (string, []TaskInspection) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.overlayLocked(nil)
	owned := ownedLive(o)
	ids := make([]durable.TaskID, 0, len(s.live))
	for id := range s.live {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	tasks := make([]TaskInspection, 0, len(ids))
	for _, id := range ids {
		record := s.live[id]
		tasks = append(tasks, TaskInspection{Record: *record, State: s.inspectTaskLocked(record, snapshot, o, owned)})
	}
	scheduling := "paused"
	if s.closing {
		scheduling = "closing"
	} else if s.enabled {
		scheduling = "running"
	}
	return scheduling, tasks
}

func (s *scheduler) inspectTaskLocked(record *durable.TaskRecord, snapshot RegistrySnapshot, o overlay, owned map[durable.TaskID][]durable.TaskID) TaskInspectionState {
	if _, ok := s.invocations[record.ID]; ok {
		return TaskInspectionState{Kind: "running"}
	}
	if record.State.Status == durable.TaskStatusCompleting {
		return TaskInspectionState{Kind: "completing"}
	}
	if on := waitingOn(record, o, owned); len(on) > 0 {
		return TaskInspectionState{Kind: "waiting", On: on}
	}
	task, ok := snapshot.Task(record.Kind)
	if !ok {
		return TaskInspectionState{Kind: "blocked", Reason: "missing_task"}
	}
	if task.Version < record.Version {
		return TaskInspectionState{Kind: "blocked", Reason: "task_too_old"}
	}
	if task.Version > record.Version {
		if task.Migrate == nil {
			return TaskInspectionState{Kind: "blocked", Reason: "migration_failed"}
		}
		return TaskInspectionState{Kind: "ready", Migrates: true}
	}
	return TaskInspectionState{Kind: "ready"}
}

func withState(record durable.TaskRecord, state durable.TaskState) durable.TaskRecord {
	if state.Status == durable.TaskStatusTerminal || state.Status == durable.TaskStatusCompleting {
		record.Memos = nil
	}
	record.State = state
	return record
}

func nodeOf(record durable.TaskRecord) taskNode {
	return taskNode{conversationID: record.ConversationID, owner: record.Owner, background: record.Background}
}

func phaseOf(checkpoint []byte) string {
	if len(checkpoint) == 0 {
		return ""
	}
	var value struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(checkpoint, &value); err != nil {
		return ""
	}
	return value.Phase
}

func rawValue(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}
