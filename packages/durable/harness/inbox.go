package harness

import (
	"context"
	"encoding/json"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable"
)

// InboxDoc is the built-in queue of one conversation's pending submissions.
var InboxDoc, _ = durable.DefineDoc(durable.DocumentDefinition{
	Kind:    "pi.inbox",
	Version: 1,
	Scope:   durable.ScopeConversation,
	History: durable.HistoryLatest,
	Fork:    durable.ForkInitial,
	Initial: func(json.RawMessage) (durable.JsonObject, error) {
		return durable.JsonObject{"items": []any{}}, nil
	},
	CheckpointWhen: func(value durable.JsonObject, _ []chordOp, _ durable.CheckpointInfo) (bool, error) {
		return len(inboxItems(value)) == 0, nil
	},
})

func inboxAddress(conversationID durable.ConversationID) durable.DocumentAddress {
	return durable.ConversationAddress(InboxDoc, conversationID, nil)
}

func inboxItems(state map[string]any) []any {
	items, _ := state["items"].([]any)
	return items
}

// boundary is what a boundary reads before the commit's first table write.
type boundary struct {
	conversationID durable.ConversationID
	inbox          map[string]any
	steeringMode   string
	followUpMode   string
	head           *durable.EntryID
}

// boundaryResult is the selected user submissions and whether a reset was
// placed.
type boundaryResult struct {
	users []durable.SubmissionID
	reset bool
}

