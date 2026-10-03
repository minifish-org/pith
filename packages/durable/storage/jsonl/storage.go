// Package jsonl implements the durable.Storage contract over an append-only
// JSONL directory. It is the native port of Pi
// packages/durable/src/storage/jsonl/storage.ts at revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32.
//
// The adapter keeps the in-memory reference store as its working set and
// publishes every commit to disk as: one append of prepared records to each
// affected sidecar (doc-<id>.jsonl / task-<id>.jsonl), then exactly one main
// marker line (main.jsonl). Readers always observe committed, detached state
// through the memory store; recovery replays only confirmed markers.
//
// # On-disk format
//
// The format is version 1 with the upstream lower-camel JSON field names. The
// main marker is
//
//	{"format":1,"type":"commit","seq":N,"writes":[...]}
//
// and each sidecar line is
//
//	{"format":1,"type":"record","seq":N,"ordinal":M,"payload":{...}}
//
// # Documented host/runtime differences
//
//   - Go does not provide a language-level revocable draft; retained writes and
//     every read are detached by the memory store.
//   - Object key order is not significant and is not reproduced byte for byte.
//   - Default mode promises ordinary process-crash consistency, not power-loss
//     durability; enable Fsync for sidecar-before-marker ordering.
package jsonl

import (
	"context"
	"sync"

	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/env"
	"github.com/minifish-org/pith/packages/durable/storage/memory"
)

// Options configures a JSONL storage directory.
type Options struct {
	// Fsync flushes every affected sidecar before appending the main marker and
	// flushes the marker before destructive reclamation. Its zero value is
	// false.
	Fsync bool
	// FileSystem is the filesystem the adapter publishes through. A nil value
	// selects the ordinary local filesystem.
	FileSystem env.FileSystem
}

// Storage is the JSONL durable.Storage implementation. It is safe for
// concurrent use.
type Storage struct {
	fs        env.FileSystem
	directory string
	mainPath  string
	fsync     bool

	mu     sync.RWMutex
	memory *memory.Storage

	currentOnlyDocuments map[durable.DocumentID]struct{}
	liveTaskSidecars     map[durable.TaskID]struct{}

	closed    bool
	poisonErr *PoisonedError
}

// PoisonedError reports that an uncertain append or sync failure left the open
// backend in an unknown state. No further read or write may imply a known
// consistent state until reopen. The optional cause is exposed through Unwrap.
type PoisonedError struct {
	Message string
	Cause   error
}

func (e *PoisonedError) Error() string { return e.Message }

// Unwrap exposes the optional underlying cause.
func (e *PoisonedError) Unwrap() error { return e.Cause }

// Open opens or creates a JSONL storage directory. A nil options.FileSystem
// selects the ordinary local filesystem.
func Open(ctx context.Context, directory string, options Options) (*Storage, error) {
	fs := options.FileSystem
	if fs == nil {
		local, err := env.NewLocal("")
		if err != nil {
			return nil, err
		}
		fs = local
	}
	absolute, err := fs.AbsolutePath(ctx, directory)
	if err != nil {
		return nil, errorFromFile("path resolution", err)
	}
	if err := fs.CreateDir(ctx, absolute, true); err != nil {
		return nil, errorFromFile("directory creation", err)
	}
	mainPath, err := fs.JoinPath(ctx, absolute, mainFileName)
	if err != nil {
		return nil, errorFromFile("path join", err)
	}
	storage := &Storage{
		fs:                   fs,
		directory:            absolute,
		mainPath:             mainPath,
		fsync:                options.Fsync,
		memory:               memory.New(),
		currentOnlyDocuments: map[durable.DocumentID]struct{}{},
		liveTaskSidecars:     map[durable.TaskID]struct{}{},
	}
	if err := storage.recover(ctx); err != nil {
		return nil, err
	}
	return storage, nil
}

// Commit implements durable.Storage. One commit appends prepared records to
// every affected sidecar, then one main marker, then publishes memory.
func (s *Storage) Commit(ctx context.Context, writes []durable.StorageWrite) (durable.Seq, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.assertUsable(); err != nil {
		return 0, err
	}
	prepared, err := s.memory.PrepareCommit(writes)
	if err != nil {
		return 0, err
	}
	encoded, err := encodeCommit(prepared.Seq, prepared.Writes)
	if err != nil {
		return 0, err
	}
	replacements := s.planReclamations(prepared.Writes, encoded)

	type pendingSidecar struct {
		name    string
		path    string
		content []byte
	}
	pending := make([]pendingSidecar, 0, len(encoded.sidecars))
	for _, sidecar := range encoded.sidecars {
		path, err := s.resolveFile(sidecar.name, ctx)
		if err != nil {
			return 0, err
		}
		pending = append(pending, pendingSidecar{name: sidecar.name, path: path, content: sidecar.content})
	}

	for _, sidecar := range pending {
		if err := s.fs.AppendFile(ctx, sidecar.path, sidecar.content); err != nil {
			return 0, s.poison(errorFromFile("append to "+sidecar.name, err))
		}
	}
	if s.fsync {
		for _, sidecar := range pending {
			if err := s.fs.FlushFile(ctx, sidecar.path); err != nil {
				return 0, s.poison(errorFromFile("flush of "+sidecar.name, err))
			}
		}
	}
	if err := s.fs.AppendFile(ctx, s.mainPath, encoded.marker); err != nil {
		return 0, s.poison(errorFromFile("append to "+mainFileName, err))
	}
	seq := prepared.Apply()
	s.adoptSidecarState(prepared.Writes)
	s.reclaimSidecars(replacements, ctx)
	return seq, nil
}

