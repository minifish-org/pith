package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/minifish-org/pith/packages/durable"
)

// checkContext reports a cancelled invocation before admission.
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// readConversation reads and decodes one conversation record, or nil when
// absent.
func readConversation(ctx context.Context, exec Executor, id durable.ConversationID) (*durable.ConversationRecord, error) {
	var raw string
	err := exec.QueryRowContext(ctx, "SELECT record FROM conversations WHERE id = ?", int64(id)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record durable.ConversationRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func readEntry(ctx context.Context, exec Executor, id durable.EntryID) (*durable.EntryRecord, durable.Seq, error) {
	var raw string
	var commitSeq int64
	err := exec.QueryRowContext(ctx, "SELECT record, commit_seq FROM entries WHERE id = ?", int64(id)).Scan(&raw, &commitSeq)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	var record durable.EntryRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, 0, err
	}
	return &record, durable.Seq(commitSeq), nil
}

// Conversation implements durable.Storage.
func (s *Storage) Conversation(ctx context.Context, id durable.ConversationID) (*durable.ConversationRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	return readConversation(ctx, s.db, id)
}

// ScanConversations implements durable.Storage.
func (s *Storage) ScanConversations(ctx context.Context, query durable.ConversationQuery, limit int, cursor durable.Cursor) (durable.Page[durable.ConversationRecord], error) {
	if err := checkContext(ctx); err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	clauses := []string{"id > ?"}
	args := []any{int64(-1)}
	if after != nil {
		args[0] = int64(*after)
	}
	if query.OwnerConversationID != nil {
		clauses = append(clauses, "owner_conversation_id = ?")
		args = append(args, int64(*query.OwnerConversationID))
	}
	if query.OwnerTaskID != nil {
		clauses = append(clauses, "owner_task_id = ?")
		args = append(args, int64(*query.OwnerTaskID))
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT record FROM conversations WHERE "+strings.Join(clauses, " AND ")+" ORDER BY id LIMIT ?", args...)
	if err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	values := []durable.ConversationRecord{}
	if err := scanRecords(rows, &values); err != nil {
		return durable.Page[durable.ConversationRecord]{}, err
	}
	return pageRecords(values, limit, func(value durable.ConversationRecord) durable.ID { return value.ID }), nil
}

// Entry implements durable.Storage. It is the global lookup form of the
// upstream overload.
func (s *Storage) Entry(ctx context.Context, id durable.EntryID) (*durable.StoredEntry, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	entry, seq, err := readEntry(ctx, s.db, id)
	if err != nil || entry == nil {
		return nil, err
	}
	return &durable.StoredEntry{Entry: *entry, CommitSeq: seq}, nil
}

// VisibleEntry implements durable.Storage.
func (s *Storage) VisibleEntry(ctx context.Context, conversationID durable.ConversationID, id durable.EntryID) (*durable.StoredEntry, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	conversation, err := readConversation(ctx, s.db, conversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		return nil, fmt.Errorf("Unknown conversation: %d", conversationID)
	}
	entry, seq, err := readEntry(ctx, s.db, id)
	if err != nil || entry == nil {
		return nil, err
	}
	upper := int64(durable.MaxSafeInteger)
	current := conversation
	for current.ID != entry.ConversationID {
		if current.Parent == nil {
			return nil, nil
		}
		if int64(current.Parent.At) < upper {
			upper = int64(current.Parent.At)
		}
		current, err = readConversation(ctx, s.db, current.Parent.ConversationID)
		if err != nil {
			return nil, err
		}
		if current == nil {
			return nil, nil
		}
	}
	if int64(entry.ID) > upper {
		return nil, nil
	}
	return &durable.StoredEntry{Entry: *entry, CommitSeq: seq}, nil
}

// FindLatestHeadMarker implements durable.Storage. The returned entry is the
// marker; its Head is the range's actual lower bound.
func (s *Storage) FindLatestHeadMarker(ctx context.Context, conversationID durable.ConversationID, atOrBefore *durable.EntryID) (*durable.EntryRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	conversation, err := readConversation(ctx, s.db, conversationID)
	if err != nil {
		return nil, err
	}
	if conversation == nil {
		return nil, fmt.Errorf("Unknown conversation: %d", conversationID)
	}
	var upper *int64
	if atOrBefore != nil {
		value := int64(*atOrBefore)
		upper = &value
	}
	for {
		var raw string
		var scanErr error
		if upper == nil {
			scanErr = s.db.QueryRowContext(ctx,
				"SELECT record FROM entries WHERE conversation_id = ? AND head IS NOT NULL ORDER BY id DESC LIMIT 1",
				int64(conversation.ID)).Scan(&raw)
		} else {
			scanErr = s.db.QueryRowContext(ctx,
				"SELECT record FROM entries WHERE conversation_id = ? AND head IS NOT NULL AND id <= ? ORDER BY id DESC LIMIT 1",
				int64(conversation.ID), *upper).Scan(&raw)
		}
		if scanErr == nil {
			var record durable.EntryRecord
			if err := json.Unmarshal([]byte(raw), &record); err != nil {
				return nil, err
			}
			return &record, nil
		}
		if !errors.Is(scanErr, sql.ErrNoRows) {
			return nil, scanErr
		}
		if conversation.Parent == nil {
			return nil, nil
		}
		if upper == nil || int64(conversation.Parent.At) < *upper {
			value := int64(conversation.Parent.At)
			upper = &value
		}
		conversation, err = readConversation(ctx, s.db, conversation.Parent.ConversationID)
		if err != nil {
			return nil, err
		}
		if conversation == nil {
			return nil, nil
		}
	}
}

// ScanEntries implements durable.Storage. It walks fork-aware ancestry
// newest-first and applies every ancestor cap and inclusive ID bound.
func (s *Storage) ScanEntries(ctx context.Context, query durable.EntryQuery, limit int, cursor durable.Cursor) (durable.Page[durable.EntryRecord], error) {
	if err := checkContext(ctx); err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	conversation, err := readConversation(ctx, s.db, query.ConversationID)
	if err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	if conversation == nil {
		return durable.Page[durable.EntryRecord]{}, fmt.Errorf("Unknown conversation: %d", query.ConversationID)
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.EntryRecord]{}, err
	}
	var upper *int64
	if query.MaxEntryID != nil {
		value := int64(*query.MaxEntryID)
		upper = &value
	}
	if after != nil {
		value := int64(*after) - 1
		if upper == nil || value < *upper {
			upper = &value
		}
	}
	values := []durable.EntryRecord{}
	for {
		clauses := []string{"conversation_id = ?"}
		args := []any{int64(conversation.ID)}
		if query.MinEntryID != nil {
			clauses = append(clauses, "id >= ?")
			args = append(args, int64(*query.MinEntryID))
		}
		if upper != nil {
			clauses = append(clauses, "id <= ?")
			args = append(args, *upper)
		}
		remaining := limit + 1 - len(values)
		if remaining < 1 {
			remaining = 1
		}
		args = append(args, remaining)
		rows, err := s.db.QueryContext(ctx, "SELECT record FROM entries WHERE "+strings.Join(clauses, " AND ")+" ORDER BY id DESC LIMIT ?", args...)
		if err != nil {
			return durable.Page[durable.EntryRecord]{}, err
		}
		if err := scanRecords(rows, &values); err != nil {
			return durable.Page[durable.EntryRecord]{}, err
		}
		if len(values) > limit || conversation.Parent == nil {
			break
		}
		if upper == nil || int64(conversation.Parent.At) < *upper {
			value := int64(conversation.Parent.At)
			upper = &value
		}
		if query.MinEntryID != nil && upper != nil && *upper < int64(*query.MinEntryID) {
			break
		}
		conversation, err = readConversation(ctx, s.db, conversation.Parent.ConversationID)
		if err != nil {
			return durable.Page[durable.EntryRecord]{}, err
		}
		if conversation == nil {
			break
		}
	}
	return pageRecords(values, limit, func(value durable.EntryRecord) durable.ID { return value.ID }), nil
}

// Task implements durable.Storage.
func (s *Storage) Task(ctx context.Context, id durable.TaskID) (*durable.TaskRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT record FROM tasks WHERE id = ?", int64(id)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record durable.TaskRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// ScanTasks implements durable.Storage.
func (s *Storage) ScanTasks(ctx context.Context, query durable.TaskQuery, limit int, cursor durable.Cursor) (durable.Page[durable.TaskRecord], error) {
	if err := checkContext(ctx); err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	clauses := []string{"id > ?"}
	args := []any{int64(-1)}
	if after != nil {
		args[0] = int64(*after)
	}
	if query.ConversationID != nil {
		clauses = append(clauses, "conversation_id = ?")
		args = append(args, int64(*query.ConversationID))
	}
	if query.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, indexedString(query.Kind))
	}
	if query.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, query.Status)
	}
	if query.AbortRequested != nil {
		clauses = append(clauses, "abort_requested = ?")
		args = append(args, boolInt(*query.AbortRequested))
	}
	if query.Background != nil {
		clauses = append(clauses, "background = ?")
		args = append(args, boolInt(*query.Background))
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT record FROM tasks WHERE "+strings.Join(clauses, " AND ")+" ORDER BY id LIMIT ?", args...)
	if err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	values := []durable.TaskRecord{}
	if err := scanRecords(rows, &values); err != nil {
		return durable.Page[durable.TaskRecord]{}, err
	}
	return pageRecords(values, limit, func(value durable.TaskRecord) durable.ID { return value.ID }), nil
}

