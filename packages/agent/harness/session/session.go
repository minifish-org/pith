// This file carries session.ts: the storage-backed Session implementation.
package session

import (
	"errors"
	"fmt"
	"math"
	"sync"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// StorageBackedSessionOptions configure a StorageBackedSession.
type StorageBackedSessionOptions struct {
	MutationLine *MutationLine
	IDGenerator  harnesstypes.IdGenerator
	OnClose      func()
}

// SessionInvariantError reports internally inconsistent durable session state.
type SessionInvariantError struct{ Message string }

func (e *SessionInvariantError) Error() string { return e.Message }

// SessionInvalidBranchError reports an invalid branch name.
type SessionInvalidBranchError struct {
	Branch string
	Reason string
}

func (e *SessionInvalidBranchError) Error() string {
	return fmt.Sprintf("Invalid branch %q: %s", e.Branch, e.Reason)
}

// SessionBranchExistsError reports an already-existing branch.
type SessionBranchExistsError struct{ Branch string }

func (e *SessionBranchExistsError) Error() string {
	return "Branch already exists: " + e.Branch
}

// SessionPendingAssistantMessageError reports a pending assistant message that
// cannot be persisted.
type SessionPendingAssistantMessageError struct{}

func (e *SessionPendingAssistantMessageError) Error() string {
	return "Cannot persist a pending assistant message"
}

// SessionUnknownTargetError reports a missing session entry target.
type SessionUnknownTargetError struct{ TargetID string }

func (e *SessionUnknownTargetError) Error() string {
	return "Unknown target: " + e.TargetID
}

type uuidIDGenerator struct{}

func (uuidIDGenerator) Next(timestampMs *float64) string {
	id, err := aiutils.UUIDv7(timestampMs)
	if err != nil {
		panic(err)
	}
	return id
}

type storageBackedSessionMutation struct {
	storage      harnesstypes.Storage
	release      func()
	active       bool
	commitResult *harnesstypes.CommitResult
	commitErr    error
	committed    bool
	endOnce      sync.Once
	endErr       error
}

func (m *storageBackedSessionMutation) assertActive() error {
	if !m.active {
		return errors.New("SessionMutator cannot be used outside its mutation callback")
	}
	return nil
}

func (m *storageBackedSessionMutation) Commit(writes []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	if err := m.assertActive(); err != nil {
		return harnesstypes.CommitResult{}, err
	}
	if m.committed {
		return harnesstypes.CommitResult{}, errors.New("SessionMutator commit already attempted")
	}
	m.committed = true
	for _, write := range writes {
		if hasPendingAssistantWrite(write) {
			m.commitErr = &SessionPendingAssistantMessageError{}
			return harnesstypes.CommitResult{}, m.commitErr
		}
	}
	result, err := m.storage.Commit(writes, ctx)
	if err != nil {
		m.commitErr = err
		return harnesstypes.CommitResult{}, err
	}
	m.commitResult = &result
	return result, nil
}

func (m *storageBackedSessionMutation) GetEntries(ids []string, ctx harnesstypes.Context) (map[string]harnesstypes.Entry, error) {
	if err := m.assertActive(); err != nil {
		return nil, err
	}
	return m.storage.GetEntries(ids, ctx)
}

func (m *storageBackedSessionMutation) GetStats(ctx harnesstypes.Context) (harnesstypes.SessionStats, error) {
	if err := m.assertActive(); err != nil {
		return harnesstypes.SessionStats{}, err
	}
	return m.storage.GetStats(ctx)
}

func (m *storageBackedSessionMutation) GetValue(address harnesstypes.Value, ctx harnesstypes.Context) (harnesstypes.StoredValue, bool, error) {
	if err := m.assertActive(); err != nil {
		return harnesstypes.StoredValue{}, false, err
	}
	return m.storage.GetValue(address, ctx)
}

func (m *storageBackedSessionMutation) ScanValues(prefix harnesstypes.Value, ctx harnesstypes.Context) ([]harnesstypes.StoredValue, error) {
	if err := m.assertActive(); err != nil {
		return nil, err
	}
	return m.storage.ScanValues(prefix, ctx)
}

func (m *storageBackedSessionMutation) ReadList(address harnesstypes.ValueList, options *harnesstypes.ListReadOptions, ctx harnesstypes.Context) ([]harnesstypes.ListElement, error) {
	if err := m.assertActive(); err != nil {
		return nil, err
	}
	return m.storage.ReadList(address, options, ctx)
}

func (m *storageBackedSessionMutation) ScanBranch(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	if err := m.assertActive(); err != nil {
		return nil, err
	}
	return m.storage.ScanBranch(query, ctx)
}

// End waits for the commit attempt and releases the mutation barrier.
func (m *storageBackedSessionMutation) End(ctx harnesstypes.Context) error {
	m.endOnce.Do(func() {
		m.active = false
		m.release()
	})
	return m.endErr
}

// StorageBackedSession is the durable Session implementation shared by concrete
// repositories.
type StorageBackedSession[T harnesstypes.SessionMetadataView] struct {
	metadata     T
	idGenerator  harnesstypes.IdGenerator
	storage      harnesstypes.Storage
	mutationLine *MutationLine
	onClose      func()

	mu          sync.Mutex
	state       string
	closedError error
	closeOnce   sync.Once
	closeErr    error

	branchesMu sync.Mutex
	branches   map[string]*storageBackedBranch[T]
}

// NewStorageBackedSession builds a session over storage.
func NewStorageBackedSession[T harnesstypes.SessionMetadataView](metadata T, storage harnesstypes.Storage, options StorageBackedSessionOptions) *StorageBackedSession[T] {
	idGenerator := options.IDGenerator
	if idGenerator == nil {
		idGenerator = uuidIDGenerator{}
	}
	mutationLine := options.MutationLine
	if mutationLine == nil {
		mutationLine = NewMutationLine()
	}
	return &StorageBackedSession[T]{
		metadata:     metadata,
		idGenerator:  idGenerator,
		storage:      storage,
		mutationLine: mutationLine,
		onClose:      options.OnClose,
		state:        "open",
		closedError:  errors.New("Session is closed"),
		branches:     map[string]*storageBackedBranch[T]{},
	}
}

// Metadata returns the session identity.
func (s *StorageBackedSession[T]) Metadata() T { return s.metadata }

// IDGenerator returns the session id generator.
func (s *StorageBackedSession[T]) IDGenerator() harnesstypes.IdGenerator { return s.idGenerator }

func (s *StorageBackedSession[T]) assertOpen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != "open" {
		return s.closedError
	}
	return nil
}

