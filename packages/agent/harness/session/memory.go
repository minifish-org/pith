// This file carries memory.ts: the in-memory Storage and SessionRepo.
package session

import (
	"errors"
	"sync"
	"time"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// MemoryStorageOptions configure MemoryStorage.
type MemoryStorageOptions struct {
	Now func() float64
}

// MemorySessionRepoOptions configure MemorySessionRepo.
type MemorySessionRepoOptions struct {
	Now func() float64
}

const memoryStorageVersion = 1

func defaultNow() float64 { return float64(time.Now().UnixMilli()) }

func resolveNow(now func() float64) func() float64 {
	if now != nil {
		return now
	}
	return defaultNow
}

// MemoryStorage is an in-memory durable storage capability.
type MemoryStorage struct {
	mu        sync.Mutex
	now       func() float64
	state     *InMemoryStorageState
	closed    bool
	closeOnce sync.Once
}

// NewMemoryStorage builds empty in-memory storage.
func NewMemoryStorage(options *MemoryStorageOptions) *MemoryStorage {
	var now func() float64
	if options != nil {
		now = options.Now
	}
	return &MemoryStorage{now: resolveNow(now), state: NewInMemoryStorageState()}
}

func (s *MemoryStorage) assertOpen() error {
	if s.closed {
		return errors.New("MemoryStorage is closed")
	}
	return nil
}

// Commit applies one transaction atomically.
func (s *MemoryStorage) Commit(writes []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return harnesstypes.CommitResult{}, err
	}
	prepared, err := s.state.PrepareCommit(writes, s.now())
	if err != nil {
		return harnesstypes.CommitResult{}, err
	}
	stats := s.state.ApplyValidated(prepared.Writes)
	return harnesstypes.CommitResult{
		FirstSeq:  prepared.Result.FirstSeq,
		Seqs:      prepared.Result.Seqs,
		Timestamp: prepared.Result.Timestamp,
		Stats:     stats,
	}, nil
}

// GetEntries resolves entries by id.
func (s *MemoryStorage) GetEntries(ids []string, ctx harnesstypes.Context) (map[string]harnesstypes.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.state.GetEntries(ids), nil
}

// GetValue resolves one scalar value.
func (s *MemoryStorage) GetValue(address harnesstypes.Value, ctx harnesstypes.Context) (harnesstypes.StoredValue, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return harnesstypes.StoredValue{}, false, err
	}
	value, ok := s.state.GetValue(address)
	return value, ok, nil
}

// ScanValues scans scalar values by namespace prefix.
func (s *MemoryStorage) ScanValues(prefix harnesstypes.Value, ctx harnesstypes.Context) ([]harnesstypes.StoredValue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.state.ScanValues(prefix), nil
}

// ReadList reads one page of a list.
func (s *MemoryStorage) ReadList(address harnesstypes.ValueList, options *harnesstypes.ListReadOptions, ctx harnesstypes.Context) ([]harnesstypes.ListElement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.state.ReadList(address, options)
}

// ScanBranch scans one branch.
func (s *MemoryStorage) ScanBranch(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.state.ScanBranch(query)
}

// ScanBranchStructure scans one branch structure.
func (s *MemoryStorage) ScanBranchStructure(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.EntryStructure, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.state.ScanBranchStructure(query)
}

// ScanEntries scans the global entry index.
func (s *MemoryStorage) ScanEntries(query harnesstypes.EntryScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.state.ScanEntries(query), nil
}

// ScanUsage scans the usage ledger.
func (s *MemoryStorage) ScanUsage(query harnesstypes.UsageScan, ctx harnesstypes.Context) ([]harnesstypes.UsageRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return s.state.ScanUsage(query), nil
}

// GetStats returns the session totals.
func (s *MemoryStorage) GetStats(ctx harnesstypes.Context) (harnesstypes.SessionStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return harnesstypes.SessionStats{}, err
	}
	return s.state.GetStats(), nil
}

// Close seals admission and marks the storage closed.
func (s *MemoryStorage) Close(ctx harnesstypes.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	})
	return nil
}

// Fork builds a destination storage at one serialized boundary between source
// commits.
func (s *MemoryStorage) Fork(options harnesstypes.ForkOptions) (*MemoryStorage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	destination := NewMemoryStorage(&MemoryStorageOptions{Now: s.now})
	forked, err := s.state.CreateFork(options)
	if err != nil {
		return nil, err
	}
	destination.state = forked
	return destination, nil
}

