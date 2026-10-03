// Package sqlite implements the durable.Storage contract over a real, embedded
// SQLite database. It is the native port of Pi
// packages/durable/src/storage/sqlite/*.ts at revision
// a13d35a742c6ef8462812a28fbe1d8c8b7431c32 and uses database/sql with the
// CGO-free modernc.org/sqlite driver.
//
// # Ownership and format
//
// One SQL transaction is one storage batch: every table and document write is
// applied atomically, then durable_metadata allocates the next commit sequence
// and advances the global ID namespace. Records are stored as decoded JSON in
// ordinary indexed rows, and document revisions are stored as private
// base/delta rows that are replayed with the canonical Chord operations; they
// are never applied as SQL JSON patches.
//
// # Documented host/runtime differences
//
//   - Go exposes blocking storage operations with a context.Context instead of
//     JavaScript promises. A transaction is serialized on one pooled connection,
//     so operations run in call order and readers never observe uncommitted rows.
//   - Returned records are freshly decoded from their stored JSON, so they are
//     detached; there is no language-level revocable proxy.
//   - Default mode (synchronous=NORMAL, WAL) promises ordinary process-crash
//     consistency, not power-loss or kernel-failure durability.
//   - Allocated IDs and commit sequences are bounded by MaxSafeInteger and
//     persisted in decimal.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/minifish-org/pith/packages/durable"
)

// Default connection settings matched to the upstream adapter.
const (
	defaultWALAutoCheckpointPages = 1000
	defaultBusyTimeoutMS          = 5000
)

// Options configures a file-backed SQLite storage.
type Options struct {
	// WALAutoCheckpointPages overrides SQLite's WAL auto-checkpoint threshold.
	// SQLite and this adapter default to 1,000 pages; 0 disables it. A nil value
	// selects the default.
	WALAutoCheckpointPages *int
	// BusyTimeoutMS overrides how long SQLite waits for a competing file lock.
	// SQLite defaults to 0; this adapter defaults to 5,000 ms. A nil value
	// selects the default.
	BusyTimeoutMS *int
}

// Storage is the SQLite durable.Storage implementation. It is safe for
// concurrent use; one pooled connection serializes transactions and ordinary
// operations in call order.
type Storage struct {
	db Database

	mu     sync.Mutex // guards nextID and closed
	nextID int64
	closed bool

	reads     sync.RWMutex
	closeOnce sync.Once
	closeErr  error
}

// Open opens or creates file-backed durable storage at path. The parent
// directory is created when path is an ordinary filesystem path. ":memory:"
// selects an in-memory database, which is primarily useful for tests.
func Open(ctx context.Context, path string, options Options) (*Storage, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	checkpointPages := defaultWALAutoCheckpointPages
	if options.WALAutoCheckpointPages != nil {
		checkpointPages = *options.WALAutoCheckpointPages
	}
	busyTimeoutMS := defaultBusyTimeoutMS
	if options.BusyTimeoutMS != nil {
		busyTimeoutMS = *options.BusyTimeoutMS
	}
	db, err := openDatabase(path, checkpointPages, busyTimeoutMS)
	if err != nil {
		return nil, err
	}
	if err := applySqliteMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	var nextIDText string
	var nextSeq int64
	err = db.QueryRowContext(ctx, "SELECT next_id, next_seq FROM durable_metadata WHERE singleton = 1").Scan(&nextIDText, &nextSeq)
	if errors.Is(err, sql.ErrNoRows) {
		_ = db.Close()
		return nil, errors.New("Durable SQLite metadata is missing")
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	nextID, err := strconv.ParseInt(nextIDText, 10, 64)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("Durable SQLite next_id is invalid: %w", err)
	}
	return &Storage{db: db, nextID: nextID}, nil
}

