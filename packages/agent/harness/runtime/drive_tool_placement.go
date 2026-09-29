// This file carries tool result placement of
// packages/agent/src/harness/runtime/drive/tool-placement.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A tool batch never interleaves: only the contiguous run of outcome-ready
// calls at the front of the batch is materialized, and the batch block is
// completed only when every call settles.
package agentruntime

import (
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// ToolBatchSource is the resolved assistant message and its indexed calls.
type ToolBatchSource struct {
	Assistant aitypes.AssistantMessage
	Calls     map[int]aitypes.ToolCall
}

// ReadToolBatchSource resolves the assistant content blocks referenced by a
// tool batch.
func ReadToolBatchSource(lane *Lane, drive *harnesstypes.Drive, batch harnesstypes.ToolBatch) (ToolBatchSource, error) {
	entries, err := lane.Session.GetEntries([]string{batch.AssistantEntryID}, drive.Context)
	if err != nil {
		return ToolBatchSource{}, err
	}
	entry, ok := entries[batch.AssistantEntryID]
	if !ok {
		return ToolBatchSource{}, &laneInvariantError{message: "Tool batch assistant entry is invalid"}
	}
	message, ok := entryMessage(entry)
	if !ok {
		return ToolBatchSource{}, &laneInvariantError{message: "Tool batch assistant entry is invalid"}
	}
	assistant, ok := agentMessageAssistant(message)
	if !ok {
		return ToolBatchSource{}, &laneInvariantError{message: "Tool batch assistant entry is invalid"}
	}
	calls := map[int]aitypes.ToolCall{}
	for _, call := range batch.Calls {
		if call.SourceIndex < 0 || call.SourceIndex >= len(assistant.Content) {
			return ToolBatchSource{}, &laneInvariantError{message: "Tool call source index is invalid"}
		}
		block := assistant.Content[call.SourceIndex]
		if !block.IsToolCall() || block.ToolCall == nil {
			return ToolBatchSource{}, &laneInvariantError{message: "Tool call source index does not name a tool-call block"}
		}
		calls[call.SourceIndex] = *block.ToolCall
	}
	return ToolBatchSource{Assistant: *assistant, Calls: calls}, nil
}

// ToolCallFor resolves one planned call against the batch source.
func ToolCallFor(sources ToolBatchSource, call harnesstypes.ToolCall) (aitypes.ToolCall, error) {
	source, ok := sources.Calls[call.SourceIndex]
	if !ok {
		return aitypes.ToolCall{}, &laneInvariantError{message: "Tool call source index is invalid"}
	}
	return source, nil
}

// WithToolBatch returns a tools leaf with a replaced batch.
func WithToolBatch(run *harnesstypes.ToolsOperation, batch harnesstypes.ToolBatch) *harnesstypes.ToolsOperation {
	return &harnesstypes.ToolsOperation{
		OperationScope: run.OperationScope,
		At:             harnesstypes.OperationAtTools,
		Batch:          batch,
	}
}

type placementItem struct {
	call    harnesstypes.ToolCall
	message agenttypes.AgentMessage
}

// MaterializeReady commits the contiguous outcome-ready prefix of a tool
// batch.
func MaterializeReady(
	lane *Lane,
	drive *harnesstypes.Drive,
	capability *harnesstypes.ToolsOperation,
	sources ToolBatchSource,
	recovery bool,
) error {
	items, err := lane.readPlacement(drive, capability, sources)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	events := []Event{}
	for _, item := range items {
		events = append(events,
			Event{Type: "message_start", RunID: &drive.OperationID, Message: item.message},
			Event{Type: "message_end", RunID: &drive.OperationID, Message: item.message, EntryID: &item.call.ResultEntryID},
		)
	}
	if len(events) > 0 {
		if err := lane.EmitBatch(events, drive.Context); err != nil {
			return err
		}
	}
	_, err = lane.SettleOperation(capability, func(
		state *harnesstypes.RuntimeLaneState,
		current harnesstypes.OperationState,
		_ harnesstypes.OperationMeta,
		reader harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		run, ok := current.(*harnesstypes.ToolsOperation)
		if !ok {
			return harnesstypes.OperationCommand[any]{}, &laneInvariantError{message: "Tool placement requires a tools leaf"}
		}
		parentID := state.TipID
		writes := []harnesstypes.Write{}
		completed := append([]harnesstypes.ToolCall{}, run.Batch.Calls...)
		for _, item := range items {
			entry := harnesstypes.MessageEntry{
				EntryBase: harnesstypes.EntryBase{ID: item.call.ResultEntryID, ParentID: parentID, Type: harnesstypes.EntryTypeMessage},
				Message:   item.message,
			}
			writes = append(writes, harnesssession.InsertEntry(entry), harnesssession.DeleteValue(harnesssession.PendingEntry(item.call.ResultEntryID)))
			id := item.call.ResultEntryID
			parentID = &id
			for index := range completed {
				if completed[index].SourceIndex == item.call.SourceIndex && completed[index].ResultEntryID == item.call.ResultEntryID {
					completed[index].Status = "completed"
				}
			}
		}
		if parentID != nil {
			writes = append(writes, harnesssession.SetValue(harnesssession.BranchTip(lane.Name), *parentID))
		}
		complete := true
		for _, call := range completed {
			if call.Status != "completed" {
				complete = false
				break
			}
		}
		var next harnesstypes.OperationState
		if complete {
			if parentID == nil {
				return harnesstypes.OperationCommand[any]{}, errRunOperationHasNoTip
			}
			next = &harnesstypes.CheckpointOperation{
				OperationScope: run.OperationScope,
				CheckpointData: harnesstypes.CheckpointData{
					Continuation:   harnesstypes.Continuation{Kind: "need_assistant"},
					TriggerEntryID: *parentID,
				},
				At: harnesstypes.OperationAtCheckpoint,
			}
		} else {
			next = WithToolBatch(run, harnesstypes.ToolBatch{
				AssistantEntryID: run.Batch.AssistantEntryID,
				Configuration:    run.Batch.Configuration,
				TurnID:           run.Batch.TurnID,
				Calls:            completed,
			})
		}
		result := procedureContinue()
		return harnesstypes.OperationCommand[any]{
			Kind:           harnesstypes.OperationCommandCommit,
			Writes:         writes,
			OperationState: next,
			Lane:           lanePatch(parentID, state.Inbox, state.Configuration),
			Result:         boxProcedure(result),
		}, nil
	}, drive.Context)
	return err
}

func (l *Lane) readPlacement(
	drive *harnesstypes.Drive,
	capability *harnesstypes.ToolsOperation,
	sources ToolBatchSource,
) ([]placementItem, error) {
	items := []placementItem{}
	for _, call := range capability.Batch.Calls {
		if call.Status != "outcome_ready" {
			break
		}
		stored, ok, err := l.Session.GetValue(harnesssession.PendingEntry(call.ResultEntryID), drive.Context)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, &laneInvariantError{message: "Tool call is missing its staged result"}
		}
		pending, ok := stored.Value.(harnesstypes.PendingEntry)
		if !ok || pending.Type != "message" || pending.Payload == nil {
			return nil, &laneInvariantError{message: "Tool call is missing its staged result"}
		}
		message := *pending.Payload
		assistant, err := ToolCallFor(sources, call)
		if err != nil {
			return nil, err
		}
		toolResult, ok := agentMessageToolResult(message)
		if !ok || toolResult.ToolCallId != assistant.Id || toolResult.ToolName != assistant.Name {
			return nil, &laneInvariantError{message: "Tool call has a mismatched staged result"}
		}
		items = append(items, placementItem{call: call, message: message})
	}
	return items, nil
}

func agentMessageToolResult(message agenttypes.AgentMessage) (*aitypes.ToolResultMessage, bool) {
	if message.Message == nil || message.Message.ToolResult == nil {
		return nil, false
	}
	return message.Message.ToolResult, true
}
