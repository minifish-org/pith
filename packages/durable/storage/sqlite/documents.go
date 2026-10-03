package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/chord/delta"
	"github.com/minifish-org/pith/packages/durable"
)

// addressFields are the indexed, encoded columns that identify one logical
// document address. kind and keyValue are JSON-encoded strings so indexed
// identities are lossless for any UTF-16 code unit, including values SQLite
// bindings might otherwise replace.
type addressFields struct {
	kind      string
	scopeKind string
	ownerID   int64
	family    int64
	keyValue  string
}

func indexedString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func scopeColumns(scope durable.DocumentScope) (string, int64) {
	switch scope.Kind {
	case durable.ScopeConversation:
		return durable.ScopeConversation, int64(scope.ConversationID)
	case durable.ScopeTask:
		return durable.ScopeTask, int64(scope.TaskID)
	default:
		return durable.ScopeSession, 0
	}
}

func addressParts(kind string, key *string, scope durable.DocumentScope) addressFields {
	scopeKind, ownerID := scopeColumns(scope)
	family := int64(0)
	keyValue := indexedString("")
	if key != nil {
		family = 1
		keyValue = indexedString(*key)
	}
	return addressFields{
		kind:      indexedString(kind),
		scopeKind: scopeKind,
		ownerID:   ownerID,
		family:    family,
		keyValue:  keyValue,
	}
}

func addressPartsOfAddress(address durable.DocumentAddress) addressFields {
	return addressParts(address.Kind, address.Key, address.Scope)
}

func addressPartsOfCreate(create durable.DocumentCreate) addressFields {
	return addressParts(create.Kind, create.Key, create.Scope)
}

func addressPartsOfRecord(record durable.DocumentRecord) addressFields {
	return addressParts(record.Kind, record.Key, record.Scope)
}

func (p addressFields) key() string {
	return p.kind + "\x00" + p.scopeKind + "\x00" + fmt.Sprint(p.ownerID) + "\x00" + fmt.Sprint(p.family) + "\x00" + p.keyValue
}

func isAliveAt(record durable.DocumentRecord, at durable.DocumentPoint) bool {
	if at.Current {
		return record.RetiredAt == nil
	}
	return record.CreatedAt <= at.Seq && (record.RetiredAt == nil || at.Seq < *record.RetiredAt)
}

func isCurrentOnly(record durable.DocumentRecord) bool {
	return record.Scope.Kind != durable.ScopeConversation || record.History == durable.HistoryLatest
}

// documentAction is the collapsed effect of every document write for one ID in
// one batch.
type documentAction struct {
	create  *durable.DocumentCreate
	copy    *durable.DocumentCopySource
	content *durable.DocumentContent
	retire  bool
}

// prepareDocumentActions collapses every document write in one batch into one
// action per document ID. It performs no I/O.
func prepareDocumentActions(writes []durable.StorageWrite) (map[durable.DocumentID]*documentAction, error) {
	actions := map[durable.DocumentID]*documentAction{}
	for _, write := range writes {
		switch write.Type {
		case durable.WriteDocumentCreate:
			if write.Record == nil {
				return nil, fmt.Errorf("storage write %q is missing a record", write.Type)
			}
			id := write.Record.ID
			action := actions[id]
			if action == nil {
				action = &documentAction{}
				actions[id] = action
			}
			if action.create != nil || action.content != nil || action.copy != nil {
				return nil, fmt.Errorf("Document %d has more than one content command", id)
			}
			action.create = write.Record
			action.content = write.Content
		case durable.WriteDocumentCopy:
			if write.Record == nil {
				return nil, fmt.Errorf("storage write %q is missing a record", write.Type)
			}
			id := write.Record.ID
			action := actions[id]
			if action == nil {
				action = &documentAction{}
				actions[id] = action
			}
			if action.create != nil || action.content != nil || action.copy != nil {
				return nil, fmt.Errorf("Document %d has more than one content command", id)
			}
			action.create = write.Record
			action.copy = write.Source
		case durable.WriteDocumentChange:
			id := write.ID
			action := actions[id]
			if action == nil {
				action = &documentAction{}
				actions[id] = action
			}
			if action.content != nil || action.copy != nil {
				return nil, fmt.Errorf("Document %d has more than one content command", id)
			}
			action.content = write.Content
		case durable.WriteDocumentRetire:
			id := write.ID
			action := actions[id]
			if action == nil {
				action = &documentAction{}
				actions[id] = action
			}
			if action.retire {
				return nil, fmt.Errorf("Document %d is retired more than once", id)
			}
			action.retire = true
		}
	}
	return actions, nil
}