// BeginMutation acquires the exclusive mutation barrier.
func (s *StorageBackedSession[T]) BeginMutation(ctx harnesstypes.Context) (harnesstypes.SessionMutation, error) {
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	if err := s.mutationLine.acquire(); err != nil {
		return nil, err
	}
	if err := s.assertOpen(); err != nil {
		s.mutationLine.release()
		return nil, err
	}
	return &storageBackedSessionMutation{
		storage: s.storage,
		release: s.mutationLine.release,
		active:  true,
	}, nil
}

// Mutate runs one trusted exclusive callback over the mutation line.
func (s *StorageBackedSession[T]) Mutate(callback harnesstypes.SessionMutationCallback[any], ctx harnesstypes.Context) (any, error) {
	mutator, err := s.BeginMutation(ctx)
	if err != nil {
		return nil, err
	}
	defer mutator.End(ctx)
	return callback(mutator, ctx)
}

// GetEntries resolves entries by id.
func (s *StorageBackedSession[T]) GetEntries(ids []string, ctx harnesstypes.Context) (map[string]harnesstypes.Entry, error) {
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.storage.GetEntries(ids, ctx)
}

// GetEntry resolves one entry.
func (s *StorageBackedSession[T]) GetEntry(id string, ctx harnesstypes.Context) (harnesstypes.Entry, bool, error) {
	entries, err := s.GetEntries([]string{id}, ctx)
	if err != nil {
		return nil, false, err
	}
	entry, ok := entries[id]
	return entry, ok, nil
}