// memorySessionRecord is the repository-owned state of one in-memory session.
type memorySessionRecord struct {
	metadata harnesstypes.SessionMetadata
	storage  *MemoryStorage
	session  *StorageBackedSession[harnesstypes.SessionMetadata]
	open     bool
}

// openRecord builds a facade over one underlying session.
func (r *MemorySessionRepo) openRecord(record *memorySessionRecord) harnesstypes.Session[harnesstypes.SessionMetadata] {
	return &memorySessionFacade{inner: record.session, record: record, state: "open", markClosed: func() {
		r.mu.Lock()
		record.open = false
		r.mu.Unlock()
	}}
}

type memorySessionFacade struct {
	inner      *StorageBackedSession[harnesstypes.SessionMetadata]
	record     *memorySessionRecord
	markClosed func()
	mu         sync.Mutex
	state      string
	closeErr   error
	once       sync.Once
	admitted   sync.WaitGroup
}

func (f *memorySessionFacade) closedError() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closeErr != nil {
		return f.closeErr
	}
	return errors.New("Session is closed")
}

func (f *memorySessionFacade) begin() error {
	f.mu.Lock()
	if f.state != "open" {
		f.mu.Unlock()
		return f.closedError()
	}
	f.admitted.Add(1)
	f.mu.Unlock()
	return nil
}

func (f *memorySessionFacade) end() { f.admitted.Done() }

func (f *memorySessionFacade) Metadata() harnesstypes.SessionMetadata { return f.inner.Metadata() }
func (f *memorySessionFacade) IDGenerator() harnesstypes.IdGenerator  { return f.inner.IDGenerator() }

func (f *memorySessionFacade) GetEntries(ids []string, ctx harnesstypes.Context) (map[string]harnesstypes.Entry, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	defer f.end()
	return f.inner.GetEntries(ids, ctx)
}

func (f *memorySessionFacade) GetStats(ctx harnesstypes.Context) (harnesstypes.SessionStats, error) {
	if err := f.begin(); err != nil {
		return harnesstypes.SessionStats{}, err
	}
	defer f.end()
	return f.inner.GetStats(ctx)
}

func (f *memorySessionFacade) GetValue(address harnesstypes.Value, ctx harnesstypes.Context) (harnesstypes.StoredValue, bool, error) {
	if err := f.begin(); err != nil {
		return harnesstypes.StoredValue{}, false, err
	}
	defer f.end()
	return f.inner.GetValue(address, ctx)
}

func (f *memorySessionFacade) ScanValues(prefix harnesstypes.Value, ctx harnesstypes.Context) ([]harnesstypes.StoredValue, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	defer f.end()
	return f.inner.ScanValues(prefix, ctx)
}

func (f *memorySessionFacade) ReadList(address harnesstypes.ValueList, options *harnesstypes.ListReadOptions, ctx harnesstypes.Context) ([]harnesstypes.ListElement, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	defer f.end()
	return f.inner.ReadList(address, options, ctx)
}

func (f *memorySessionFacade) ScanBranch(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	defer f.end()
	return f.inner.ScanBranch(query, ctx)
}

func (f *memorySessionFacade) GetEntry(id string, ctx harnesstypes.Context) (harnesstypes.Entry, bool, error) {
	if err := f.begin(); err != nil {
		return nil, false, err
	}
	defer f.end()
	return f.inner.GetEntry(id, ctx)
}

func (f *memorySessionFacade) GetName(ctx harnesstypes.Context) (*string, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	defer f.end()
	return f.inner.GetName(ctx)
}

func (f *memorySessionFacade) GetLabel(targetID string, ctx harnesstypes.Context) (*string, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	defer f.end()
	return f.inner.GetLabel(targetID, ctx)
}

func (f *memorySessionFacade) FindEntries(query *harnesstypes.EntryQuery, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	defer f.end()
	return f.inner.FindEntries(query, ctx)
}

func (f *memorySessionFacade) FindEntry(query *harnesstypes.EntryQuery, ctx harnesstypes.Context) (harnesstypes.Entry, bool, error) {
	if err := f.begin(); err != nil {
		return nil, false, err
	}
	defer f.end()
	return f.inner.FindEntry(query, ctx)
}