// checkDocumentActions validates every collapsed document action against the
// committed state and against the other actions in the batch. It runs inside
// the commit transaction before any table write.
func checkDocumentActions(ctx context.Context, exec Executor, actions map[durable.DocumentID]*documentAction) error {
	liveCounts := map[string]int64{}
	for id, action := range actions {
		if action.copy != nil {
			if _, changed := actions[action.copy.ID]; changed {
				return &durable.StorageRejected{Message: fmt.Sprintf("Document copy %d source is changed in the copy batch", id)}
			}
		}
		existing, err := readDocumentRecord(ctx, exec, id)
		if err != nil {
			return err
		}
		if action.create == nil && existing == nil {
			return fmt.Errorf("Unknown document: %d", id)
		}
		if action.create != nil && existing != nil {
			return fmt.Errorf("Document %d already exists", id)
		}
		if existing != nil && existing.RetiredAt != nil {
			return fmt.Errorf("Document %d is retired", id)
		}
		if action.content != nil && action.content.Kind == durable.ContentDelta {
			var version int64
			err := exec.QueryRowContext(ctx,
				"SELECT version FROM document_revisions WHERE document_id = ? ORDER BY seq DESC LIMIT 1",
				int64(id)).Scan(&version)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("Document %d delta has no base", id)
			}
			if err != nil {
				return err
			}
			if version != int64(action.content.Version) {
				return fmt.Errorf("Document %d version transition requires a base", id)
			}
		}
		var fields addressFields
		if action.create != nil {
			fields = addressPartsOfCreate(*action.create)
		} else {
			fields = addressPartsOfRecord(*existing)
		}
		key := fields.key()
		live, present := liveCounts[key]
		if !present {
			current, err := currentDocumentID(ctx, exec, fields)
			if err != nil {
				return err
			}
			if current == nil {
				live = 0
			} else {
				live = 1
			}
		}
		if action.retire && existing != nil {
			live--
		}
		if action.create != nil && !action.retire {
			live++
		}
		liveCounts[key] = live
	}
	for _, live := range liveCounts {
		if live > 1 {
			return fmt.Errorf("Document address already has a current incarnation")
		}
	}
	return nil
}