// GetValue resolves one durable value.
func (s *StorageBackedSession[T]) GetValue(address harnesstypes.Value, ctx harnesstypes.Context) (harnesstypes.StoredValue, bool, error) {
	if err := s.assertOpen(); err != nil {
		return harnesstypes.StoredValue{}, false, err
	}
	return s.storage.GetValue(address, ctx)
}

// ScanValues scans durable values by namespace prefix.
func (s *StorageBackedSession[T]) ScanValues(prefix harnesstypes.Value, ctx harnesstypes.Context) ([]harnesstypes.StoredValue, error) {
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.storage.ScanValues(prefix, ctx)
}

// ReadList reads one page of a durable list.
func (s *StorageBackedSession[T]) ReadList(address harnesstypes.ValueList, options *harnesstypes.ListReadOptions, ctx harnesstypes.Context) ([]harnesstypes.ListElement, error) {
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.storage.ReadList(address, options, ctx)
}

// ScanBranch scans one branch.
func (s *StorageBackedSession[T]) ScanBranch(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.storage.ScanBranch(query, ctx)
}

// GetStats returns the session totals.
func (s *StorageBackedSession[T]) GetStats(ctx harnesstypes.Context) (harnesstypes.SessionStats, error) {
	if err := s.assertOpen(); err != nil {
		return harnesstypes.SessionStats{}, err
	}
	return s.storage.GetStats(ctx)
}

