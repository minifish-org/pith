// This file carries context.ts: projecting a path of session entries onto model
// context messages.
package session

import (
	"encoding/json"

	harnessmessages "github.com/minifish-org/pith/packages/agent/harness/messages"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// SessionContextBuildOptions configure context projection.
type SessionContextBuildOptions struct {
	EntryProjectors map[string]harnesstypes.EntryProjector
}

// BuildContextEntries trims a path to the last compaction and everything after
// it.
func BuildContextEntries(pathEntries []harnesstypes.Entry) []harnesstypes.Entry {
	compactionIndex := -1
	var compaction harnesstypes.Entry
	for index := len(pathEntries) - 1; index >= 0; index-- {
		if pathEntries[index].EntryKind() == harnesstypes.EntryTypeCompaction {
			compaction = pathEntries[index]
			compactionIndex = index
			break
		}
	}
	if compaction == nil {
		out := make([]harnesstypes.Entry, len(pathEntries))
		copy(out, pathEntries)
		return out
	}
	out := []harnesstypes.Entry{compaction}
	out = append(out, pathEntries[compactionIndex+1:]...)
	return out
}

func isContextMessage(message agenttypes.AgentMessage) bool {
	if message.Message != nil && message.Message.Assistant != nil {
		switch message.Message.Assistant.StopReason {
		case aitypes.StopReasonError, aitypes.StopReasonAborted, aitypes.StopReasonDeferred:
			return false
		default:
			return true
		}
	}
	return true
}

// SessionEntryToContextMessages projects one entry onto context messages.
func SessionEntryToContextMessages(entry harnesstypes.Entry) []agenttypes.AgentMessage {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		if isContextMessage(typed.Message) {
			return []agenttypes.AgentMessage{typed.Message}
		}
		return []agenttypes.AgentMessage{}
	case *harnesstypes.MessageEntry:
		if isContextMessage(typed.Message) {
			return []agenttypes.AgentMessage{typed.Message}
		}
		return []agenttypes.AgentMessage{}
	case harnesstypes.CompactionEntry:
		return compactionMessages(typed)
	case *harnesstypes.CompactionEntry:
		return compactionMessages(*typed)
	case harnesstypes.BranchSummaryEntry:
		return branchSummaryMessages(typed)
	case *harnesstypes.BranchSummaryEntry:
		return branchSummaryMessages(*typed)
	default:
		return []agenttypes.AgentMessage{}
	}
}

func compactionMessages(entry harnesstypes.CompactionEntry) []agenttypes.AgentMessage {
	out := []agenttypes.AgentMessage{compactionSummaryMessage(entry.Summary, entry.TokensBefore, entry.Timestamp)}
	for _, message := range entry.RetainedTail {
		if isContextMessage(message) {
			out = append(out, message)
		}
	}
	return out
}

func branchSummaryMessages(entry harnesstypes.BranchSummaryEntry) []agenttypes.AgentMessage {
	if entry.Summary == "" {
		return []agenttypes.AgentMessage{}
	}
	return []agenttypes.AgentMessage{branchSummaryMessage(entry.Summary, entry.FromID, entry.Timestamp)}
}

func compactionSummaryMessage(summary string, tokensBefore float64, timestamp float64) agenttypes.AgentMessage {
	message := harnessmessages.CreateCompactionSummaryMessage(summary, tokensBefore, harnessmessages.TimestampFromNumber(timestamp))
	raw, err := json.Marshal(message)
	if err != nil {
		return agenttypes.AgentMessage{}
	}
	return agenttypes.NewCustomMessage(harnessmessages.CompactionSummaryRole, raw)
}

func branchSummaryMessage(summary string, fromID *string, timestamp float64) agenttypes.AgentMessage {
	message := harnessmessages.CreateBranchSummaryMessage(summary, fromID, harnessmessages.TimestampFromNumber(timestamp))
	raw, err := json.Marshal(message)
	if err != nil {
		return agenttypes.AgentMessage{}
	}
	return agenttypes.NewCustomMessage(harnessmessages.BranchSummaryRole, raw)
}

// BuildSessionContext projects a path of entries, applying custom entry
// projectors.
func BuildSessionContext(
	pathEntries []harnesstypes.Entry,
	options *SessionContextBuildOptions,
	ctx harnesstypes.Context,
) ([]agenttypes.AgentMessage, error) {
	entries := BuildContextEntries(pathEntries)
	messages := []agenttypes.AgentMessage{}
	for _, entry := range entries {
		if entry.EntryKind() != harnesstypes.EntryTypeCustom {
			messages = append(messages, SessionEntryToContextMessages(entry)...)
			continue
		}
		if options == nil || options.EntryProjectors == nil {
			continue
		}
		customEntry, ok := customEntryValue(entry)
		if !ok {
			continue
		}
		projector, ok := options.EntryProjectors[customEntry.CustomType]
		if !ok || projector == nil {
			continue
		}
		projected, err := projector(customEntry, ctx)
		if err != nil {
			return nil, err
		}
		messages = append(messages, projected...)
	}
	return messages, nil
}

func customEntryValue(entry harnesstypes.Entry) (harnesstypes.CustomEntry, bool) {
	switch typed := entry.(type) {
	case harnesstypes.CustomEntry:
		return typed, true
	case *harnesstypes.CustomEntry:
		return *typed, true
	default:
		return harnesstypes.CustomEntry{}, false
	}
}