func prepareBoundary(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, steeringMode, followUpMode string) (*boundary, error) {
	marker, err := tx.LatestHeadMarker(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	var head *durable.EntryID
	if marker != nil && marker.Head != nil {
		value := *marker.Head
		head = &value
	}
	draft, err := tx.Doc(ctx, *InboxDoc, inboxAddress(conversationID), nil)
	if err != nil {
		return nil, err
	}
	inbox, err := draft.Value()
	if err != nil {
		return nil, err
	}
	return &boundary{conversationID: conversationID, inbox: inbox, steeringMode: steeringMode, followUpMode: followUpMode, head: head}, nil
}

// applyBoundary places the queued items a boundary selects. at is "postTools"
// or "final".
func applyBoundary(ctx context.Context, tx durable.Tx, b *boundary, at string, now int64) (boundaryResult, error) {
	items := inboxItems(b.inbox)
	reset := false
	for _, item := range items {
		entry, ok := itemEntry(item)
		if !ok {
			continue
		}
		if headSelf(entry) {
			reset = true
			break
		}
	}
	final := at == "final" || reset
	pick := func(mode string, queueMode string) []int {
		var indexes []int
		for index, item := range items {
			if itemMode(item) == mode {
				indexes = append(indexes, index)
			}
		}
		if queueMode == QueueAll {
			return indexes
		}
		if len(indexes) > 0 {
			return indexes[:1]
		}
		return nil
	}
	var writeIndexes []int
	for index, item := range items {
		if itemMode(item) == "write" {
			writeIndexes = append(writeIndexes, index)
		}
	}
	userIndexes := append(pick("steer", b.steeringMode), pick("followUp", func() string {
		if final {
			return b.followUpMode
		}
		return ""
	}())...)
	sortInts(userIndexes)

	removedIndexes := map[int]bool{}
	for _, index := range writeIndexes {
		record, _ := items[index].(map[string]any)
		entryRaw, _ := record["entry"].(map[string]any)
		draft := entryDraftFromMap(entryRaw)
		if isStale(b, draft) {
			id, _ := asSubmissionID(record["id"])
			if err := tx.SettleSubmission(id, durable.SubmissionSettlement{Status: "unanswered", Reason: "stale"}); err != nil {
				return boundaryResult{}, err
			}
			removedIndexes[index] = true
			continue
		}
		entry, err := tx.AppendEntry(ctx, b.conversationID, draft)
		if err != nil {
			return boundaryResult{}, err
		}
		id, _ := asSubmissionID(record["id"])
		if err := tx.PlaceSubmission(id, entry.ID); err != nil {
			return boundaryResult{}, err
		}
		if draft.Head != nil {
			if draft.HeadSelf {
				value := entry.ID
				b.head = &value
			} else {
				value := *draft.Head
				b.head = &value
			}
		}
		removedIndexes[index] = true
	}
	var placed []durable.SubmissionID
	for _, index := range userIndexes {
		record, _ := items[index].(map[string]any)
		content := record["content"]
		message := userMessageFromContent(content, now)
		entry, err := tx.AppendEntry(ctx, b.conversationID, durable.EntryDraft{Kind: entryKindUser, Model: []types.Message{message}})
		if err != nil {
			return boundaryResult{}, err
		}
		id, _ := asSubmissionID(record["id"])
		if err := tx.PlaceSubmission(id, entry.ID); err != nil {
			return boundaryResult{}, err
		}
		placed = append(placed, id)
		removedIndexes[index] = true
	}
	var kept []any
	for index, item := range items {
		if !removedIndexes[index] {
			kept = append(kept, item)
		}
	}
	if kept == nil {
		kept = []any{}
	}
	b.inbox["items"] = kept
	return boundaryResult{users: placed, reset: reset}, nil
}

func isStale(b *boundary, entry durable.EntryDraft) bool {
	if entry.Head == nil || entry.HeadSelf {
		return false
	}
	return b.head != nil && *entry.Head < *b.head
}

func removeInboxItem(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, id durable.SubmissionID) error {
	live, err := loadLiveInbox(ctx, tx, conversationID)
	if err != nil {
		return err
	}
	items := inboxItems(live)
	kept := items[:0]
	for _, item := range items {
		record, _ := item.(map[string]any)
		if existing, ok := asSubmissionID(record["id"]); ok && existing == id {
			continue
		}
		kept = append(kept, item)
	}
	if kept == nil {
		kept = []any{}
	}
	live["items"] = kept
	return nil
}

func loadLiveInbox(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID) (map[string]any, error) {
	draft, err := tx.Doc(ctx, *InboxDoc, inboxAddress(conversationID), nil)
	if err != nil {
		return nil, err
	}
	return draft.Value()
}

// withdrawQueuedInputs settles and removes every queued input.
func withdrawQueuedInputs(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID) error {
	live, err := loadLiveInbox(ctx, tx, conversationID)
	if err != nil {
		return err
	}
	items := inboxItems(live)
	var kept []any
	for _, item := range items {
		mode := itemMode(item)
		if mode == "write" {
			kept = append(kept, item)
			continue
		}
		record, _ := item.(map[string]any)
		if id, ok := asSubmissionID(record["id"]); ok {
			if err := tx.SettleSubmission(id, durable.SubmissionSettlement{Status: "unanswered", Reason: "aborted"}); err != nil {
				return err
			}
		}
	}
	if kept == nil {
		kept = []any{}
	}
	live["items"] = kept
	return nil
}

func itemMode(item any) string {
	record, ok := item.(map[string]any)
	if !ok {
		return ""
	}
	return str(record["mode"])
}

func itemEntry(item any) (map[string]any, bool) {
	record, ok := item.(map[string]any)
	if !ok {
		return nil, false
	}
	entry, ok := record["entry"].(map[string]any)
	return entry, ok
}

func headSelf(entry map[string]any) bool {
	value, ok := entry["head"]
	if !ok {
		return false
	}
	if s, ok := value.(string); ok {
		return s == "self"
	}
	return false
}

// entryToMap stores an EntryDraft as plain JSON, preserving the "self" head
// marker that the durable EntryDraft cannot round-trip through JSON.
func entryToMap(draft durable.EntryDraft) map[string]any {
	value, err := json.Marshal(draft)
	if err != nil {
		return map[string]any{}
	}
	result, err := decodeObject(value)
	if err != nil {
		return map[string]any{}
	}
	if draft.HeadSelf {
		result["head"] = "self"
	}
	return result
}

// entryDraftFromMap reconstructs an EntryDraft from its stored JSON, restoring
// the "self" head marker.
func entryDraftFromMap(value map[string]any) durable.EntryDraft {
	self := false
	if head, ok := value["head"]; ok {
		if text, ok := head.(string); ok && text == "self" {
			self = true
			value = cloneStringMap(value)
			delete(value, "head")
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return durable.EntryDraft{HeadSelf: self}
	}
	var draft durable.EntryDraft
	if err := json.Unmarshal(data, &draft); err != nil {
		return durable.EntryDraft{HeadSelf: self}
	}
	draft.HeadSelf = self
	return draft
}

func cloneStringMap(value map[string]any) map[string]any {
	clone := make(map[string]any, len(value))
	for key, child := range value {
		clone[key] = child
	}
	return clone
}

func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j-1] > values[j]; j-- {
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
}
