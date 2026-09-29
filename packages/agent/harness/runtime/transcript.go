// This file carries the transcript projection of
// packages/agent/src/harness/runtime/transcript.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Entry lifecycle events stay ordered: message_start, message_end and
// entry_added for a message, and entry_added alone for every other entry. The
// bounded readers never materialize more than the current compaction window.
package agentruntime

import (
	"encoding/json"

	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

// Event is one observable harness lifecycle event emitted by the runtime. The
// harness assembly (a later slice) forwards these into the event bus; the
// runtime owns only construction.
type Event struct {
	Type      string          `json:"type"`
	Lane      *string         `json:"lane,omitempty"`
	RunID     *string         `json:"runId,omitempty"`
	Message   any             `json:"message,omitempty"`
	EntryID   *string         `json:"entryId,omitempty"`
	Entry     any             `json:"entry,omitempty"`
	Queues    any             `json:"queues,omitempty"`
	StartedAt *float64        `json:"startedAt,omitempty"`
	EndedAt   *float64        `json:"endedAt,omitempty"`
	Status    string          `json:"status,omitempty"`
	FromTipID *string         `json:"fromTipId,omitempty"`
	TipID     *string         `json:"tipId,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Error     any             `json:"error,omitempty"`
	Recovery  *bool           `json:"recovery,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

// LaneQueuedItem is one queued inbox projection item.
type LaneQueuedItem struct {
	EntryID    string `json:"entryId"`
	Kind       string `json:"kind"`
	Type       string `json:"type"`
	Message    any    `json:"message,omitempty"`
	CustomType string `json:"customType,omitempty"`
	Data       any    `json:"data,omitempty"`
}

// PendingMessage is one loaded pending inbox message.
type PendingMessage struct {
	EntryID string
	Message agenttypes.AgentMessage
}

// ChainEntries assigns each item the previous item's id as parent, in order.
func ChainEntries(parentID *string, items []harnesstypes.MessageEntry) []harnesstypes.MessageEntry {
	chained := make([]harnesstypes.MessageEntry, 0, len(items))
	current := parentID
	for _, item := range items {
		entry := item
		entry.ParentID = current
		chained = append(chained, entry)
		id := item.ID
		current = &id
	}
	return chained
}

// entryIDOf returns the id of any concrete entry.
func entryIDOf(entry harnesstypes.Entry) string {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		return typed.ID
	case *harnesstypes.MessageEntry:
		return typed.ID
	case harnesstypes.CompactionEntry:
		return typed.ID
	case *harnesstypes.CompactionEntry:
		return typed.ID
	case harnesstypes.BranchSummaryEntry:
		return typed.ID
	case *harnesstypes.BranchSummaryEntry:
		return typed.ID
	case harnesstypes.CustomEntry:
		return typed.ID
	case *harnesstypes.CustomEntry:
		return typed.ID
	default:
		return ""
	}
}

// entryMessage returns the message of a message entry and whether it is one.
func entryMessage(entry harnesstypes.Entry) (agenttypes.AgentMessage, bool) {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		return typed.Message, true
	case *harnesstypes.MessageEntry:
		return typed.Message, true
	default:
		return agenttypes.AgentMessage{}, false
	}
}

// EntryLifecycleEvents builds the ordered lifecycle events for one committed
// entry.
func EntryLifecycleEvents(entry harnesstypes.Entry, lane string, runID *string) []Event {
	laneValue := lane
	if message, ok := entryMessage(entry); ok {
		id := entryIDOf(entry)
		idValue := id
		return []Event{
			{Type: "message_start", Lane: &laneValue, RunID: runID, Message: message},
			{Type: "message_end", Lane: &laneValue, RunID: runID, Message: message, EntryID: &idValue},
			{Type: "entry_added", Lane: &laneValue, Entry: entry},
		}
	}
	return []Event{{Type: "entry_added", Lane: &laneValue, Entry: entry}}
}

// CommittedEntryEvents materializes committed entries and builds their ordered
// lifecycle events.
func CommittedEntryEvents(
	entries []harnesstypes.NewEntry,
	commit harnesstypes.CommitResult,
	lane string,
	runID *string,
	firstWriteIndex int,
) []Event {
	events := []Event{}
	for index, entry := range entries {
		seq := 0
		if firstWriteIndex+index < len(commit.Seqs) {
			seq = commit.Seqs[firstWriteIndex+index]
		}
		materialized := harnesssession.MaterializeCommittedEntry(entry, seq, commit.Timestamp)
		events = append(events, EntryLifecycleEvents(materialized, lane, runID)...)
	}
	return events
}

