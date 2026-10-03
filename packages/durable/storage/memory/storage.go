// Package memory implements the complete detached in-memory storage contract of
// the Pith Durable SDK. It imports the shared durable records and never imports
// an adapter. It is the reference backend used by storage conformance and by
// embedding code that does not need a file-backed store.
//
// Reads and retained writes are cloned intentionally to match the ownership
// boundary of serialization-backed stores. This is backend conformance, not
// semantic validation: the owning Session supplies valid records, references,
// ancestry, and transitions.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
)

// state is the complete detached in-memory record set. Every slice is kept in
// ascending ID order and every map value is owned by this storage.
type state struct {
	recordTypes map[durable.ID]string

	conversations                      map[durable.ConversationID]*durable.ConversationRecord
	conversationIDs                    []durable.ConversationID
	conversationIDsByOwnerConversation map[durable.ConversationID][]durable.ConversationID
	conversationIDsByOwnerTask         map[durable.TaskID][]durable.ConversationID

	entries         map[durable.EntryID]*durable.EntryRecord
	entryIDs        map[durable.ConversationID][]durable.EntryID
	headEntryIDs    map[durable.ConversationID][]durable.EntryID
	entryCommitSeqs map[durable.EntryID]durable.Seq

	tasks           map[durable.TaskID]*durable.TaskRecord
	taskIDs         []durable.TaskID
	taskIDsByStatus map[string][]durable.TaskID

	submissions            map[durable.SubmissionID]*durable.SubmissionRecord
	submissionIDs          []durable.SubmissionID
	submissionIDsByStatus  map[string][]durable.SubmissionID
	submissionIDsByRequest map[durable.ConversationID]map[string]durable.SubmissionID

	documents          map[durable.DocumentID]*storedDocumentState
	documentAddresses  map[string]*documentAddressIndex
	documentIDsByScope map[string][]durable.DocumentID
}

// Storage is the detached in-memory reference implementation of
// durable.Storage. It is safe for concurrent use.
type Storage struct {
	mu      sync.RWMutex
	state   state
	nextID  int64
	nextSeq int64
	closed  bool
}

// New returns a fresh empty in-memory storage. The reserved root conversation
// ID 1 is never handed out by MintID, which first returns 2.
func New() *Storage {
	return &Storage{
		state: state{
			recordTypes: map[durable.ID]string{},

			conversations:                      map[durable.ConversationID]*durable.ConversationRecord{},
			conversationIDs:                    []durable.ConversationID{},
			conversationIDsByOwnerConversation: map[durable.ConversationID][]durable.ConversationID{},
			conversationIDsByOwnerTask:         map[durable.TaskID][]durable.ConversationID{},

			entries:         map[durable.EntryID]*durable.EntryRecord{},
			entryIDs:        map[durable.ConversationID][]durable.EntryID{},
			headEntryIDs:    map[durable.ConversationID][]durable.EntryID{},
			entryCommitSeqs: map[durable.EntryID]durable.Seq{},

			tasks:   map[durable.TaskID]*durable.TaskRecord{},
			taskIDs: []durable.TaskID{},
			taskIDsByStatus: map[string][]durable.TaskID{
				durable.TaskStatusPending:    {},
				durable.TaskStatusRunning:    {},
				durable.TaskStatusWaiting:    {},
				durable.TaskStatusCompleting: {},
				durable.TaskStatusTerminal:   {},
			},

			submissions:   map[durable.SubmissionID]*durable.SubmissionRecord{},
			submissionIDs: []durable.SubmissionID{},
			submissionIDsByStatus: map[string][]durable.SubmissionID{
				durable.SubmissionStatusQueued:     {},
				durable.SubmissionStatusPlaced:     {},
				durable.SubmissionStatusDone:       {},
				durable.SubmissionStatusUnanswered: {},
			},
			submissionIDsByRequest: map[durable.ConversationID]map[string]durable.SubmissionID{},

			documents:          map[durable.DocumentID]*storedDocumentState{},
			documentAddresses:  map[string]*documentAddressIndex{},
			documentIDsByScope: map[string][]durable.DocumentID{},
		},
		nextID:  2,
		nextSeq: 1,
	}
}

