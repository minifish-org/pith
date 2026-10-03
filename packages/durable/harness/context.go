package harness

import (
	"context"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/durable"
)

const scanPageSize = 256

var excludedStopReasons = map[types.StopReason]bool{
	types.StopReasonAborted:  true,
	types.StopReasonError:    true,
	types.StopReasonDeferred: true,
}

const missingResultText = "Tool result unavailable: history ends before this call completed."

// contextBounds captures the head marker and newest visible entry that fix one
// committed context range.
type contextBounds struct {
	head *durable.EntryRecord
	tail durable.EntryID
}

// captureContextBounds captures the bounds of the current context, or of the
// context cut at the visible entry at, with two reads.
func captureContextBounds(ctx context.Context, storage durable.Storage, conversationID durable.ConversationID, at *durable.EntryID) (*contextBounds, error) {
	var tail durable.EntryID
	if at == nil {
		page, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: conversationID}, 1, nil)
		if err != nil {
			return nil, err
		}
		if len(page.Items) == 0 {
			return nil, nil
		}
		tail = page.Items[0].ID
	} else {
		stored, err := storage.VisibleEntry(ctx, conversationID, *at)
		if err != nil {
			return nil, err
		}
		if stored == nil {
			return nil, fmt.Errorf("Entry %d is not visible from conversation %d", *at, conversationID)
		}
		tail = *at
	}
	head, err := storage.FindLatestHeadMarker(ctx, conversationID, &tail)
	if err != nil {
		return nil, err
	}
	return &contextBounds{head: head, tail: tail}, nil
}

// readContext reads the committed context of one conversation.
func readContext(ctx context.Context, session *durable.Session, storage durable.Storage, conversationID durable.ConversationID, at *durable.EntryID) (ContextView, error) {
	var bounds *contextBounds
	err := session.ReadOnLine(ctx, func(tx *durable.Transaction) error {
		var inner error
		bounds, inner = captureContextBounds(ctx, storage, conversationID, at)
		return inner
	})
	if err != nil {
		return ContextView{}, err
	}
	return deriveContext(ctx, storage, conversationID, bounds)
}

// activeEntries returns the raw active entries within captured bounds.
func activeEntries(ctx context.Context, storage durable.Storage, conversationID durable.ConversationID, bounds *contextBounds) ([]durable.EntryRecord, error) {
	if bounds == nil {
		return nil, nil
	}
	rangeEntries, err := scanRange(ctx, storage, conversationID, bounds)
	if err != nil {
		return nil, err
	}
	return selectActive(bounds.head, rangeEntries), nil
}

// deriveContext derives the active transcript and model context within bounds.
func deriveContext(ctx context.Context, storage durable.Storage, conversationID durable.ConversationID, bounds *contextBounds) (ContextView, error) {
	if bounds == nil {
		return ContextView{Entries: []durable.EntryRecord{}, Contributions: [][]types.Message{}, Messages: []types.Message{}}, nil
	}
	rangeEntries, err := scanRange(ctx, storage, conversationID, bounds)
	if err != nil {
		return ContextView{}, err
	}
	edits := map[durable.EntryID]durable.ContextEdit{}
	for _, entry := range rangeEntries {
		for _, edit := range entry.Edits {
			edits[edit.Target] = edit
		}
	}
	entries := selectActive(bounds.head, rangeEntries)
	contributions := make([][]types.Message, 0, len(entries))
	for _, entry := range entries {
		edit, hasEdit := edits[entry.ID]
		var contributed []types.Message
		switch {
		case hasEdit && edit.Action == "omit":
			contributed = nil
		case hasEdit && edit.Action == "replace":
			contributed = edit.Messages
		default:
			contributed = entry.Model
		}
		filtered := make([]types.Message, 0, len(contributed))
		for _, message := range contributed {
			if message.Assistant != nil && excludedStopReasons[message.Assistant.StopReason] {
				continue
			}
			filtered = append(filtered, message)
		}
		contributions = append(contributions, filtered)
	}
	flat := make([]types.Message, 0)
	for _, list := range contributions {
		flat = append(flat, list...)
	}
	return ContextView{
		Head:          bounds.head,
		Entries:       entries,
		Contributions: contributions,
		Messages:      orderToolResults(flat),
	}, nil
}

func scanRange(ctx context.Context, storage durable.Storage, conversationID durable.ConversationID, bounds *contextBounds) ([]durable.EntryRecord, error) {
	query := durable.EntryQuery{ConversationID: conversationID, MaxEntryID: &bounds.tail}
	if bounds.head != nil && bounds.head.Head != nil {
		query.MinEntryID = bounds.head.Head
	}
	var all []durable.EntryRecord
	var cursor durable.Cursor
	for {
		page, err := storage.ScanEntries(ctx, query, scanPageSize, cursor)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Items...)
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	return all, nil
}

func selectActive(head *durable.EntryRecord, rangeEntries []durable.EntryRecord) []durable.EntryRecord {
	if head == nil {
		return rangeEntries
	}
	active := make([]durable.EntryRecord, 0, len(rangeEntries)+1)
	active = append(active, *head)
	for _, entry := range rangeEntries {
		if entry.Head == nil {
			active = append(active, entry)
		}
	}
	return active
}

// orderToolResults places each assistant's tool results directly after it in
// call order.
func orderToolResults(messages []types.Message) []types.Message {
	ordered := make([]types.Message, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		if message.ToolResult != nil {
			continue
		}
		ordered = append(ordered, message)
		if message.Assistant == nil {
			continue
		}
		var calls []types.ToolCall
		for _, block := range message.Assistant.Content {
			if block.ToolCall != nil {
				calls = append(calls, *block.ToolCall)
			}
		}
		if len(calls) == 0 {
			continue
		}
		results := map[string]int{}
		for next := index + 1; next < len(messages) && messages[next].Assistant == nil; next++ {
			candidate := messages[next]
			if candidate.ToolResult != nil {
				if _, ok := results[candidate.ToolResult.ToolCallId]; !ok {
					results[candidate.ToolResult.ToolCallId] = next
				}
			}
		}
		for _, call := range calls {
			if resultIndex, ok := results[call.Id]; ok {
				ordered = append(ordered, messages[resultIndex])
			} else {
				ordered = append(ordered, missingResult(call, message.Assistant.Timestamp))
			}
		}
	}
	return ordered
}

func missingResult(call types.ToolCall, timestamp float64) types.Message {
	message := types.NewToolResultMessage(call.Id, call.Name, []types.ContentBlock{types.TextBlock(missingResultText)}, true, timestamp)
	message.Details = mustJSON(map[string]any{"reason": "missing_result"})
	return types.Message{Role: types.ToolResultMessageRole, ToolResult: &message}
}

var _ = errors.New