// Submission implements durable.Storage.
func (s *Storage) Submission(ctx context.Context, id durable.SubmissionID) (*durable.SubmissionRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx, "SELECT record FROM submissions WHERE id = ?", int64(id)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record durable.SubmissionRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// ScanSubmissions implements durable.Storage.
func (s *Storage) ScanSubmissions(ctx context.Context, query durable.SubmissionQuery, limit int, cursor durable.Cursor) (durable.Page[durable.SubmissionRecord], error) {
	if err := checkContext(ctx); err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	clauses := []string{"id > ?"}
	args := []any{int64(-1)}
	if after != nil {
		args[0] = int64(*after)
	}
	if query.ConversationID != nil {
		clauses = append(clauses, "conversation_id = ?")
		args = append(args, int64(*query.ConversationID))
	}
	if query.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, query.Status)
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT record FROM submissions WHERE "+strings.Join(clauses, " AND ")+" ORDER BY id LIMIT ?", args...)
	if err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	values := []durable.SubmissionRecord{}
	if err := scanRecords(rows, &values); err != nil {
		return durable.Page[durable.SubmissionRecord]{}, err
	}
	return pageRecords(values, limit, func(value durable.SubmissionRecord) durable.ID { return value.ID }), nil
}

// SubmissionByRequest implements durable.Storage.
func (s *Storage) SubmissionByRequest(ctx context.Context, conversationID durable.ConversationID, requestID string) (*durable.SubmissionRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	var raw string
	err := s.db.QueryRowContext(ctx,
		"SELECT record FROM submissions WHERE conversation_id = ? AND request_id = ?",
		int64(conversationID), indexedString(requestID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record durable.SubmissionRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// FindDocument implements durable.Storage.
func (s *Storage) FindDocument(ctx context.Context, address durable.DocumentAddress, at durable.DocumentPoint) (*durable.DocumentRecord, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	fields := addressPartsOfAddress(address)
	var raw string
	var err error
	if at.Current {
		err = s.db.QueryRowContext(ctx,
			`SELECT record FROM documents
				WHERE kind = ? AND scope_kind = ? AND owner_id = ? AND family = ? AND key_value = ?
				AND retired_at IS NULL ORDER BY created_at DESC LIMIT 1`,
			fields.kind, fields.scopeKind, fields.ownerID, fields.family, fields.keyValue).Scan(&raw)
	} else {
		err = s.db.QueryRowContext(ctx,
			`SELECT record FROM documents
				WHERE kind = ? AND scope_kind = ? AND owner_id = ? AND family = ? AND key_value = ?
				AND created_at <= ? AND (retired_at IS NULL OR retired_at > ?)
				ORDER BY created_at DESC LIMIT 1`,
			fields.kind, fields.scopeKind, fields.ownerID, fields.family, fields.keyValue, int64(at.Seq), int64(at.Seq)).Scan(&raw)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record durable.DocumentRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, err
	}
	return &record, nil
}

// Document implements durable.Storage. The record and revision reads run in one
// transaction so a concurrent commit that replaces the base cannot split them.
func (s *Storage) Document(ctx context.Context, id durable.DocumentID, at durable.DocumentPoint) (*durable.StoredDocument, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return nil, err
	}
	var stored *durable.StoredDocument
	err := s.db.Transaction(ctx, func(tx Executor) error {
		result, err := materializeDocument(ctx, tx, id, at)
		if err != nil {
			return err
		}
		stored = result
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stored, nil
}

// ScanDocuments implements durable.Storage.
func (s *Storage) ScanDocuments(ctx context.Context, query durable.DocumentQuery, limit int, cursor durable.Cursor) (durable.Page[durable.DocumentRecord], error) {
	if err := checkContext(ctx); err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	s.reads.RLock()
	defer s.reads.RUnlock()
	if err := s.assertOpen(); err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	after, err := cursorID(cursor)
	if err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	scopeKind, ownerID := scopeColumns(query.Scope)
	clauses := []string{"scope_kind = ?", "owner_id = ?", "id > ?"}
	args := []any{scopeKind, ownerID, int64(-1)}
	if after != nil {
		args[2] = int64(*after)
	}
	if query.Kind != "" {
		clauses = append(clauses, "kind = ?")
		args = append(args, indexedString(query.Kind))
	}
	if query.At.Current {
		clauses = append(clauses, "retired_at IS NULL")
	} else {
		clauses = append(clauses, "created_at <= ?", "(retired_at IS NULL OR retired_at > ?)")
		args = append(args, int64(query.At.Seq), int64(query.At.Seq))
	}
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT record FROM documents WHERE "+strings.Join(clauses, " AND ")+" ORDER BY id LIMIT ?", args...)
	if err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	values := []durable.DocumentRecord{}
	if err := scanRecords(rows, &values); err != nil {
		return durable.Page[durable.DocumentRecord]{}, err
	}
	return pageRecords(values, limit, func(value durable.DocumentRecord) durable.ID { return value.ID }), nil
}

// scanRecords decodes every remaining JSON record row into values.
func scanRecords[T any](rows *sql.Rows, values *[]T) error {
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var value T
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return err
		}
		*values = append(*values, value)
	}
	return rows.Err()
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

// cursorID decodes backend-owned continuation state. A nil or empty cursor
// means "from the start".
func cursorID(cursor durable.Cursor) (*durable.ID, error) {
	if len(cursor) == 0 {
		return nil, nil
	}
	var decoded struct {
		After *json.Number `json:"after"`
	}
	if err := json.Unmarshal(cursor, &decoded); err != nil {
		return nil, fmt.Errorf("Invalid storage cursor: %w", err)
	}
	if decoded.After == nil {
		return nil, nil
	}
	value, err := decoded.After.Int64()
	if err != nil || value < 0 || value > durable.MaxSafeInteger {
		return nil, fmt.Errorf("Invalid storage cursor")
	}
	id := durable.ID(value)
	return &id, nil
}

func cursorAfter(id durable.ID) durable.Cursor {
	return durable.Cursor(fmt.Sprintf(`{"after":%d}`, int64(id)))
}

func pageRecords[T any](values []T, limit int, idOf func(T) durable.ID) durable.Page[T] {
	if limit < 0 {
		limit = 0
	}
	if len(values) <= limit {
		return durable.Page[T]{Items: values}
	}
	items := values[:limit]
	var next durable.Cursor
	if len(items) > 0 {
		next = cursorAfter(idOf(items[len(items)-1]))
	}
	return durable.Page[T]{Items: items, Next: next}
}