// PreparedCommit is a fully validated, detached state mutation whose
// application performs no fallible preparation. It is exposed for tests that
// verify retained writes are detached from the mutation applied to storage.
type PreparedCommit struct {
	// Seq is the commit sequence this mutation will publish.
	Seq durable.Seq
	// Writes are a detached snapshot exposed for inspection. Mutating them does
	// not change the mutation that Apply publishes.
	Writes []durable.StorageWrite

	storage *Storage
	writes  []durable.StorageWrite
	actions map[durable.DocumentID]*documentAction
	applied bool
}

// Apply publishes the prepared mutation once and returns its sequence. Repeated
// calls return the same sequence without applying again. It is immune to
// mutation of the exposed Writes snapshot.
func (p *PreparedCommit) Apply() durable.Seq {
	p.storage.mu.Lock()
	defer p.storage.mu.Unlock()
	if p.applied {
		return p.Seq
	}
	p.applied = true
	p.storage.applyPreparedCommit(p.writes, p.actions, p.Seq)
	return p.Seq
}

// PrepareCommit validates and detaches one commit without changing observable
// state. The returned Writes snapshot is independent of the mutation that Apply
// publishes.
func (s *Storage) PrepareCommit(writes []durable.StorageWrite) (*PreparedCommit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	prepared, actions, seq, err := s.prepareCommit(writes, s.nextSeq)
	if err != nil {
		return nil, err
	}
	exposed, err := detachWrites(prepared)
	if err != nil {
		return nil, err
	}
	return &PreparedCommit{Seq: durable.Seq(seq), Writes: exposed, writes: prepared, storage: s, actions: actions}, nil
}

// PrepareCommitAt is PrepareCommit for one explicit commit sequence. JSONL
// recovery uses it to replay confirmed markers in their recorded order,
// including permitted sequence gaps.
func (s *Storage) PrepareCommitAt(seq durable.Seq, writes []durable.StorageWrite) (*PreparedCommit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	prepared, actions, resolvedSeq, err := s.prepareCommit(writes, int64(seq))
	if err != nil {
		return nil, err
	}
	exposed, err := detachWrites(prepared)
	if err != nil {
		return nil, err
	}
	return &PreparedCommit{Seq: durable.Seq(resolvedSeq), Writes: exposed, writes: prepared, storage: s, actions: actions}, nil
}

// Commit implements durable.Storage.
func (s *Storage) Commit(ctx context.Context, writes []durable.StorageWrite) (durable.Seq, error) {
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return 0, err
	}
	prepared, actions, seq, err := s.prepareCommit(writes, s.nextSeq)
	if err != nil {
		return 0, err
	}
	s.applyPreparedCommit(prepared, actions, durable.Seq(seq))
	return durable.Seq(seq), nil
}

// MintID implements durable.Storage.
func (s *Storage) MintID(ctx context.Context) (durable.ID, error) {
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertOpen(); err != nil {
		return 0, err
	}
	if s.nextID > durable.MaxSafeInteger {
		return 0, fmt.Errorf("ID space is exhausted")
	}
	id := durable.ID(s.nextID)
	s.nextID++
	return id, nil
}

// Conversation implements durable.Storage.
func (s *Storage) Conversation(ctx context.Context, id durable.ConversationID) (*durable.ConversationRecord, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	value := s.state.conversations[id]
	if value == nil {
		return nil, nil
	}
	return cloneConversation(value), nil
}