func (f *memorySessionFacade) Branch(name string, ctx harnesstypes.Context) (harnesstypes.Branch, bool, error) {
	if err := f.begin(); err != nil {
		return nil, false, err
	}
	defer f.end()
	branch, ok, err := f.inner.Branch(name, ctx)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &memoryBranch{facade: f, inner: branch}, true, nil
}

func (f *memorySessionFacade) CreateBranch(name string, at *string, ctx harnesstypes.Context) (harnesstypes.Branch, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	defer f.end()
	branch, err := f.inner.CreateBranch(name, at, ctx)
	if err != nil {
		return nil, err
	}
	return &memoryBranch{facade: f, inner: branch}, nil
}

func (f *memorySessionFacade) BeginMutation(ctx harnesstypes.Context) (harnesstypes.SessionMutation, error) {
	if err := f.begin(); err != nil {
		return nil, err
	}
	mutator, err := f.inner.BeginMutation(ctx)
	if err != nil {
		f.end()
		return nil, err
	}
	f.mu.Lock()
	if f.state != "open" {
		f.mu.Unlock()
		_ = mutator.End(ctx)
		f.end()
		return nil, f.closedError()
	}
	f.mu.Unlock()
	return &memoryMutation{facade: f, inner: mutator}, nil
}

func (f *memorySessionFacade) Mutate(callback harnesstypes.SessionMutationCallback[any], ctx harnesstypes.Context) (any, error) {
	var zero any
	if err := f.begin(); err != nil {
		return zero, err
	}
	defer f.end()
	return f.inner.Mutate(func(mutator harnesstypes.SessionMutator, mutationCtx harnesstypes.Context) (any, error) {
		f.mu.Lock()
		if f.state != "open" {
			f.mu.Unlock()
			return nil, f.closedError()
		}
		f.mu.Unlock()
		return callback(mutator, mutationCtx)
	}, ctx)
}

func (f *memorySessionFacade) SetValue(address harnesstypes.Value, next any, ctx harnesstypes.Context) error {
	if err := f.begin(); err != nil {
		return err
	}
	defer f.end()
	return f.inner.SetValue(address, next, ctx)
}

func (f *memorySessionFacade) DeleteValue(address harnesstypes.Value, ctx harnesstypes.Context) error {
	if err := f.begin(); err != nil {
		return err
	}
	defer f.end()
	return f.inner.DeleteValue(address, ctx)
}

func (f *memorySessionFacade) AppendList(address harnesstypes.ValueList, element any, ctx harnesstypes.Context) error {
	if err := f.begin(); err != nil {
		return err
	}
	defer f.end()
	return f.inner.AppendList(address, element, ctx)
}

func (f *memorySessionFacade) DeleteList(address harnesstypes.ValueList, ctx harnesstypes.Context) error {
	if err := f.begin(); err != nil {
		return err
	}
	defer f.end()
	return f.inner.DeleteList(address, ctx)
}

func (f *memorySessionFacade) SetName(name *string, ctx harnesstypes.Context) error {
	if err := f.begin(); err != nil {
		return err
	}
	defer f.end()
	return f.inner.SetName(name, ctx)
}

func (f *memorySessionFacade) SetLabel(targetID string, label *string, ctx harnesstypes.Context) error {
	if err := f.begin(); err != nil {
		return err
	}
	defer f.end()
	return f.inner.SetLabel(targetID, label, ctx)
}

func (f *memorySessionFacade) Close(ctx harnesstypes.Context) error {
	f.mu.Lock()
	if f.state == "closed" {
		err := f.closeErr
		f.mu.Unlock()
		return err
	}
	f.state = "closing"
	f.mu.Unlock()
	f.once.Do(func() {
		f.admitted.Wait()
		f.mu.Lock()
		f.state = "closed"
		f.mu.Unlock()
		if f.markClosed != nil {
			f.markClosed()
		} else {
			f.record.open = false
		}
	})
	return nil
}

type memoryBranch struct {
	facade *memorySessionFacade
	inner  harnesstypes.Branch
}

func (b *memoryBranch) Name() string { return b.inner.Name() }

func (b *memoryBranch) GetTipID(ctx harnesstypes.Context) (*string, error) {
	if err := b.facade.begin(); err != nil {
		return nil, err
	}
	defer b.facade.end()
	return b.inner.GetTipID(ctx)
}

