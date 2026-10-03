package durable

import (
	"context"
	"fmt"
)

const forkScanPageSize = 256

// ForkDocumentCopy is one definition-free document copy created with a forked
// conversation: the new create record and the exact persisted source it copies.
type ForkDocumentCopy struct {
	Record DocumentCreate
	Source DocumentCopySource
}

// prepareForkDocumentCopies selects every persisted conversation document that
// one fork copies.
//
// Documents whose fork policy is "asOf" are copied from the fork entry's
// commit; documents whose policy is "current" are copied from the parent's
// current state. "initial" documents are never copied and initialize lazily
// from the supplied definition.
func prepareForkDocumentCopies(storage Storage, parentConversationID ConversationID, at EntryID, childConversationID ConversationID, ctx context.Context) ([]ForkDocumentCopy, error) {
	entry, err := storage.VisibleEntry(ctx, parentConversationID, at)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, fmt.Errorf("Entry %d is not visible from conversation %d", at, parentConversationID)
	}
	copies := []ForkDocumentCopy{}
	copiedAddresses := map[string]struct{}{}
	if err := collectForkCopies(storage, DocumentScope{Kind: ScopeConversation, ConversationID: entry.Entry.ConversationID}, AtSeq(entry.CommitSeq), ForkAsOf, childConversationID, &copies, copiedAddresses, ctx); err != nil {
		return nil, err
	}
	if err := collectForkCopies(storage, DocumentScope{Kind: ScopeConversation, ConversationID: parentConversationID}, CurrentDocument(), ForkCurrent, childConversationID, &copies, copiedAddresses, ctx); err != nil {
		return nil, err
	}
	return copies, nil
}

func collectForkCopies(storage Storage, scope DocumentScope, at DocumentPoint, policy string, childConversationID ConversationID, copies *[]ForkDocumentCopy, copiedAddresses map[string]struct{}, ctx context.Context) error {
	var cursor Cursor
	for {
		page, err := storage.ScanDocuments(ctx, DocumentQuery{Scope: scope, At: at}, forkScanPageSize, cursor)
		if err != nil {
			return err
		}
		for index := range page.Items {
			source := page.Items[index]
			if source.Scope.Kind != ScopeConversation || source.Fork != policy {
				continue
			}
			id, err := storage.MintID(ctx)
			if err != nil {
				return err
			}
			record := DocumentCreate{
				ID:      DocumentID(id),
				Kind:    source.Kind,
				Key:     cloneString(source.Key),
				Scope:   DocumentScope{Kind: ScopeConversation, ConversationID: childConversationID},
				Fork:    source.Fork,
				History: HistoryRewindable,
			}
			if source.History == HistoryLatest {
				record.History = HistoryLatest
			}
			address := DocumentAddress{Kind: record.Kind, Scope: record.Scope, Key: record.Key}
			copyAddress := AddressID(address)
			if _, present := copiedAddresses[copyAddress]; present {
				member := record.Kind
				if record.Key != nil {
					member = record.Kind + "/" + *record.Key
				}
				return fmt.Errorf("Fork selects multiple source documents for %s", member)
			}
			copiedAddresses[copyAddress] = struct{}{}
			*copies = append(*copies, ForkDocumentCopy{Record: record, Source: DocumentCopySource{ID: source.ID, At: at}})
		}
		cursor = page.Next
		if cursor == nil {
			return nil
		}
	}
}