// GetName returns the session display name.
func (s *StorageBackedSession[T]) GetName(ctx harnesstypes.Context) (*string, error) {
	stored, ok, err := s.GetValue(SessionName, ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return stringPointerValue(stored.Value, "Session name")
}

// GetLabel returns the label of one entry.
func (s *StorageBackedSession[T]) GetLabel(targetID string, ctx harnesstypes.Context) (*string, error) {
	stored, ok, err := s.GetValue(EntryLabel(targetID), ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return stringPointerValue(stored.Value, "Entry label")
}

// FindEntries scans global entries.
func (s *StorageBackedSession[T]) FindEntries(query *harnesstypes.EntryQuery, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	var resolved harnesstypes.EntryQuery
	if query != nil {
		resolved = *query
	}
	order := "desc"
	if resolved.Order != nil {
		order = *resolved.Order
	}
	if resolved.Cursor != nil {
		if order == "asc" && resolved.Cursor.Seq == math.MaxInt64 {
			return []harnesstypes.Entry{}, nil
		}
		if order == "desc" && resolved.Cursor.Seq <= 1 {
			return []harnesstypes.Entry{}, nil
		}
	}
	scan := harnesstypes.EntryScan{
		Type:       resolved.Type,
		CustomType: resolved.CustomType,
		Order:      &order,
		Limit:      resolved.Limit,
	}
	if resolved.Cursor != nil {
		if order == "asc" {
			from := resolved.Cursor.Seq + 1
			scan.FromSeq = &from
		} else {
			to := resolved.Cursor.Seq - 1
			scan.ToSeq = &to
		}
	}
	return s.storage.ScanEntries(scan, ctx)
}

// FindEntry returns the first matching entry.
func (s *StorageBackedSession[T]) FindEntry(query *harnesstypes.EntryQuery, ctx harnesstypes.Context) (harnesstypes.Entry, bool, error) {
	resolved := harnesstypes.EntryQuery{}
	if query != nil {
		resolved = *query
	}
	limit := 1
	if resolved.Limit != nil && *resolved.Limit < 1 {
		limit = *resolved.Limit
	}
	resolved.Limit = &limit
	entries, err := s.FindEntries(&resolved, ctx)
	if err != nil {
		return nil, false, err
	}
	if len(entries) == 0 {
		return nil, false, nil
	}
	return entries[0], true, nil
}

// Branch returns a branch handle when the branch exists.
func (s *StorageBackedSession[T]) Branch(name string, ctx harnesstypes.Context) (harnesstypes.Branch, bool, error) {
	if err := validateBranchName(name); err != nil {
		return nil, false, err
	}
	if _, ok, err := s.GetValue(BranchTip(name), ctx); err != nil {
		return nil, false, err
	} else if !ok {
		return nil, false, nil
	}
	return s.branchObject(name), true, nil
}

// CreateBranch creates a new branch at an entry or the empty root.
func (s *StorageBackedSession[T]) CreateBranch(name string, at *string, ctx harnesstypes.Context) (harnesstypes.Branch, error) {
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	if err := validateBranchName(name); err != nil {
		return nil, err
	}
	_, err := s.Mutate(func(mutator harnesstypes.SessionMutator, mutationCtx harnesstypes.Context) (any, error) {
		if _, ok, err := mutator.GetValue(BranchTip(name), mutationCtx); err != nil {
			return nil, err
		} else if ok {
			return nil, &SessionBranchExistsError{Branch: name}
		}
		if at != nil {
			entries, err := mutator.GetEntries([]string{*at}, mutationCtx)
			if err != nil {
				return nil, err
			}
			if _, ok := entries[*at]; !ok {
				return nil, &SessionUnknownTargetError{TargetID: *at}
			}
		}
		var writes []harnesstypes.Write
		if at == nil {
			writes = []harnesstypes.Write{harnesstypes.ValueSetWrite{Namespace: NamespaceBranchTip, Key: name, Value: nil}}
		} else {
			writes = []harnesstypes.Write{harnesstypes.ValueSetWrite{Namespace: NamespaceBranchTip, Key: name, Value: *at}}
		}
		_, err := mutator.Commit(writes, mutationCtx)
		return nil, err
	}, ctx)
	if err != nil {
		return nil, err
	}
	return s.branchObject(name), nil
}

// SetValue writes one durable scalar value.
func (s *StorageBackedSession[T]) SetValue(address harnesstypes.Value, next any, ctx harnesstypes.Context) error {
	_, err := s.Mutate(func(mutator harnesstypes.SessionMutator, mutationCtx harnesstypes.Context) (any, error) {
		_, err := mutator.Commit([]harnesstypes.Write{harnesstypes.ValueSetWrite{Namespace: address.Namespace, Key: address.Key, Value: next}}, mutationCtx)
		return nil, err
	}, ctx)
	return err
}

// DeleteValue deletes one durable scalar value.
func (s *StorageBackedSession[T]) DeleteValue(address harnesstypes.Value, ctx harnesstypes.Context) error {
	_, err := s.Mutate(func(mutator harnesstypes.SessionMutator, mutationCtx harnesstypes.Context) (any, error) {
		_, err := mutator.Commit([]harnesstypes.Write{harnesstypes.ValueDeleteWrite{Namespace: address.Namespace, Key: address.Key}}, mutationCtx)
		return nil, err
	}, ctx)
	return err
}

// AppendList appends one list element.
func (s *StorageBackedSession[T]) AppendList(address harnesstypes.ValueList, element any, ctx harnesstypes.Context) error {
	_, err := s.Mutate(func(mutator harnesstypes.SessionMutator, mutationCtx harnesstypes.Context) (any, error) {
		_, err := mutator.Commit([]harnesstypes.Write{harnesstypes.ListAppendWrite{Namespace: address.Namespace, Key: address.Key, Value: element}}, mutationCtx)
		return nil, err
	}, ctx)
	return err
}

// DeleteList deletes one whole list.
func (s *StorageBackedSession[T]) DeleteList(address harnesstypes.ValueList, ctx harnesstypes.Context) error {
	_, err := s.Mutate(func(mutator harnesstypes.SessionMutator, mutationCtx harnesstypes.Context) (any, error) {
		_, err := mutator.Commit([]harnesstypes.Write{harnesstypes.ListDeleteWrite{Namespace: address.Namespace, Key: address.Key}}, mutationCtx)
		return nil, err
	}, ctx)
	return err
}

// SetName sets or clears the session display name.
func (s *StorageBackedSession[T]) SetName(name *string, ctx harnesstypes.Context) error {
	if name == nil {
		return s.DeleteValue(SessionName, ctx)
	}
	return s.SetValue(SessionName, *name, ctx)
}

// SetLabel sets or clears one entry label.
func (s *StorageBackedSession[T]) SetLabel(targetID string, label *string, ctx harnesstypes.Context) error {
	address := EntryLabel(targetID)
	if label == nil {
		return s.DeleteValue(address, ctx)
	}
	return s.SetValue(address, *label, ctx)
}

// GetBranchTip returns the tip of a branch, or an invariant error for an
// unknown branch.
func (s *StorageBackedSession[T]) GetBranchTip(name string, ctx harnesstypes.Context) (*string, error) {
	stored, ok, err := s.GetValue(BranchTip(name), ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &SessionInvariantError{Message: "Unknown branch: " + name}
	}
	return valueStringPointer(stored.Value), nil
}

// AppendToBranch appends one message or custom entry to a branch.
func (s *StorageBackedSession[T]) AppendToBranch(name string, entry harnesstypes.Entry, ctx harnesstypes.Context) (string, error) {
	if err := s.assertOpen(); err != nil {
		return "", err
	}
	if isPendingAssistantEntry(entry) {
		return "", &SessionPendingAssistantMessageError{}
	}
	id := s.idGenerator.Next(nil)
	_, err := s.Mutate(func(mutator harnesstypes.SessionMutator, mutationCtx harnesstypes.Context) (any, error) {
		tip, ok, err := mutator.GetValue(BranchTip(name), mutationCtx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, &SessionInvariantError{Message: "Unknown branch: " + name}
		}
		var parentID *string
		if tip.Value != nil {
			parentID = valueStringPointer(tip.Value)
		}
		stamped := withEntryBase(entry, id, parentID)
		tipWrite := harnesstypes.ValueSetWrite{Namespace: NamespaceBranchTip, Key: name, Value: id}
		if _, err := mutator.Commit([]harnesstypes.Write{InsertEntry(stamped), tipWrite}, mutationCtx); err != nil {
			return nil, err
		}
		return nil, nil
	}, ctx)
	if err != nil {
		return "", err
	}
	return id, nil
}

// Close seals admission, drains the mutation line and closes storage.
func (s *StorageBackedSession[T]) Close(ctx harnesstypes.Context) error {
	s.mu.Lock()
	if s.state == "closed" {
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	s.state = "closing"
	s.mu.Unlock()

	s.closeOnce.Do(func() {
		s.mutationLine.seal(s.closedError)
		err := s.storage.Close(ctx)
		s.mu.Lock()
		s.state = "closed"
		s.closeErr = err
		s.mu.Unlock()
		if s.onClose != nil {
			s.onClose()
		}
	})
	s.mu.Lock()
	err := s.closeErr
	s.mu.Unlock()
	return err
}

func (s *StorageBackedSession[T]) branchObject(name string) *storageBackedBranch[T] {
	s.branchesMu.Lock()
	defer s.branchesMu.Unlock()
	branch, ok := s.branches[name]
	if !ok {
		branch = &storageBackedBranch[T]{name: name, session: s}
		s.branches[name] = branch
	}
	return branch
}

// MutateSession runs a typed mutation callback over a StorageBackedSession.
func MutateSession[T harnesstypes.SessionMetadataView, R any](
	s *StorageBackedSession[T],
	ctx harnesstypes.Context,
	callback func(mutator harnesstypes.SessionMutator, ctx harnesstypes.Context) (R, error),
) (R, error) {
	var zero R
	mutator, err := s.BeginMutation(ctx)
	if err != nil {
		return zero, err
	}
	defer mutator.End(ctx)
	return callback(mutator, ctx)
}

type storageBackedBranch[T harnesstypes.SessionMetadataView] struct {
	name    string
	session *StorageBackedSession[T]
}

func (b *storageBackedBranch[T]) Name() string { return b.name }

func (b *storageBackedBranch[T]) GetTipID(ctx harnesstypes.Context) (*string, error) {
	return b.session.GetBranchTip(b.name, ctx)
}

func (b *storageBackedBranch[T]) FindEntries(query *harnesstypes.BranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	var resolved harnesstypes.BranchScan
	if query != nil {
		resolved = *query
	}
	var start *string
	if resolved.Start != nil {
		start = resolved.Start
	} else {
		tip, err := b.GetTipID(ctx)
		if err != nil {
			return nil, err
		}
		if tip == nil {
			return []harnesstypes.Entry{}, nil
		}
		start = tip
	}
	order := "newestFirst"
	if resolved.Order != nil {
		order = *resolved.Order
	}
	scan := harnesstypes.StorageBranchScan{
		BranchScan: resolved,
		Start:      *start,
	}
	scan.Order = &order
	return b.session.ScanBranch(scan, ctx)
}

func (b *storageBackedBranch[T]) FindEntry(query *harnesstypes.BranchScan, ctx harnesstypes.Context) (harnesstypes.Entry, bool, error) {
	resolved := harnesstypes.BranchScan{}
	if query != nil {
		resolved = *query
	}
	limit := 1
	resolved.Limit = &limit
	entries, err := b.FindEntries(&resolved, ctx)
	if err != nil {
		return nil, false, err
	}
	if len(entries) == 0 {
		return nil, false, nil
	}
	return entries[0], true, nil
}

func (b *storageBackedBranch[T]) AppendMessage(message agenttypes.AgentMessage, ctx harnesstypes.Context) (string, error) {
	entry := harnesstypes.MessageEntry{EntryBase: harnesstypes.EntryBase{Type: harnesstypes.EntryTypeMessage}, Message: message}
	return b.session.AppendToBranch(b.name, entry, ctx)
}

func (b *storageBackedBranch[T]) AppendCustomEntry(customType string, data harnesstypes.JsonValue, ctx harnesstypes.Context) (string, error) {
	entry := harnesstypes.CustomEntry{
		EntryBase:  harnesstypes.EntryBase{Type: harnesstypes.EntryTypeCustom},
		CustomType: customType,
		Data:       data,
	}
	return b.session.AppendToBranch(b.name, entry, ctx)
}

func validateBranchName(name string) error {
	if name == "" {
		return &SessionInvalidBranchError{Branch: name, Reason: "branch name must not be empty"}
	}
	for _, r := range name {
		if r == 0 {
			return &SessionInvalidBranchError{Branch: name, Reason: "branch name must not contain \\u0000"}
		}
	}
	return nil
}

func stringPointerValue(value any, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, &SessionInvariantError{Message: field + " is not a string"}
	}
	return &text, nil
}

func isPendingAssistantEntry(entry harnesstypes.Entry) bool {
	messageEntry, ok := entry.(harnesstypes.MessageEntry)
	if !ok {
		if pointer, pointerOK := entry.(*harnesstypes.MessageEntry); pointerOK {
			messageEntry = *pointer
		} else {
			return false
		}
	}
	return isPendingAssistantMessage(messageEntry.Message)
}

func isPendingAssistantMessage(message agenttypes.AgentMessage) bool {
	if message.Message == nil || message.Message.Assistant == nil {
		return false
	}
	return message.Message.Assistant.StopReason == aitypes.StopReasonPending
}

func hasPendingAssistantWrite(write harnesstypes.Write) bool {
	entryWrite, ok := write.(harnesstypes.EntryWrite)
	if !ok {
		if pointer, pointerOK := write.(*harnesstypes.EntryWrite); pointerOK {
			return isPendingAssistantEntry(pointer.Entry)
		}
		return false
	}
	return isPendingAssistantEntry(entryWrite.Entry)
}

func withEntryBase(entry harnesstypes.Entry, id string, parentID *string) harnesstypes.Entry {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		typed.ID = id
		typed.ParentID = parentID
		return typed
	case *harnesstypes.MessageEntry:
		copied := *typed
		copied.ID = id
		copied.ParentID = parentID
		return &copied
	case harnesstypes.CustomEntry:
		typed.ID = id
		typed.ParentID = parentID
		return typed
	case *harnesstypes.CustomEntry:
		copied := *typed
		copied.ID = id
		copied.ParentID = parentID
		return &copied
	case harnesstypes.BranchSummaryEntry:
		typed.ID = id
		typed.ParentID = parentID
		return typed
	case *harnesstypes.BranchSummaryEntry:
		copied := *typed
		copied.ID = id
		copied.ParentID = parentID
		return &copied
	case harnesstypes.CompactionEntry:
		typed.ID = id
		typed.ParentID = parentID
		return typed
	case *harnesstypes.CompactionEntry:
		copied := *typed
		copied.ID = id
		copied.ParentID = parentID
		return &copied
	default:
		return entry
	}
}