// ScanConversations implements durable.Storage.
func (s *Storage) ScanConversations(ctx context.Context, query durable.ConversationQuery, limit int, cursor durable.Cursor) (durable.Page[durable.ConversationRecord], error) {
	if err := ctxErr(ctx); err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	var ids []durable.ConversationID
	switch {
	case query.OwnerTaskID != nil:
		ids = s.state.conversationIDsByOwnerTask[*query.OwnerTaskID]
	case query.OwnerConversationID != nil:
		ids = s.state.conversationIDsByOwnerConversation[*query.OwnerConversationID]
	default:
		ids = s.state.conversationIDs
	}
	start := 0
	if after != nil {
		start = upperBound(ids, durable.ConversationID(*after))
	}
	values := []durable.ConversationRecord{}
	for index := start; index < len(ids) && len(values) <= limit; index++ {
		value := s.state.conversations[ids[index]]
		if value == nil {
			continue
		}
		if query.OwnerConversationID != nil && (value.Owner == nil || value.Owner.ConversationID != *query.OwnerConversationID) {
			continue
		}
		values = append(values, *cloneConversation(value))
	}
	return pageItems(values, limit, func(value durable.ConversationRecord) durable.ID { return value.ID }), nil
}

// Entry implements durable.Storage.
func (s *Storage) Entry(ctx context.Context, id durable.EntryID) (*durable.StoredEntry, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	entry := s.state.entries[id]
	if entry == nil {
		return nil, nil
	}
	return &durable.StoredEntry{Entry: *cloneEntry(entry), CommitSeq: s.state.entryCommitSeqs[id]}, nil
}

// VisibleEntry implements durable.Storage.
func (s *Storage) VisibleEntry(ctx context.Context, conversationID durable.ConversationID, id durable.EntryID) (*durable.StoredEntry, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	entries, err := s.visibleEntries(conversationID, &id, &id)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return &durable.StoredEntry{Entry: *cloneEntry(entries[0]), CommitSeq: s.state.entryCommitSeqs[id]}, nil
}

// FindLatestHeadMarker implements durable.Storage.
func (s *Storage) FindLatestHeadMarker(ctx context.Context, conversationID durable.ConversationID, atOrBefore *durable.EntryID) (*durable.EntryRecord, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	if s.state.conversations[conversationID] == nil {
		return nil, fmt.Errorf("Unknown conversation: %d", conversationID)
	}
	currentID := conversationID
	upper := durable.MaxSafeInteger
	if atOrBefore != nil {
		upper = int64(*atOrBefore)
	}
	for {
		ids := s.state.headEntryIDs[currentID]
		index := upperBound(ids, durable.EntryID(upper)) - 1
		if index >= 0 {
			entry := s.state.entries[ids[index]]
			if entry != nil && entry.Head != nil {
				head := *entry.Head
				clone := *cloneEntry(entry)
				clone.Head = &head
				return &clone, nil
			}
		}
		conversation := s.state.conversations[currentID]
		if conversation == nil || conversation.Parent == nil {
			return nil, nil
		}
		if int64(conversation.Parent.At) < upper {
			upper = int64(conversation.Parent.At)
		}
		currentID = conversation.Parent.ConversationID
	}
}

// ScanEntries implements durable.Storage.
func (s *Storage) ScanEntries(ctx context.Context, query durable.EntryQuery, limit int, cursor durable.Cursor) (durable.Page[durable.EntryRecord], error) {
	if err := ctxErr(ctx); err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	maxID := query.MaxEntryID
	if after != nil {
		bound := durable.EntryID(*after - 1)
		if maxID == nil || bound < *maxID {
			maxID = &bound
		}
	}
	entries, err := s.visibleEntries(query.ConversationID, query.MinEntryID, maxID)
	if err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	values := []durable.EntryRecord{}
	for _, entry := range entries {
		if len(values) > limit {
			break
		}
		values = append(values, *cloneEntry(entry))
	}
	return pageItems(values, limit, func(value durable.EntryRecord) durable.ID { return value.ID }), nil
}

// Task implements durable.Storage.
func (s *Storage) Task(ctx context.Context, id durable.TaskID) (*durable.TaskRecord, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	value := s.state.tasks[id]
	if value == nil {
		return nil, nil
	}
	return cloneTask(value), nil
}

