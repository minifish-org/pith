package memory

import (
	"fmt"

	"github.com/minifish-org/pith/packages/chord/delta"
	"github.com/minifish-org/pith/packages/durable"
)

// documentRevision is one persisted base or delta revision of a document
// incarnation, stamped with the commit sequence that stored it.
type documentRevision struct {
	content durable.DocumentContent
	seq     durable.Seq
}

// storedDocumentState is the internal record of one document incarnation.
type storedDocumentState struct {
	record    durable.DocumentRecord
	revisions []documentRevision
}

// documentAddressIndex tracks every incarnation that ever occupied one logical
// address, in ascending ID order, plus the current incarnation.
type documentAddressIndex struct {
	ids       []durable.DocumentID
	currentID *durable.DocumentID
}

// documentAction is the collapsed effect of every document write for one ID in
// one batch.
type documentAction struct {
	create  *durable.DocumentCreate
	content *durable.DocumentContent
	retire  bool
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

func scopeKey(scope durable.DocumentScope) string {
	switch scope.Kind {
	case durable.ScopeConversation:
		return fmt.Sprintf("c:%d", int64(scope.ConversationID))
	case durable.ScopeTask:
		return fmt.Sprintf("t:%d", int64(scope.TaskID))
	default:
		return "s"
	}
}

func addressKey(address durable.DocumentAddress) string {
	key := "singleton"
	if address.Key != nil {
		key = "family:" + *address.Key
	}
	return address.Kind + "\x00" + scopeKey(address.Scope) + "\x00" + key
}

func recordAddressKey(record durable.DocumentRecord) string {
	return addressKey(durable.DocumentAddress{Kind: record.Kind, Scope: record.Scope, Key: record.Key})
}

func createAddressKey(record durable.DocumentCreate) string {
	return addressKey(durable.DocumentAddress{Kind: record.Kind, Scope: record.Scope, Key: record.Key})
}

// materializeDocument materializes one specific incarnation by ID at the
// selected point without following a replacement at its address.
func (s *Storage) materializeDocument(id durable.DocumentID, at durable.DocumentPoint) (*durable.StoredDocument, error) {
	stored := s.state.documents[id]
	if stored == nil {
		return nil, nil
	}
	if !at.Current && isCurrentOnly(stored.record) {
		return nil, fmt.Errorf("Document %d does not retain historical content", id)
	}
	if !isAliveAt(stored.record, at) {
		return nil, nil
	}
	revisions := stored.revisions
	if !at.Current {
		filtered := make([]documentRevision, 0, len(revisions))
		for _, revision := range revisions {
			if revision.seq <= at.Seq {
				filtered = append(filtered, revision)
			}
		}
		revisions = filtered
	}
	baseIndex := -1
	for index := len(revisions) - 1; index >= 0; index-- {
		if revisions[index].content.Kind == durable.ContentBase {
			baseIndex = index
			break
		}
	}
	if baseIndex < 0 {
		return nil, fmt.Errorf("Document %d is missing a required base", id)
	}
	base := revisions[baseIndex]
	batches := make([][]delta.Op, 0, len(revisions)-baseIndex-1)
	for index := baseIndex + 1; index < len(revisions); index++ {
		revision := revisions[index]
		if revision.content.Kind != durable.ContentDelta || revision.content.Version != base.content.Version {
			return nil, fmt.Errorf("Document %d crosses a stored version boundary without a base", id)
		}
		batches = append(batches, revision.content.Ops)
	}
	value, err := delta.ApplyImmutableBatches(base.content.Value, batches)
	if err != nil {
		return nil, fmt.Errorf("Document %d could not be materialized: %w", id, err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Document %d materialized value is not a JSON object", id)
	}
	return &durable.StoredDocument{
		Record:          stored.record,
		Version:         base.content.Version,
		Value:           durable.JsonObject(object),
		DeltasSinceBase: len(revisions) - baseIndex - 1,
	}, nil
}

// resolveDocumentCopies rewrites each definition-free document.copy into a
// document.create carrying the exact stored base of its selected source. The
// source must be readable at the selected point and must not change in the same
// batch; those rejections are definite and surface as StorageRejected.
func (s *Storage) resolveDocumentCopies(writes []durable.StorageWrite) ([]durable.StorageWrite, error) {
	hasCopy := false
	for _, write := range writes {
		if write.Type == durable.WriteDocumentCopy {
			hasCopy = true
			break
		}
	}
	if !hasCopy {
		return writes, nil
	}
	changed := map[durable.DocumentID]bool{}
	for _, write := range writes {
		switch write.Type {
		case durable.WriteDocumentCreate, durable.WriteDocumentCopy:
			if write.Record != nil {
				changed[write.Record.ID] = true
			}
		case durable.WriteDocumentChange, durable.WriteDocumentRetire:
			changed[write.ID] = true
		}
	}
	out := make([]durable.StorageWrite, len(writes))
	for index, write := range writes {
		if write.Type != durable.WriteDocumentCopy {
			out[index] = write
			continue
		}
		resolved, err := s.resolveDocumentCopy(write, changed)
		if err != nil {
			return nil, err
		}
		out[index] = resolved
	}
	return out, nil
}

func (s *Storage) resolveDocumentCopy(write durable.StorageWrite, changed map[durable.DocumentID]bool) (durable.StorageWrite, error) {
	if write.Record == nil {
		return durable.StorageWrite{}, rejected("Document copy %d has no record", write.Record.ID)
	}
	if write.Source == nil {
		return durable.StorageWrite{}, rejected("Document copy %d has no source", write.Record.ID)
	}
	if changed[write.Source.ID] {
		return durable.StorageWrite{}, rejected("Fork source document %d is changed in the copy batch", write.Source.ID)
	}
	stored, err := s.materializeDocument(write.Source.ID, write.Source.At)
	if err != nil {
		return durable.StorageWrite{}, &durable.StorageRejected{
			Message: fmt.Sprintf("Document copy %d was rejected", write.Record.ID),
			Cause:   err,
		}
	}
	if stored == nil {
		return durable.StorageWrite{}, rejected("Fork source document %d cannot be read", write.Source.ID)
	}
	content := durable.DocumentContent{Kind: durable.ContentBase, Version: stored.Version, Value: stored.Value}
	return durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: write.Record, Content: &content}, nil
}

// prepareDocumentActions collapses every document write in one batch into one
// action per document ID.
func (s *Storage) prepareDocumentActions(writes []durable.StorageWrite) (map[durable.DocumentID]*documentAction, error) {
	actions := map[durable.DocumentID]*documentAction{}
	for _, write := range writes {
		switch write.Type {
		case durable.WriteDocumentCreate:
			id := write.Record.ID
			action := actions[id]
			if action == nil {
				action = &documentAction{}
				actions[id] = action
			}
			if action.create != nil || action.content != nil {
				return nil, fmt.Errorf("Document %d has more than one content command", id)
			}
			action.create = write.Record
			action.content = write.Content
		case durable.WriteDocumentChange:
			id := write.ID
			action := actions[id]
			if action == nil {
				action = &documentAction{}
				actions[id] = action
			}
			if action.content != nil {
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
// committed state and against the other actions in the batch.
func (s *Storage) checkDocumentActions(actions map[durable.DocumentID]*documentAction) error {
	liveCounts := map[string]int{}
	for id, action := range actions {
		existing := s.state.documents[id]
		if action.create == nil && existing == nil {
			return fmt.Errorf("Unknown document: %d", id)
		}
		if action.create != nil && existing != nil {
			return fmt.Errorf("Document %d already exists", id)
		}
		if existing != nil && existing.record.RetiredAt != nil {
			return fmt.Errorf("Document %d is retired", id)
		}
		if action.content != nil && action.content.Kind == durable.ContentDelta {
			if existing == nil || len(existing.revisions) == 0 {
				return fmt.Errorf("Document %d delta has no base", id)
			}
			previous := existing.revisions[len(existing.revisions)-1]
			if previous.content.Version != action.content.Version {
				return fmt.Errorf("Document %d version transition requires a base", id)
			}
		}
		var key string
		if action.create == nil {
			key = recordAddressKey(existing.record)
		} else {
			key = createAddressKey(*action.create)
		}
		live, present := liveCounts[key]
		if !present {
			index := s.state.documentAddresses[key]
			if index == nil || index.currentID == nil {
				live = 0
			} else {
				live = 1
			}
		}
		if action.retire {
			if index := s.state.documentAddresses[key]; index != nil && index.currentID != nil && *index.currentID == id {
				live--
			}
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

// applyDocumentActions applies every collapsed document action. It performs no
// fallible preparation, so the caller has already validated the batch.
func (s *Storage) applyDocumentActions(actions map[durable.DocumentID]*documentAction, seq durable.Seq) {
	for id, action := range actions {
		stored := s.state.documents[id]
		if action.create != nil {
			record := durable.DocumentRecord{
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
			revisions := []documentRevision{}
			if action.content != nil {
				revisions = append(revisions, documentRevision{content: *action.content, seq: seq})
			}
			stored = &storedDocumentState{record: record, revisions: revisions}
			s.state.recordTypes[id] = "document"
			s.state.documents[id] = stored

			key := recordAddressKey(record)
			index := s.state.documentAddresses[key]
			if index == nil {
				index = &documentAddressIndex{}
				s.state.documentAddresses[key] = index
			}
			index.ids = insertSorted(index.ids, id)
			scope := scopeKey(record.Scope)
			s.state.documentIDsByScope[scope] = insertSorted(s.state.documentIDsByScope[scope], id)
			if int64(id)+1 > s.nextID {
				s.nextID = int64(id) + 1
			}
		} else if action.content != nil {
			revision := documentRevision{content: *action.content, seq: seq}
			if revision.content.Kind == durable.ContentBase && isCurrentOnly(stored.record) {
				stored.revisions = []documentRevision{revision}
			} else {
				stored.revisions = append(stored.revisions, revision)
			}
		}

		if action.retire && action.create == nil {
			retired := seq
			stored.record.RetiredAt = &retired
		}
		if action.retire && isCurrentOnly(stored.record) {
			stored.revisions = []documentRevision{}
		}
		if action.create != nil || action.retire {
			index := s.state.documentAddresses[recordAddressKey(stored.record)]
			if index != nil {
				if action.retire && index.currentID != nil && *index.currentID == id {
					index.currentID = nil
				}
				if action.create != nil && !action.retire {
					current := id
					index.currentID = &current
				}
			}
		}
	}
}

// rejected builds a definite, no-effect rejection.
func rejected(format string, args ...any) *durable.StorageRejected {
	return &durable.StorageRejected{Message: fmt.Sprintf(format, args...)}
}
