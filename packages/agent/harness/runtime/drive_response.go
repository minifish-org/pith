// This file carries the assistant response lifecycle of
// packages/agent/src/harness/runtime/drive/response.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// The response entry is committed exactly once, at the tip that existed when
// the request was admitted. Frames stream to a bounded pending list until the
// entry commits, then the list is deleted in the same transaction.
package agentruntime

import (
	"encoding/json"

	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// AssistantResponseLifecycle owns the pending frames and metadata of one
// assistant response.
type AssistantResponseLifecycle struct {
	Lane            *Lane
	Drive           *harnesstypes.Drive
	ResponseEntryID string
	Frames          ProgressChannel[aiutils.AssistantMessageFrame]
	encoder         *aiutils.AssistantMessageFrameEncoder
	recovery        bool
}

// OpenAssistantResponse opens the bounded frame channel for one response.
func OpenAssistantResponse(lane *Lane, drive *harnesstypes.Drive, responseEntryID string, recovery bool) *AssistantResponseLifecycle {
	return &AssistantResponseLifecycle{
		Lane:            lane,
		Drive:           drive,
		ResponseEntryID: responseEntryID,
		Frames:          OpenFrameProgress(lane, drive, responseEntryID),
		encoder:         aiutils.NewAssistantMessageFrameEncoder(),
		recovery:        recovery,
	}
}

// Close seals the frame channel.
func (l *AssistantResponseLifecycle) Close() error {
	l.Frames.Seal()
	return l.Frames.Drain()
}

// PublishOptions configures one response publication.
type PublishOptions struct {
	Recovery bool
}

// PublishConfigurationFailure publishes a synthetic model-unavailable response
// without opening a provider request.
func PublishConfigurationFailure(
	lane *Lane,
	drive *harnesstypes.Drive,
	effect harnesstypes.OperationState,
	operationError harnesstypes.OperationError,
) (harnesstypes.ProcedureResult, error) {
	message := aitypes.NewAssistantMessage("unknown", aitypes.ProviderId(operationError.Code), operationError.Message, NowMs())
	return PublishResponse(lane, drive, effect, message, PublishOptions{})
}

func responseEntryIDOf(effect harnesstypes.OperationState) string {
	switch typed := effect.(type) {
	case *harnesstypes.AssistantEffectPendingOperation:
		return typed.ResponseEntryID
	case *harnesstypes.DeferredEffectPendingOperation:
		return typed.ResponseEntryID
	default:
		return ""
	}
}

func responseConfigurationOf(effect harnesstypes.OperationState) harnesstypes.LaneConfiguration {
	switch typed := effect.(type) {
	case *harnesstypes.AssistantEffectPendingOperation:
		return typed.GenerationContext.Configuration
	case *harnesstypes.DeferredEffectPendingOperation:
		return typed.Configuration
	default:
		return harnesstypes.LaneConfiguration{}
	}
}

// PublishResponse commits the settled assistant message and advances the run.
func PublishResponse(
	lane *Lane,
	drive *harnesstypes.Drive,
	effect harnesstypes.OperationState,
	message aitypes.AssistantMessage,
	options PublishOptions,
) (harnesstypes.ProcedureResult, error) {
	responseEntryID := responseEntryIDOf(effect)
	if responseEntryID == "" {
		return harnesstypes.ProcedureResult{}, &laneInvariantError{message: "Response publication requires an effect-pending leaf"}
	}
	value, err := lane.SettleOperation(effect, func(
		state *harnesstypes.RuntimeLaneState,
		current harnesstypes.OperationState,
		_ harnesstypes.OperationMeta,
		reader harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		scope := harnesstypes.OperationScopeOf(current)
		parentID := state.TipID
		entry := harnesstypes.MessageEntry{
			EntryBase: harnesstypes.EntryBase{ID: responseEntryID, ParentID: parentID, Type: harnesstypes.EntryTypeMessage},
			Message:   agenttypes.NewAgentMessageFromMessage(aitypes.NewAssistantMessageVariant(message)),
		}
		writes := []harnesstypes.Write{
			harnesssession.InsertEntry(entry),
			harnesssession.DeleteList(harnesssession.PendingAssistantFrames(drive.OperationID, responseEntryID)),
			harnesssession.SetValue(harnesssession.BranchTip(lane.Name), responseEntryID),
		}
		usageCopy := message.Usage
		usageID := lane.Session.IDGenerator().Next(nil)
		writes = append(writes, harnesssession.InsertUsage(harnesstypes.UsageRow{
			ID:      usageID,
			Usage:   usageCopy,
			EntryID: &responseEntryID,
		}))
		nextScope := scope
		nextScope.LatestAssistantEntryID = &responseEntryID
		next := toolsOrCheckpoint(lane, drive, nextScope, state, message, responseEntryID)
		result := procedureContinue()
		return harnesstypes.OperationCommand[any]{
			Kind:           harnesstypes.OperationCommandCommit,
			Writes:         writes,
			OperationState: next,
			Lane:           lanePatch(&responseEntryID, state.Inbox, state.Configuration),
			Result:         boxProcedure(result),
		}, nil
	}, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if result, ok := value.(harnesstypes.ProcedureResult); ok {
		return result, nil
	}
	return procedureContinue(), nil
}

func toolsOrCheckpoint(
	lane *Lane,
	drive *harnesstypes.Drive,
	scope harnesstypes.OperationScope,
	state *harnesstypes.RuntimeLaneState,
	message aitypes.AssistantMessage,
	responseEntryID string,
) harnesstypes.OperationState {
	calls := []harnesstypes.ToolCall{}
	sourceIndex := 0
	for _, block := range message.Content {
		if block.IsToolCall() {
			resultEntryID := lane.Session.IDGenerator().Next(nil)
			calls = append(calls, harnesstypes.ToolCall{
				SourceIndex:   sourceIndex,
				ResultEntryID: resultEntryID,
				Status:        "planned",
			})
			sourceIndex++
		}
	}
	if len(calls) > 0 {
		turnID := lane.Session.IDGenerator().Next(nil)
		return &harnesstypes.ToolsOperation{
			OperationScope: scope,
			At:             harnesstypes.OperationAtTools,
			Batch: harnesstypes.ToolBatch{
				AssistantEntryID: responseEntryID,
				Configuration:    state.Configuration,
				TurnID:           turnID,
				Calls:            calls,
			},
		}
	}
	return &harnesstypes.CheckpointOperation{
		OperationScope: scope,
		CheckpointData: harnesstypes.CheckpointData{
			Continuation: harnesstypes.Continuation{
				Kind:                  "may_finish",
				IncludeFinalAssistant: boolPtr(true),
			},
			TriggerEntryID: responseEntryID,
		},
		At: harnesstypes.OperationAtCheckpoint,
	}
}

func boolPtr(value bool) *bool { return &value }

var _ = json.Marshal