// applyDocumentActions applies every collapsed document action inside the
// commit transaction. Copy sources are materialized here so a definition-free
// copy stores the exact selected base.
func applyDocumentActions(ctx context.Context, exec Executor, actions map[durable.DocumentID]*documentAction, seq durable.Seq) error {
	for id, action := range actions {
		var content *durable.DocumentContent
		if action.content != nil {
			content = action.content
		}
		if action.copy != nil {
			stored, err := materializeDocument(ctx, exec, action.copy.ID, action.copy.At)
			if err != nil {
				return &durable.StorageRejected{Message: fmt.Sprintf("Document copy %d was rejected", id), Cause: err}
			}
			if stored == nil {
				return &durable.StorageRejected{Message: fmt.Sprintf("Document copy %d was rejected", id), Cause: fmt.Errorf("Fork source document %d cannot be read", action.copy.ID)}
			}
			create := action.create
			if create == nil {
				return &durable.StorageRejected{Message: fmt.Sprintf("Document copy %d was rejected", id), Cause: fmt.Errorf("Document copy %d has no record", id)}
			}
			content = &durable.DocumentContent{Kind: durable.ContentBase, Version: stored.Version, Value: stored.Value}
		}

		var record durable.DocumentRecord
		created := false
		if action.create != nil {
			created = true
			record = durable.DocumentRecord{
				ID:        action.create.ID,
				Kind:      action.create.Kind,
				Key:       action.create.Key,
				Scope:     action.create.Scope,
				History:   action.create.History,
				Fork:      action.create.Fork,
				CreatedAt: seq,
			}
			if action.retire {
				retired := seq
				record.RetiredAt = &retired
			}
			fields := addressPartsOfRecord(record)
			if err := claimID(ctx, exec, int64(id), "document"); err != nil {
				return err
			}
			encoded, err := encodeJSON(record)
			if err != nil {
				return err
			}
			var retired any
			if record.RetiredAt != nil {
				retired = int64(*record.RetiredAt)
			}
			if _, err := exec.ExecContext(ctx,
				`INSERT INTO documents
					(id, kind, family, key_value, scope_kind, owner_id, created_at, retired_at, record)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				int64(id), fields.kind, fields.family, fields.keyValue, fields.scopeKind, fields.ownerID,
				int64(seq), retired, encoded); err != nil {
				return err
			}
		} else {
			existing, err := readDocumentRecord(ctx, exec, id)
			if err != nil {
				return err
			}
			if existing == nil {
				return fmt.Errorf("Unknown document: %d", id)
			}
			record = *existing
		}

		if content != nil {
			if content.Kind == durable.ContentBase && isCurrentOnly(record) {
				if _, err := exec.ExecContext(ctx, "DELETE FROM document_revisions WHERE document_id = ?", int64(id)); err != nil {
					return err
				}
			}
			var encoded string
			var encErr error
			if content.Kind == durable.ContentBase {
				encoded, encErr = encodeJSON(content.Value)
			} else {
				encoded, encErr = encodeJSON(content.Ops)
			}
			if encErr != nil {
				return encErr
			}
			if _, err := exec.ExecContext(ctx,
				"INSERT INTO document_revisions (document_id, seq, kind, version, content) VALUES (?, ?, ?, ?, ?)",
				int64(id), int64(seq), content.Kind, content.Version, encoded); err != nil {
				return err
			}
		}

		if action.retire {
			if !created {
				retired := seq
				record.RetiredAt = &retired
				encoded, err := encodeJSON(record)
				if err != nil {
					return err
				}
				if _, err := exec.ExecContext(ctx,
					"UPDATE documents SET retired_at = ?, record = ? WHERE id = ?",
					int64(seq), encoded, int64(id)); err != nil {
					return err
				}
			}
			if isCurrentOnly(record) {
				if _, err := exec.ExecContext(ctx, "DELETE FROM document_revisions WHERE document_id = ?", int64(id)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// materializeDocument reconstructs one specific incarnation by ID at the
// selected point without following a replacement at its address.
func materializeDocument(ctx context.Context, exec Executor, id durable.DocumentID, at durable.DocumentPoint) (*durable.StoredDocument, error) {
	record, err := readDocumentRecord(ctx, exec, id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	if !at.Current && isCurrentOnly(*record) {
		return nil, fmt.Errorf("Document %d does not retain historical content", id)
	}
	if !isAliveAt(*record, at) {
		return nil, nil
	}
	upper := int64(durable.MaxSafeInteger)
	if !at.Current {
		upper = int64(at.Seq)
	}
	var baseSeq int64
	var baseKind string
	var baseVersion int64
	var baseContent string
	err = exec.QueryRowContext(ctx,
		`SELECT seq, kind, version, content FROM document_revisions
			WHERE document_id = ? AND kind = 'base' AND seq <= ? ORDER BY seq DESC LIMIT 1`,
		int64(id), upper).Scan(&baseSeq, &baseKind, &baseVersion, &baseContent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("Document %d is missing a required base", id)
	}
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal([]byte(baseContent), &value); err != nil {
		return nil, err
	}
	rows, err := exec.QueryContext(ctx,
		`SELECT kind, version, content FROM document_revisions
			WHERE document_id = ? AND seq > ? AND seq <= ? ORDER BY seq`,
		int64(id), baseSeq, upper)
	if err != nil {
		return nil, err
	}
	batches := [][]delta.Op{}
	for rows.Next() {
		var kind string
		var version int64
		var raw string
		if err := rows.Scan(&kind, &version, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if kind != durable.ContentDelta || version != baseVersion {
			rows.Close()
			return nil, fmt.Errorf("Document %d crosses a stored version boundary without a base", id)
		}
		var ops []delta.Op
		if err := json.Unmarshal([]byte(raw), &ops); err != nil {
			rows.Close()
			return nil, err
		}
		batches = append(batches, ops)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	replayed, err := delta.ApplyImmutableBatches(value, batches)
	if err != nil {
		return nil, err
	}
	object, ok := replayed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Document %d materialized value is not a JSON object", id)
	}
	return &durable.StoredDocument{
		Record:          *record,
		Version:         int(baseVersion),
		Value:           durable.JsonObject(object),
		DeltasSinceBase: len(batches),
	}, nil
}

// readDocumentRecord reads and decodes one document record, or nil when absent.
func readDocumentRecord(ctx context.Context, exec Executor, id durable.DocumentID) (*durable.DocumentRecord, error) {
	var raw string
	err := exec.QueryRowContext(ctx, "SELECT record FROM documents WHERE id = ?", int64(id)).Scan(&raw)
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

// currentDocumentID resolves the current incarnation of one exact logical
// address, or nil when the address has no live incarnation.
func currentDocumentID(ctx context.Context, exec Executor, fields addressFields) (*durable.DocumentID, error) {
	var id int64
	err := exec.QueryRowContext(ctx,
		`SELECT id FROM documents
			WHERE kind = ? AND scope_kind = ? AND owner_id = ? AND family = ? AND key_value = ? AND retired_at IS NULL
			LIMIT 1`,
		fields.kind, fields.scopeKind, fields.ownerID, fields.family, fields.keyValue).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value := durable.DocumentID(id)
	return &value, nil
}

// encodeJSON marshals one trusted durable value into the stored TEXT column.
func encodeJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