// ScanTasks implements durable.Storage.
func (s *Storage) ScanTasks(ctx context.Context, query durable.TaskQuery, limit int, cursor durable.Cursor) (durable.Page[durable.TaskRecord], error) {
	if err := ctxErr(ctx); err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	ids := s.state.taskIDs
	if query.Status != "" {
		ids = s.state.taskIDsByStatus[query.Status]
	}
	start := 0
	if after != nil {
		start = upperBound(ids, durable.TaskID(*after))
	}
	values := []durable.TaskRecord{}
	for index := start; index < len(ids) && len(values) <= limit; index++ {
		value := s.state.tasks[ids[index]]
		if value == nil {
			continue
		}
		if query.ConversationID != nil && value.ConversationID != *query.ConversationID {
			continue
		}
		if query.Kind != "" && value.Kind != query.Kind {
			continue
		}
		if query.AbortRequested != nil && value.AbortRequested != *query.AbortRequested {
			continue
		}
		if query.Background != nil && value.Background != *query.Background {
			continue
		}
		values = append(values, *cloneTask(value))
	}
	return pageItems(values, limit, func(value durable.TaskRecord) durable.ID { return value.ID }), nil
}

// Submission implements durable.Storage.
func (s *Storage) Submission(ctx context.Context, id durable.SubmissionID) (*durable.SubmissionRecord, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	value := s.state.submissions[id]
	if value == nil {
		return nil, nil
	}
	return cloneSubmission(value), nil
}

// ScanSubmissions implements durable.Storage.
func (s *Storage) ScanSubmissions(ctx context.Context, query durable.SubmissionQuery, limit int, cursor durable.Cursor) (durable.Page[durable.SubmissionRecord], error) {
	if err := ctxErr(ctx); err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	ids := s.state.submissionIDs
	if query.Status != "" {
		ids = s.state.submissionIDsByStatus[query.Status]
	}
	start := 0
	if after != nil {
		start = upperBound(ids, durable.SubmissionID(*after))
	}
	values := []durable.SubmissionRecord{}
	for index := start; index < len(ids) && len(values) <= limit; index++ {
		value := s.state.submissions[ids[index]]
		if value == nil {
			continue
		}
		if query.ConversationID != nil && value.ConversationID != *query.ConversationID {
			continue
		}
		values = append(values, *cloneSubmission(value))
	}
	return pageItems(values, limit, func(value durable.SubmissionRecord) durable.ID { return value.ID }), nil
}

// SubmissionByRequest implements durable.Storage.
func (s *Storage) SubmissionByRequest(ctx context.Context, conversationID durable.ConversationID, requestID string) (*durable.SubmissionRecord, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	id, ok := s.state.submissionIDsByRequest[conversationID][requestID]
	if !ok {
		return nil, nil
	}
	value := s.state.submissions[id]
	if value == nil {
		return nil, nil
	}
	return cloneSubmission(value), nil
}

// FindDocument implements durable.Storage.
func (s *Storage) FindDocument(ctx context.Context, address durable.DocumentAddress, at durable.DocumentPoint) (*durable.DocumentRecord, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	index := s.state.documentAddresses[addressKey(address)]
	if at.Current {
		if index == nil || index.currentID == nil {
			return nil, nil
		}
		stored := s.state.documents[*index.currentID]
		if stored == nil {
			return nil, nil
		}
		record := cloneDocumentRecord(stored.record)
		return &record, nil
	}
	if index != nil {
		for _, id := range index.ids {
			stored := s.state.documents[id]
			if stored == nil {
				continue
			}
			if isAliveAt(stored.record, at) {
				record := cloneDocumentRecord(stored.record)
				return &record, nil
			}
		}
	}
	return nil, nil
}