// Commit implements durable.Storage. One SQL transaction applies every table
// and document write, advances the global ID namespace, and allocates the next
// commit sequence. A failed batch leaves no earlier item visible.
func (s *Storage) Commit(ctx context.Context, writes []durable.StorageWrite) (durable.Seq, error) {
	if err := checkContext(ctx); err != nil {
		return 0, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return 0, err
	}
	actions, err := prepareDocumentActions(writes)
	if err != nil {
		return 0, err
	}
	candidate := s.candidateNextID(writes)

	var committedSeq durable.Seq
	err = s.db.Transaction(ctx, func(tx Executor) error {
		var nextIDText string
		var nextSeq int64
		if err := tx.QueryRowContext(ctx, "SELECT next_id, next_seq FROM durable_metadata WHERE singleton = 1").Scan(&nextIDText, &nextSeq); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("Durable SQLite metadata is missing")
			}
			return err
		}
		committed := durable.Seq(nextSeq)
		if err := checkGlobalIDs(ctx, tx, writes); err != nil {
			return err
		}
		if err := checkDocumentActions(ctx, tx, actions); err != nil {
			return err
		}
		for _, write := range writes {
			if err := applyTableWrite(ctx, tx, write, committed); err != nil {
				return err
			}
		}
		if err := applyDocumentActions(ctx, tx, actions, committed); err != nil {
			return err
		}
		persistedID := candidate
		if parsed, parseErr := strconv.ParseInt(nextIDText, 10, 64); parseErr == nil && parsed > persistedID {
			persistedID = parsed
		}
		if _, err := tx.ExecContext(ctx,
			"UPDATE durable_metadata SET next_id = ?, next_seq = ? WHERE singleton = 1",
			strconv.FormatInt(persistedID, 10), int64(committed)+1); err != nil {
			return err
		}
		committedSeq = committed
		return nil
	})
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	if candidate > s.nextID {
		s.nextID = candidate
	}
	s.mu.Unlock()
	return committedSeq, nil
}

// MintID implements durable.Storage. It advances the Session-global numeric ID
// namespace; the reserved root conversation ID 1 is never returned.
func (s *Storage) MintID(ctx context.Context) (durable.ID, error) {
	if err := checkContext(ctx); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, &durable.ClosedError{Name: "SqliteStorage"}
	}
	if s.nextID > durable.MaxSafeInteger {
		return 0, errors.New("ID space is exhausted")
	}
	id := durable.ID(s.nextID)
	s.nextID++
	return id, nil
}

// Close implements durable.Storage. It is idempotent: the first call checkpoints
// the WAL and closes the database after in-flight operations finish, and later
// calls return the same result. Every later operation rejects.
func (s *Storage) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.reads.Lock()
		defer s.reads.Unlock()
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

func (s *Storage) assertOpen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return &durable.ClosedError{Name: "SqliteStorage"}
	}
	return nil
}

// candidateNextID is the namespace watermark implied by the current allocator
// and every explicit write ID in the batch.
func (s *Storage) candidateNextID(writes []durable.StorageWrite) int64 {
	s.mu.Lock()
	next := s.nextID
	s.mu.Unlock()
	for _, write := range writes {
		id, ok := writeID(write)
		if !ok {
			continue
		}
		if int64(id)+1 > next {
			next = int64(id) + 1
		}
	}
	return next
}

// writeID returns the explicitly claimed ID of one write, if any.
func writeID(write durable.StorageWrite) (durable.ID, bool) {
	switch write.Type {
	case durable.WriteConversation:
		if write.Conversation != nil {
			return write.Conversation.ID, true
		}
	case durable.WriteEntry:
		if write.Entry != nil {
			return write.Entry.ID, true
		}
	case durable.WriteTask:
		if write.Task != nil {
			return write.Task.ID, true
		}
	case durable.WriteSubmission:
		if write.Submission != nil {
			return write.Submission.ID, true
		}
	case durable.WriteDocumentCreate, durable.WriteDocumentCopy:
		if write.Record != nil {
			return write.Record.ID, true
		}
	}
	return 0, false
}