func (b *memoryBranch) FindEntries(query *harnesstypes.BranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	if err := b.facade.begin(); err != nil {
		return nil, err
	}
	defer b.facade.end()
	return b.inner.FindEntries(query, ctx)
}

func (b *memoryBranch) FindEntry(query *harnesstypes.BranchScan, ctx harnesstypes.Context) (harnesstypes.Entry, bool, error) {
	if err := b.facade.begin(); err != nil {
		return nil, false, err
	}
	defer b.facade.end()
	return b.inner.FindEntry(query, ctx)
}

func (b *memoryBranch) AppendMessage(message agenttypes.AgentMessage, ctx harnesstypes.Context) (string, error) {
	if err := b.facade.begin(); err != nil {
		return "", err
	}
	defer b.facade.end()
	return b.inner.AppendMessage(message, ctx)
}

func (b *memoryBranch) AppendCustomEntry(customType string, data harnesstypes.JsonValue, ctx harnesstypes.Context) (string, error) {
	if err := b.facade.begin(); err != nil {
		return "", err
	}
	defer b.facade.end()
	return b.inner.AppendCustomEntry(customType, data, ctx)
}

type memoryMutation struct {
	facade *memorySessionFacade
	inner  harnesstypes.SessionMutation
}

func (m *memoryMutation) Commit(writes []harnesstypes.Write, ctx harnesstypes.Context) (harnesstypes.CommitResult, error) {
	return m.inner.Commit(writes, ctx)
}

func (m *memoryMutation) End(ctx harnesstypes.Context) error {
	err := m.inner.End(ctx)
	m.facade.end()
	return err
}

func (m *memoryMutation) GetEntries(ids []string, ctx harnesstypes.Context) (map[string]harnesstypes.Entry, error) {
	return m.inner.GetEntries(ids, ctx)
}

func (m *memoryMutation) GetStats(ctx harnesstypes.Context) (harnesstypes.SessionStats, error) {
	return m.inner.GetStats(ctx)
}

func (m *memoryMutation) GetValue(address harnesstypes.Value, ctx harnesstypes.Context) (harnesstypes.StoredValue, bool, error) {
	return m.inner.GetValue(address, ctx)
}

func (m *memoryMutation) ScanValues(prefix harnesstypes.Value, ctx harnesstypes.Context) ([]harnesstypes.StoredValue, error) {
	return m.inner.ScanValues(prefix, ctx)
}

func (m *memoryMutation) ReadList(address harnesstypes.ValueList, options *harnesstypes.ListReadOptions, ctx harnesstypes.Context) ([]harnesstypes.ListElement, error) {
	return m.inner.ReadList(address, options, ctx)
}

func (m *memoryMutation) ScanBranch(query harnesstypes.StorageBranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error) {
	return m.inner.ScanBranch(query, ctx)
}

// MemorySessionRepo creates, opens, forks and deletes in-memory sessions.
type MemorySessionRepo struct {
	mu         sync.Mutex
	now        func() float64
	sessions   map[string]*memorySessionRecord
	pendingIDs map[string]bool
	closed     bool
	closeOnce  sync.Once
}

// NewMemorySessionRepo builds an empty repository.
func NewMemorySessionRepo(options *MemorySessionRepoOptions) *MemorySessionRepo {
	var now func() float64
	if options != nil {
		now = options.Now
	}
	return &MemorySessionRepo{
		now:        resolveNow(now),
		sessions:   map[string]*memorySessionRecord{},
		pendingIDs: map[string]bool{},
	}
}

func (r *MemorySessionRepo) assertOpen() error {
	if r.closed {
		return errors.New("MemorySessionRepo is closed")
	}
	return nil
}

func (r *MemorySessionRepo) reserveID(id string) error {
	if _, ok := r.sessions[id]; ok {
		return errors.New("Session already exists: " + id)
	}
	if r.pendingIDs[id] {
		return errors.New("Session already exists: " + id)
	}
	r.pendingIDs[id] = true
	return nil
}