// Document implements durable.Storage.
func (s *Storage) Document(ctx context.Context, id durable.DocumentID, at durable.DocumentPoint) (*durable.StoredDocument, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	stored, err := s.materializeDocument(id, at)
	if err != nil {
		return nil, err
	}
	if stored == nil {
		return nil, nil
	}
	return &durable.StoredDocument{
		Record:          cloneDocumentRecord(stored.Record),
		Version:         stored.Version,
		Value:           stored.Value,
		DeltasSinceBase: stored.DeltasSinceBase,
	}, nil
}

// ScanDocuments implements durable.Storage.
func (s *Storage) ScanDocuments(ctx context.Context, query durable.DocumentQuery, limit int, cursor durable.Cursor) (durable.Page[durable.DocumentRecord], error) {
	if err := ctxErr(ctx); err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	ids := s.state.documentIDsByScope[scopeKey(query.Scope)]
	start := 0
	if after != nil {
		start = upperBound(ids, durable.DocumentID(*after))
	}
	values := []durable.DocumentRecord{}
	for index := start; index < len(ids) && len(values) <= limit; index++ {
		stored := s.state.documents[ids[index]]
		if stored == nil {
			continue
		}
		if query.Kind != "" && stored.record.Kind != query.Kind {
			continue
		}
		if isAliveAt(stored.record, query.At) {
			values = append(values, cloneDocumentRecord(stored.record))
		}
	}
	return pageItems(values, limit, func(value durable.DocumentRecord) durable.ID { return value.ID }), nil
}

// Close implements durable.Storage. It is idempotent; every later operation
// rejects.
func (s *Storage) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// visibleEntries walks the fork-aware ancestry of one conversation newest-first,
// applying every ancestor cap and the inclusive ID bounds. It assumes the caller
// holds at least a read lock.
func (s *Storage) visibleEntries(conversationID durable.ConversationID, minID, maxID *durable.EntryID) ([]*durable.EntryRecord, error) {
	if s.state.conversations[conversationID] == nil {
		return nil, fmt.Errorf("Unknown conversation: %d", conversationID)
	}
	upper := durable.MaxSafeInteger
	lower := int64(0)
	if maxID != nil {
		upper = int64(*maxID)
	}
	if minID != nil {
		lower = int64(*minID)
	}
	out := []*durable.EntryRecord{}
	currentID := conversationID
	for {
		ids := s.state.entryIDs[currentID]
		for index := upperBound(ids, durable.EntryID(upper)) - 1; index >= 0; index-- {
			id := ids[index]
			if int64(id) < lower {
				break
			}
			if entry := s.state.entries[id]; entry != nil {
				out = append(out, entry)
			}
		}
		conversation := s.state.conversations[currentID]
		if conversation == nil || conversation.Parent == nil {
			break
		}
		if int64(conversation.Parent.At) < upper {
			upper = int64(conversation.Parent.At)
		}
		if upper < lower {
			break
		}
		currentID = conversation.Parent.ConversationID
	}
	return out, nil
}