// checkGlobalIDs enforces one global record-ID namespace and immutable
// conversation/entry/document creation.
func checkGlobalIDs(ctx context.Context, exec Executor, writes []durable.StorageWrite) error {
	claimed := map[int64]string{}
	for _, write := range writes {
		if write.Type == durable.WriteDocumentChange || write.Type == durable.WriteDocumentRetire {
			continue
		}
		var table string
		var id durable.ID
		switch write.Type {
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
		var existing sql.NullString
		err := exec.QueryRowContext(ctx, "SELECT record_type FROM record_ids WHERE id = ?", int64(id)).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		existingType := ""
		if existing.Valid {
			existingType = existing.String
		}
		earlier, earlierOK := claimed[int64(id)]
		immutable := table == "conversation" || table == "entry" || table == "document"
		if immutable {
			if existingType != "" {
				return fmt.Errorf("ID %d already belongs to %s", id, existingType)
			}
			if earlierOK {
				return fmt.Errorf("ID %d is written more than once", id)
			}
		} else {
			if existingType != "" && existingType != table {
				return fmt.Errorf("ID %d already belongs to %s", id, existingType)
			}
			if earlierOK && earlier != table {
				return fmt.Errorf("ID %d is written as two record types", id)
			}
		}
		claimed[int64(id)] = table
	}
	return nil
}

// claimID records one ID in the global namespace; later conflicting claims of a
// different record type are rejected by checkGlobalIDs.
func claimID(ctx context.Context, exec Executor, id int64, table string) error {
	_, err := exec.ExecContext(ctx, "INSERT OR IGNORE INTO record_ids (id, record_type) VALUES (?, ?)", id, table)
	return err
}

// applyTableWrite inserts or replaces one record table row.
func applyTableWrite(ctx context.Context, exec Executor, write durable.StorageWrite, seq durable.Seq) error {
	switch write.Type {
	case durable.WriteConversation:
		record := write.Conversation
		if err := claimID(ctx, exec, int64(record.ID), "conversation"); err != nil {
			return err
		}
		var ownerConversation any
		var ownerTask any
		if record.Owner != nil {
			ownerConversation = int64(record.Owner.ConversationID)
			ownerTask = int64(record.Owner.TaskID)
		}
		encoded, err := encodeJSON(record)
		if err != nil {
			return err
		}
		_, err = exec.ExecContext(ctx,
			"INSERT INTO conversations (id, owner_conversation_id, owner_task_id, record) VALUES (?, ?, ?, ?)",
			int64(record.ID), ownerConversation, ownerTask, encoded)
		return err
	case durable.WriteEntry:
		record := write.Entry
		if err := claimID(ctx, exec, int64(record.ID), "entry"); err != nil {
			return err
		}
		var head any
		if record.Head != nil {
			head = int64(*record.Head)
		}
		encoded, err := encodeJSON(record)
		if err != nil {
			return err
		}
		_, err = exec.ExecContext(ctx,
			"INSERT INTO entries (id, conversation_id, head, commit_seq, record) VALUES (?, ?, ?, ?, ?)",
			int64(record.ID), int64(record.ConversationID), head, int64(seq), encoded)
		return err
	case durable.WriteTask:
		record := write.Task
		if err := claimID(ctx, exec, int64(record.ID), "task"); err != nil {
			return err
		}
		encoded, err := encodeJSON(record)
		if err != nil {
			return err
		}
		_, err = exec.ExecContext(ctx,
			`INSERT INTO tasks (id, conversation_id, kind, status, abort_requested, background, record)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET conversation_id = excluded.conversation_id, kind = excluded.kind,
				status = excluded.status, abort_requested = excluded.abort_requested,
				background = excluded.background, record = excluded.record`,
			int64(record.ID), int64(record.ConversationID), indexedString(record.Kind), record.State.Status,
			boolInt(record.AbortRequested), boolInt(record.Background), encoded)
		return err
	case durable.WriteSubmission:
		record := write.Submission
		if err := claimID(ctx, exec, int64(record.ID), "submission"); err != nil {
			return err
		}
		var requestID any
		if record.RequestID != nil {
			requestID = indexedString(*record.RequestID)
		}
		encoded, err := encodeJSON(record)
		if err != nil {
			return err
		}
		_, err = exec.ExecContext(ctx,
			`INSERT INTO submissions (id, conversation_id, request_id, status, record) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(id) DO UPDATE SET conversation_id = excluded.conversation_id,
				request_id = excluded.request_id, status = excluded.status, record = excluded.record`,
			int64(record.ID), int64(record.ConversationID), requestID, record.Status, encoded)
		return err
	case durable.WriteDocumentCreate, durable.WriteDocumentCopy, durable.WriteDocumentChange, durable.WriteDocumentRetire:
		return nil
	default:
		return fmt.Errorf("unknown storage write type %q", write.Type)
	}
}

var _ durable.Storage = (*Storage)(nil)