// MintID implements durable.Storage.
func (s *Storage) MintID(ctx context.Context) (durable.ID, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return 0, err
	}
	return s.memory.MintID(ctx)
}

// Conversation implements durable.Storage.
func (s *Storage) Conversation(ctx context.Context, id durable.ConversationID) (*durable.ConversationRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.Conversation(ctx, id)
}

// ScanConversations implements durable.Storage.
func (s *Storage) ScanConversations(ctx context.Context, query durable.ConversationQuery, limit int, cursor durable.Cursor) (durable.Page[durable.ConversationRecord], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	return s.memory.ScanConversations(ctx, query, limit, cursor)
}

// Entry implements durable.Storage.
func (s *Storage) Entry(ctx context.Context, id durable.EntryID) (*durable.StoredEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.Entry(ctx, id)
}

// VisibleEntry implements durable.Storage.
func (s *Storage) VisibleEntry(ctx context.Context, conversationID durable.ConversationID, id durable.EntryID) (*durable.StoredEntry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.VisibleEntry(ctx, conversationID, id)
}

// FindLatestHeadMarker implements durable.Storage.
func (s *Storage) FindLatestHeadMarker(ctx context.Context, conversationID durable.ConversationID, atOrBefore *durable.EntryID) (*durable.EntryRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.FindLatestHeadMarker(ctx, conversationID, atOrBefore)
}

// ScanEntries implements durable.Storage.
func (s *Storage) ScanEntries(ctx context.Context, query durable.EntryQuery, limit int, cursor durable.Cursor) (durable.Page[durable.EntryRecord], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	return s.memory.ScanEntries(ctx, query, limit, cursor)
}

// Task implements durable.Storage.
func (s *Storage) Task(ctx context.Context, id durable.TaskID) (*durable.TaskRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.Task(ctx, id)
}

// ScanTasks implements durable.Storage.
func (s *Storage) ScanTasks(ctx context.Context, query durable.TaskQuery, limit int, cursor durable.Cursor) (durable.Page[durable.TaskRecord], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	return s.memory.ScanTasks(ctx, query, limit, cursor)
}

// Submission implements durable.Storage.
func (s *Storage) Submission(ctx context.Context, id durable.SubmissionID) (*durable.SubmissionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.Submission(ctx, id)
}

// ScanSubmissions implements durable.Storage.
func (s *Storage) ScanSubmissions(ctx context.Context, query durable.SubmissionQuery, limit int, cursor durable.Cursor) (durable.Page[durable.SubmissionRecord], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	return s.memory.ScanSubmissions(ctx, query, limit, cursor)
}

// SubmissionByRequest implements durable.Storage.
func (s *Storage) SubmissionByRequest(ctx context.Context, conversationID durable.ConversationID, requestID string) (*durable.SubmissionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.SubmissionByRequest(ctx, conversationID, requestID)
}

// FindDocument implements durable.Storage.
func (s *Storage) FindDocument(ctx context.Context, address durable.DocumentAddress, at durable.DocumentPoint) (*durable.DocumentRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.FindDocument(ctx, address, at)
}

// Document implements durable.Storage.
func (s *Storage) Document(ctx context.Context, id durable.DocumentID, at durable.DocumentPoint) (*durable.StoredDocument, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return nil, err
	}
	return s.memory.Document(ctx, id, at)
}

// ScanDocuments implements durable.Storage.
func (s *Storage) ScanDocuments(ctx context.Context, query durable.DocumentQuery, limit int, cursor durable.Cursor) (durable.Page[durable.DocumentRecord], error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.assertUsable(); err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	return s.memory.ScanDocuments(ctx, query, limit, cursor)
}

// Close implements durable.Storage. It is idempotent and releases the memory
// working set; every later operation rejects. A nil receiver is tolerated so a
// failed Open that returns a typed nil pointer still supports a defensive
// Close.
func (s *Storage) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.memory.Close(ctx)
}