// Create creates a fresh session.
func (r *MemorySessionRepo) Create(options harnesstypes.SessionCreateOptions, ctx harnesstypes.Context) (harnesstypes.Session[harnesstypes.SessionMetadata], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.assertOpen(); err != nil {
		return nil, err
	}
	createdAt := r.now()
	id := ""
	if options.ID != nil {
		id = *options.ID
	} else {
		generated, err := aiutils.UUIDv7(&createdAt)
		if err != nil {
			return nil, err
		}
		id = generated
	}
	if err := r.reserveID(id); err != nil {
		return nil, err
	}
	defer delete(r.pendingIDs, id)
	metadata := harnesstypes.SessionMetadata{ID: id, CreatedAt: createdAt, StorageVersion: memoryStorageVersion, ParentSessionID: options.ParentSessionID}
	storage := NewMemoryStorage(&MemoryStorageOptions{Now: r.now})
	record := &memorySessionRecord{metadata: metadata, storage: storage, open: true}
	session := NewStorageBackedSession(metadata, storage, StorageBackedSessionOptions{
		OnClose: func() {
			r.mu.Lock()
			record.open = false
			r.mu.Unlock()
		},
	})
	record.session = session
	r.sessions[id] = record
	return r.openRecord(record), nil
}

// Open reopens a previously closed session.
func (r *MemorySessionRepo) Open(metadata harnesstypes.SessionMetadata, ctx harnesstypes.Context) (harnesstypes.Session[harnesstypes.SessionMetadata], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.assertOpen(); err != nil {
		return nil, err
	}
	record, ok := r.sessions[metadata.ID]
	if !ok {
		return nil, errors.New("Unknown session: " + metadata.ID)
	}
	if record.open {
		return nil, errors.New("Session is already open: " + metadata.ID)
	}
	record.open = true
	return r.openRecord(record), nil
}

// List returns the metadata of every session.
func (r *MemorySessionRepo) List(options *struct{}, ctx harnesstypes.Context) ([]harnesstypes.SessionMetadata, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.assertOpen(); err != nil {
		return nil, err
	}
	out := make([]harnesstypes.SessionMetadata, 0, len(r.sessions))
	for _, record := range r.sessions {
		out = append(out, record.metadata)
	}
	return out, nil
}

// Delete removes a closed session.
func (r *MemorySessionRepo) Delete(metadata harnesstypes.SessionMetadata, ctx harnesstypes.Context) error {
	r.mu.Lock()
	record, ok := r.sessions[metadata.ID]
	if !ok {
		r.mu.Unlock()
		return errors.New("Unknown session: " + metadata.ID)
	}
	if record.open {
		r.mu.Unlock()
		return errors.New("Session is open: " + metadata.ID)
	}
	r.mu.Unlock()
	if err := record.session.Close(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.sessions, metadata.ID)
	r.mu.Unlock()
	return nil
}

// Fork copies a source session.
func (r *MemorySessionRepo) Fork(source harnesstypes.SessionMetadata, options harnesstypes.ForkOptions, ctx harnesstypes.Context) (harnesstypes.Session[harnesstypes.SessionMetadata], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.assertOpen(); err != nil {
		return nil, err
	}
	sourceRecord, ok := r.sessions[source.ID]
	if !ok {
		return nil, errors.New("Unknown session: " + source.ID)
	}
	createdAt := r.now()
	id := ""
	if options.ID != nil {
		id = *options.ID
	} else {
		generated, err := aiutils.UUIDv7(&createdAt)
		if err != nil {
			return nil, err
		}
		id = generated
	}
	if err := r.reserveID(id); err != nil {
		return nil, err
	}
	defer delete(r.pendingIDs, id)
	storage, err := sourceRecord.storage.Fork(options)
	if err != nil {
		return nil, err
	}
	parentID := sourceRecord.metadata.ID
	metadata := harnesstypes.SessionMetadata{ID: id, CreatedAt: createdAt, StorageVersion: memoryStorageVersion, ParentSessionID: &parentID}
	record := &memorySessionRecord{metadata: metadata, storage: storage, open: true}
	session := NewStorageBackedSession(metadata, storage, StorageBackedSessionOptions{
		OnClose: func() {
			r.mu.Lock()
			record.open = false
			r.mu.Unlock()
		},
	})
	record.session = session
	r.sessions[id] = record
	return r.openRecord(record), nil
}

// Close closes every session.
func (r *MemorySessionRepo) Close(ctx harnesstypes.Context) error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		records := make([]*memorySessionRecord, 0, len(r.sessions))
		for _, record := range r.sessions {
			records = append(records, record)
		}
		r.mu.Unlock()
		for _, record := range records {
			_ = record.session.Close(ctx)
		}
	})
	return nil
}