func (s *Storage) assertOpen() error {
	if s.closed {
		return &durable.ClosedError{Name: "MemoryStorage"}
	}
	return nil
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// prepareCommit validates and detaches one commit without changing observable
// state.
func (s *Storage) prepareCommit(writes []durable.StorageWrite, nextSeq int64) ([]durable.StorageWrite, map[durable.DocumentID]*documentAction, int64, error) {
	seq := nextSeq
	if seq < 1 || seq > durable.MaxSafeInteger {
		return nil, nil, 0, fmt.Errorf("Commit sequence %d does not strictly increase", seq)
	}
	detached, err := detachWrites(writes)
	if err != nil {
		return nil, nil, 0, err
	}
	resolved, err := s.resolveDocumentCopies(detached)
	if err != nil {
		return nil, nil, 0, err
	}
	if err := s.checkGlobalIDs(resolved); err != nil {
		return nil, nil, 0, err
	}
	actions, err := s.prepareDocumentActions(resolved)
	if err != nil {
		return nil, nil, 0, err
	}
	if err := s.checkDocumentActions(actions); err != nil {
		return nil, nil, 0, err
	}
	return resolved, actions, seq, nil
}

// checkGlobalIDs enforces one global record-ID namespace and immutable
// conversation/entry/document creation.
func (s *Storage) checkGlobalIDs(writes []durable.StorageWrite) error {
	claimed := map[durable.ID]string{}
	for _, write := range writes {
		var table string
		var id durable.ID
		switch write.Type {
		case durable.WriteDocumentChange, durable.WriteDocumentRetire:
			continue
		case durable.WriteDocumentCreate, durable.WriteDocumentCopy:
			if write.Record == nil {
				return fmt.Errorf("storage write %q is missing a record", write.Type)
			}
			table, id = "document", write.Record.ID
		case durable.WriteConversation:
			if write.Conversation == nil {
				return fmt.Errorf("storage write %q is missing a record", write.Type)
			}
			table, id = "conversation", write.Conversation.ID
		case durable.WriteEntry:
			if write.Entry == nil {
				return fmt.Errorf("storage write %q is missing a record", write.Type)
			}
			table, id = "entry", write.Entry.ID
		case durable.WriteTask:
			if write.Task == nil {
				return fmt.Errorf("storage write %q is missing a record", write.Type)
			}
			table, id = "task", write.Task.ID
		case durable.WriteSubmission:
			if write.Submission == nil {
				return fmt.Errorf("storage write %q is missing a record", write.Type)
			}
			table, id = "submission", write.Submission.ID
		default:
			return fmt.Errorf("unknown storage write type %q", write.Type)
		}
		existing := s.state.recordTypes[id]
		earlier := claimed[id]
		if table == "conversation" || table == "entry" || table == "document" {
			if existing != "" {
				return fmt.Errorf("ID %d already belongs to %s", id, existing)
			}
			if earlier != "" {
				return fmt.Errorf("ID %d is written more than once", id)
			}
		} else {
			if existing != "" && existing != table {
				return fmt.Errorf("ID %d already belongs to %s", id, existing)
			}
			if earlier != "" && earlier != table {
				return fmt.Errorf("ID %d is written as two record types", id)
			}
		}
		claimed[id] = table
	}
	return nil
}

// applyPreparedCommit applies one validated batch. It performs no fallible
// preparation.
func (s *Storage) applyPreparedCommit(writes []durable.StorageWrite, actions map[durable.DocumentID]*documentAction, seq durable.Seq) {
	for _, write := range writes {
		switch write.Type {
		case durable.WriteConversation:
			record := write.Conversation
			s.state.recordTypes[record.ID] = "conversation"
			s.state.conversations[record.ID] = record
			s.state.conversationIDs = insertSorted(s.state.conversationIDs, record.ID)
			if record.Owner != nil {
				insertMapID(s.state.conversationIDsByOwnerConversation, record.Owner.ConversationID, record.ID)
				insertMapID(s.state.conversationIDsByOwnerTask, record.Owner.TaskID, record.ID)
			}
			s.bumpID(record.ID)
		case durable.WriteEntry:
			record := write.Entry
			s.state.recordTypes[record.ID] = "entry"
			s.state.entries[record.ID] = record
			s.state.entryCommitSeqs[record.ID] = seq
			insertMapID(s.state.entryIDs, record.ConversationID, record.ID)
			if record.Head != nil {
				insertMapID(s.state.headEntryIDs, record.ConversationID, record.ID)
			}
			s.bumpID(record.ID)
		case durable.WriteTask:
			record := write.Task
			s.state.recordTypes[record.ID] = "task"
			previous := s.state.tasks[record.ID]
			if previous == nil {
				s.state.taskIDs = insertSorted(s.state.taskIDs, record.ID)
				s.state.taskIDsByStatus[record.State.Status] = insertSorted(s.state.taskIDsByStatus[record.State.Status], record.ID)
			} else if previous.State.Status != record.State.Status {
				s.state.taskIDsByStatus[previous.State.Status] = removeSorted(s.state.taskIDsByStatus[previous.State.Status], record.ID)
				s.state.taskIDsByStatus[record.State.Status] = insertSorted(s.state.taskIDsByStatus[record.State.Status], record.ID)
			}
			s.state.tasks[record.ID] = record
			s.bumpID(record.ID)
		case durable.WriteSubmission:
			record := write.Submission
			s.state.recordTypes[record.ID] = "submission"
			previous := s.state.submissions[record.ID]
			if previous == nil {
				s.state.submissionIDs = insertSorted(s.state.submissionIDs, record.ID)
				s.state.submissionIDsByStatus[record.Status] = insertSorted(s.state.submissionIDsByStatus[record.Status], record.ID)
			} else if previous.Status != record.Status {
				s.state.submissionIDsByStatus[previous.Status] = removeSorted(s.state.submissionIDsByStatus[previous.Status], record.ID)
				s.state.submissionIDsByStatus[record.Status] = insertSorted(s.state.submissionIDsByStatus[record.Status], record.ID)
			}
			if previous != nil && previous.RequestID != nil {
				requests := s.state.submissionIDsByRequest[previous.ConversationID]
				if requests != nil && requests[*previous.RequestID] == record.ID {
					delete(requests, *previous.RequestID)
					if len(requests) == 0 {
						delete(s.state.submissionIDsByRequest, previous.ConversationID)
					}
				}
			}
			s.state.submissions[record.ID] = record
			if record.RequestID != nil {
				requests := s.state.submissionIDsByRequest[record.ConversationID]
				if requests == nil {
					requests = map[string]durable.SubmissionID{}
					s.state.submissionIDsByRequest[record.ConversationID] = requests
				}
				requests[*record.RequestID] = record.ID
			}
			s.bumpID(record.ID)
		}
	}
	s.applyDocumentActions(actions, seq)
	if int64(seq)+1 > s.nextSeq {
		s.nextSeq = int64(seq) + 1
	}
}

func (s *Storage) bumpID(id durable.ID) {
	if int64(id)+1 > s.nextID {
		s.nextID = int64(id) + 1
	}
}

// detachWrites deep-copies every retained input so storage never aliases caller
// memory.
func detachWrites(writes []durable.StorageWrite) ([]durable.StorageWrite, error) {
	out := make([]durable.StorageWrite, len(writes))
	for index, write := range writes {
		clone, err := detachWrite(write)
		if err != nil {
			return nil, err
		}
		out[index] = clone
	}
	return out, nil
}

func detachWrite(write durable.StorageWrite) (durable.StorageWrite, error) {
	out := write
	out.Conversation = cloneConversation(write.Conversation)
	out.Entry = cloneEntry(write.Entry)
	out.Task = cloneTask(write.Task)
	out.Submission = cloneSubmission(write.Submission)
	out.Record = cloneCreate(write.Record)
	out.Source = cloneCopySource(write.Source)
	if write.Content != nil {
		content, err := cloneContent(write.Content)
		if err != nil {
			return out, err
		}
		out.Content = content
	}
	return out, nil
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	out := make(json.RawMessage, len(value))
	copy(out, value)
	return out
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneID(value *durable.ID) *durable.ID {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneSeq(value *durable.Seq) *durable.Seq {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneConversation(value *durable.ConversationRecord) *durable.ConversationRecord {
	if value == nil {
		return nil
	}
	out := *value
	if value.Parent != nil {
		parent := *value.Parent
		out.Parent = &parent
	}
	if value.Owner != nil {
		owner := *value.Owner
		out.Owner = &owner
	}
	return &out
}

func cloneMessages(messages []types.Message) []types.Message {
	if messages == nil {
		return nil
	}
	data, err := json.Marshal(messages)
	if err != nil {
		return messages
	}
	var out []types.Message
	if err := json.Unmarshal(data, &out); err != nil {
		return messages
	}
	return out
}

func cloneEdits(edits []durable.ContextEdit) []durable.ContextEdit {
	if edits == nil {
		return nil
	}
	out := make([]durable.ContextEdit, len(edits))
	for index, edit := range edits {
		out[index] = edit
		out[index].Messages = cloneMessages(edit.Messages)
	}
	return out
}

func cloneEntry(value *durable.EntryRecord) *durable.EntryRecord {
	if value == nil {
		return nil
	}
	out := *value
	out.Model = cloneMessages(value.Model)
	out.Data = cloneRaw(value.Data)
	out.Head = cloneID(value.Head)
	out.Edits = cloneEdits(value.Edits)
	out.ByTaskID = cloneID(value.ByTaskID)
	return &out
}

func cloneOutcome(value *durable.TaskOutcome) *durable.TaskOutcome {
	if value == nil {
		return nil
	}
	out := *value
	out.Result = cloneRaw(value.Result)
	if value.Error != nil {
		errClone := *value.Error
		errClone.Detail = cloneRaw(value.Error.Detail)
		out.Error = &errClone
	}
	return &out
}

func cloneTaskState(value durable.TaskState) durable.TaskState {
	out := value
	out.Checkpoint = cloneRaw(value.Checkpoint)
	out.Outcome = cloneOutcome(value.Outcome)
	if value.On != nil {
		out.On = append([]durable.TaskID(nil), value.On...)
	}
	return out
}

func cloneMemos(memos map[string]json.RawMessage) map[string]json.RawMessage {
	if memos == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(memos))
	for name, value := range memos {
		out[name] = cloneRaw(value)
	}
	return out
}

func cloneTask(value *durable.TaskRecord) *durable.TaskRecord {
	if value == nil {
		return nil
	}
	out := *value
	out.Input = cloneRaw(value.Input)
	out.Owner = cloneID(value.Owner)
	out.State = cloneTaskState(value.State)
	out.Memos = cloneMemos(value.Memos)
	return &out
}

func cloneSubmission(value *durable.SubmissionRecord) *durable.SubmissionRecord {
	if value == nil {
		return nil
	}
	out := *value
	out.RequestID = cloneString(value.RequestID)
	out.Entry = cloneID(value.Entry)
	out.Answer = cloneID(value.Answer)
	out.Detail = cloneRaw(value.Detail)
	return &out
}

func cloneCreate(value *durable.DocumentCreate) *durable.DocumentCreate {
	if value == nil {
		return nil
	}
	out := *value
	out.Key = cloneString(value.Key)
	return &out
}

func cloneCopySource(value *durable.DocumentCopySource) *durable.DocumentCopySource {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneDocumentRecord(value durable.DocumentRecord) durable.DocumentRecord {
	out := value
	out.Key = cloneString(value.Key)
	out.RetiredAt = cloneSeq(value.RetiredAt)
	return out
}

func cloneContent(value *durable.DocumentContent) (*durable.DocumentContent, error) {
	if value == nil {
		return nil, nil
	}
	out := *value
	if value.Value != nil {
		copied, err := chord.CopyJSON(map[string]any(value.Value))
		if err != nil {
			return nil, fmt.Errorf("document content value is not strict JSON: %w", err)
		}
		object, ok := copied.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("document content value is not a JSON object")
		}
		out.Value = durable.JsonObject(object)
	}
	if value.Ops != nil {
		ops, err := cloneOps(value.Ops)
		if err != nil {
			return nil, err
		}
		out.Ops = ops
	}
	return &out, nil
}

func cloneOps(ops []chord.Op) ([]chord.Op, error) {
	out := make([]chord.Op, len(ops))
	for index, op := range ops {
		copied, err := chord.CopyJSON([]any(op))
		if err != nil {
			return nil, fmt.Errorf("document operation is not strict JSON: %w", err)
		}
		slice, ok := copied.([]any)
		if !ok {
			return nil, fmt.Errorf("document operation is not a tuple")
		}
		out[index] = chord.Op(slice)
	}
	return out, nil
}