func (s *Storage) resolveFile(file string, ctx context.Context) (string, error) {
	path, err := s.fs.JoinPath(ctx, s.directory, file)
	if err != nil {
		return "", errorFromFile("path join", err)
	}
	return path, nil
}

func (s *Storage) assertUsable() error {
	if s.closed {
		return &durable.ClosedError{Name: "JsonlStorage"}
	}
	if s.poisonErr != nil {
		return s.poisonErr
	}
	return nil
}

func (s *Storage) poison(cause error) *PoisonedError {
	if s.poisonErr == nil {
		s.poisonErr = &PoisonedError{Message: "JSONL storage is poisoned and must be reopened", Cause: cause}
	}
	return s.poisonErr
}

// planReclamations decides which sidecars a commit may replace or remove after
// its marker is published. Reclamation is best-effort maintenance: the marker
// already published the state, so a failed replacement is retried on reopen.
func (s *Storage) planReclamations(writes []durable.StorageWrite, encoded encodedCommit) []replacement {
	createdCurrentOnly := map[durable.DocumentID]struct{}{}
	retired := map[durable.DocumentID]struct{}{}
	baseDocs := map[durable.DocumentID]struct{}{}
	finalTasks := map[durable.TaskID]*durable.TaskRecord{}

	var retiredOrder []durable.DocumentID
	var baseOrder []durable.DocumentID
	var taskOrder []durable.TaskID

	for _, write := range writes {
		switch write.Type {
		case durable.WriteDocumentCreate:
			if write.Record != nil && isCurrentOnlyCreate(*write.Record) {
				if _, ok := createdCurrentOnly[write.Record.ID]; !ok {
					createdCurrentOnly[write.Record.ID] = struct{}{}
				}
			}
		case durable.WriteDocumentChange:
			if write.Content != nil && write.Content.Kind == durable.ContentBase {
				if _, ok := baseDocs[write.ID]; !ok {
					baseDocs[write.ID] = struct{}{}
					baseOrder = append(baseOrder, write.ID)
				}
			}
		case durable.WriteDocumentRetire:
			if _, ok := retired[write.ID]; !ok {
				retired[write.ID] = struct{}{}
				retiredOrder = append(retiredOrder, write.ID)
			}
		case durable.WriteTask:
			if write.Task == nil {
				continue
			}
			if _, ok := finalTasks[write.Task.ID]; !ok {
				taskOrder = append(taskOrder, write.Task.ID)
			}
			finalTasks[write.Task.ID] = write.Task
		}
	}

	isCurrentOnlyDocument := func(id durable.DocumentID) bool {
		if _, ok := s.currentOnlyDocuments[id]; ok {
			return true
		}
		_, ok := createdCurrentOnly[id]
		return ok
	}

	var replacements []replacement
	seen := map[string]int{}
	add := func(file, content string) {
		if index, ok := seen[file]; ok {
			replacements[index].content = content
			return
		}
		seen[file] = len(replacements)
		replacements = append(replacements, replacement{file: file, content: content})
	}

	for _, id := range retiredOrder {
		if isCurrentOnlyDocument(id) {
			add(sidecarFileName("doc", id), "")
		}
	}
	for _, id := range baseOrder {
		if !isCurrentOnlyDocument(id) {
			continue
		}
		if _, isRetired := retired[id]; isRetired {
			continue
		}
		if content, ok := encoded.byName[sidecarFileName("doc", id)]; ok {
			add(sidecarFileName("doc", id), string(content))
		}
	}
	for _, id := range taskOrder {
		task := finalTasks[id]
		if task.State.Status != durable.TaskStatusTerminal {
			continue
		}
		file := sidecarFileName("task", id)
		if _, live := s.liveTaskSidecars[id]; live {
			add(file, "")
			continue
		}
		if _, ok := encoded.byName[file]; ok {
			add(file, "")
		}
	}
	return replacements
}

// adoptSidecarState records which documents are current-only and which tasks
// have live sidecars after a successful commit.
func (s *Storage) adoptSidecarState(writes []durable.StorageWrite) {
	for _, write := range writes {
		switch write.Type {
		case durable.WriteDocumentCreate:
			if write.Record != nil && isCurrentOnlyCreate(*write.Record) {
				s.currentOnlyDocuments[write.Record.ID] = struct{}{}
			}
		case durable.WriteTask:
			if write.Task == nil {
				continue
			}
			if write.Task.State.Status == durable.TaskStatusTerminal {
				delete(s.liveTaskSidecars, write.Task.ID)
			} else {
				s.liveTaskSidecars[write.Task.ID] = struct{}{}
			}
		}
	}
}

var _ durable.Storage = (*Storage)(nil)