// ReadBoundedEntries reads the current branch tail up to the newest compaction.
func ReadBoundedEntries(
	lane *Lane,
	drive *harnesstypes.Drive,
	capability harnesstypes.OperationState,
) (harnesstypes.ContinueOperationResult[[]harnesstypes.Entry], error) {
	scope := harnesstypes.OperationScopeOf(capability)
	if scope.Control.Status == "cancel_requested" {
		return harnesstypes.ContinueOperationResult[[]harnesstypes.Entry]{Kind: harnesstypes.ContinueOperationCancelRequested}, nil
	}
	lane.mu.Lock()
	tipID := lane.state.TipID
	lane.mu.Unlock()
	if tipID == nil {
		return harnesstypes.ContinueOperationResult[[]harnesstypes.Entry]{}, errRunOperationHasNoTip
	}
	order := "newestFirst"
	stopAt := harnesstypes.EntryTypeCompaction
	entries, err := lane.Session.ScanBranch(harnesstypes.StorageBranchScan{
		Start: *tipID,
		BranchScan: harnesstypes.BranchScan{
			StopAtType: &stopAt,
			Order:      &order,
		},
	}, drive.Context)
	if err != nil {
		return harnesstypes.ContinueOperationResult[[]harnesstypes.Entry]{}, err
	}
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	value := entries
	return harnesstypes.ContinueOperationResult[[]harnesstypes.Entry]{
		Kind:  harnesstypes.ContinueOperationResultKind,
		Value: &value,
	}, nil
}

// ReadBoundedContext reads the current bounded transcript and projects it to
// model context.
func ReadBoundedContext(
	lane *Lane,
	drive *harnesstypes.Drive,
	capability harnesstypes.OperationState,
) (harnesstypes.ContinueOperationResult[[]agenttypes.AgentMessage], error) {
	entries, err := ReadBoundedEntries(lane, drive, capability)
	if err != nil {
		return harnesstypes.ContinueOperationResult[[]agenttypes.AgentMessage]{}, err
	}
	if entries.Kind == harnesstypes.ContinueOperationCancelRequested {
		return harnesstypes.ContinueOperationResult[[]agenttypes.AgentMessage]{Kind: entries.Kind}, nil
	}
	config := lane.ReadConfig()
	messages, err := harnesssession.BuildSessionContext(
		*entries.Value,
		&harnesssession.SessionContextBuildOptions{EntryProjectors: config.EntryProjectors},
		drive.Context,
	)
	if err != nil {
		return harnesstypes.ContinueOperationResult[[]agenttypes.AgentMessage]{}, err
	}
	return harnesstypes.ContinueOperationResult[[]agenttypes.AgentMessage]{
		Kind:  harnesstypes.ContinueOperationResultKind,
		Value: &messages,
	}, nil
}

// ReadLaneQueues loads the pending payloads behind the remaining inbox so the
// snapshot can show the queue without leaking storage addresses.
func ReadLaneQueues(
	reader harnesstypes.SessionReader,
	inbox []harnesstypes.InboxItem,
	ctx harnesstypes.Context,
) ([]LaneQueuedItem, error) {
	items := make([]LaneQueuedItem, 0, len(inbox))
	for _, item := range inbox {
		stored, ok, err := reader.GetValue(harnesssession.PendingEntry(item.EntryID), ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errPendingEntryMissingPayload
		}
		pending, ok := stored.Value.(harnesstypes.PendingEntry)
		if !ok {
			return nil, errPendingEntryMissingPayload
		}
		if pending.Type == "message" {
			var message any
			if pending.Payload != nil {
				message = *pending.Payload
			}
			items = append(items, LaneQueuedItem{
				EntryID: item.EntryID,
				Kind:    string(item.Kind),
				Type:    "message",
				Message: message,
			})
			continue
		}
		if item.Kind != harnesstypes.InboxItemWrite {
			return nil, errPendingEntryNotMessage
		}
		queued := LaneQueuedItem{
			EntryID:    item.EntryID,
			Kind:       "write",
			Type:       "custom",
			CustomType: pending.CustomType,
		}
		if pending.Data != nil {
			queued.Data = pending.Data
		}
		items = append(items, queued)
	}
	return items, nil
}

// ReadPendingMessages loads ordered pending message payloads.
func ReadPendingMessages(
	reader harnesstypes.SessionReader,
	ids []string,
	description string,
	ctx harnesstypes.Context,
) ([]PendingMessage, error) {
	messages := make([]PendingMessage, 0, len(ids))
	for _, entryID := range ids {
		stored, ok, err := reader.GetValue(harnesssession.PendingEntry(entryID), ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, errQueuedMessageMissingPayload
		}
		pending, ok := stored.Value.(harnesstypes.PendingEntry)
		if !ok || pending.Type != "message" || pending.Payload == nil {
			return nil, errQueuedMessageMissingPayload
		}
		messages = append(messages, PendingMessage{EntryID: entryID, Message: *pending.Payload})
	}
	return messages, nil
}
