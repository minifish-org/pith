// This file carries the run startup and checkpoint of
// packages/agent/src/harness/runtime/drive/checkpoint.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// startRun consumes the admitted prompt and commits the initial checkpoint.
// runCheckpoint advances at most one durable boundary per pass.
package agentruntime

import (
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// StartRun consumes before_run and commits the initial checkpoint.
func StartRun(lane *Lane, drive *harnesstypes.Drive, run *harnesstypes.StartingOperation) (harnesstypes.ProcedureResult, error) {
	value, err := lane.ContinueOperation(run, func(
		state *harnesstypes.RuntimeLaneState,
		current harnesstypes.OperationState,
		meta harnesstypes.OperationMeta,
		reader harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		if meta.Intent.Kind != "run" {
			return harnesstypes.OperationCommand[any]{}, &laneInvariantError{message: "Run operation has non-run intent"}
		}
		promptEntries, err := reader.GetEntries(meta.Intent.PromptEntryIDs, drive.Context)
		if err != nil {
			return harnesstypes.OperationCommand[any]{}, err
		}
		reserved := []harnesstypes.MessageEntry{}
		for _, id := range meta.Intent.PromptEntryIDs {
			entry, ok := promptEntries[id]
			if !ok {
				return harnesstypes.OperationCommand[any]{}, &laneInvariantError{message: "Run prompt entry " + id + " is missing its message"}
			}
			messageEntry, ok := entryMessage(entry)
			if !ok {
				return harnesstypes.OperationCommand[any]{}, &laneInvariantError{message: "Run prompt entry " + id + " is missing its message"}
			}
			reserved = append(reserved, harnesstypes.MessageEntry{
				EntryBase: harnesstypes.EntryBase{ID: id, Type: harnesstypes.EntryTypeMessage},
				Message:   messageEntry,
			})
		}
		if lane.Hooks != nil {
			if _, err := lane.Hooks.RunWithGate("before_run", drive.OperationID, drive.Gate, drive.Context); err != nil {
				return harnesstypes.OperationCommand[any]{}, err
			}
		}
		chained := ChainEntries(state.TipID, reserved)
		writes := []harnesstypes.Write{}
		currentTip := state.TipID
		for _, entry := range chained {
			writes = append(writes, harnesssession.InsertEntry(entry))
			id := entry.ID
			currentTip = &id
		}
		trigger := state.TipID
		if currentTip != nil {
			trigger = currentTip
			writes = append(writes, harnesssession.SetValue(harnesssession.BranchTip(lane.Name), *currentTip))
		}
		if trigger == nil {
			return harnesstypes.OperationCommand[any]{}, &laneInvariantError{message: "Run start has no trigger entry"}
		}
		scope := harnesstypes.OperationScopeOf(current)
		next := &harnesstypes.CheckpointOperation{
			OperationScope: scope,
			CheckpointData: harnesstypes.CheckpointData{
				Continuation:   harnesstypes.Continuation{Kind: "need_assistant"},
				TriggerEntryID: *trigger,
			},
			At: harnesstypes.OperationAtCheckpoint,
		}
		result := procedureContinue()
		return harnesstypes.OperationCommand[any]{
			Kind:           harnesstypes.OperationCommandCommit,
			Writes:         writes,
			OperationState: next,
			Lane:           lanePatch(currentTip, state.Inbox, state.Configuration),
			Result:         boxProcedure(result),
		}, nil
	}, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if value.Kind == harnesstypes.ContinueOperationCancelRequested {
		return procedureContinue(), nil
	}
	if result, ok := (*value.Value).(harnesstypes.ProcedureResult); ok {
		return result, nil
	}
	return procedureContinue(), nil
}

// RunCheckpoint advances one durable run boundary with at most one commit.
func RunCheckpoint(lane *Lane, drive *harnesstypes.Drive, run *harnesstypes.CheckpointOperation) (harnesstypes.ProcedureResult, error) {
	value, err := lane.ContinueOperation(run, func(
		state *harnesstypes.RuntimeLaneState,
		current harnesstypes.OperationState,
		_ harnesstypes.OperationMeta,
		reader harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		scope := harnesstypes.OperationScopeOf(current)
		placement, err := PlanBoundaryInbox(lane, drive, state, scope, reader, state.TipID, run.Continuation.Kind == "may_finish")
		if err != nil {
			return harnesstypes.OperationCommand[any]{}, err
		}
		if placement.TriggerEntryID != nil {
			next := AssistantReadyAtBoundary(lane, state, scope, *placement.TriggerEntryID, false)
			result := procedureContinue()
			return harnesstypes.OperationCommand[any]{
				Kind:           harnesstypes.OperationCommandCommit,
				Writes:         placement.Writes,
				OperationState: next,
				Lane:           lanePatch(placement.TipID, placement.Inbox, state.Configuration),
				Result:         boxProcedure(result),
			}, nil
		}
		if run.Continuation.Kind == "may_finish" {
			// Signal FinishRunBoundary indirectly by returning a sentinel result.
			pending := BoundaryFinishPending{Kind: "finish_pending", EntryIDs: entryIDs(placement.Entries)}
			boxed := any(pending)
			return harnesstypes.OperationCommand[any]{
				Kind:   harnesstypes.OperationCommandReturn,
				Result: &boxed,
			}, nil
		}
		overflowUsed := run.Continuation.OverflowRecoveryUsed != nil && *run.Continuation.OverflowRecoveryUsed
		next := AssistantReadyAtBoundary(lane, state, scope, run.TriggerEntryID, overflowUsed)
		result := procedureContinue()
		return harnesstypes.OperationCommand[any]{
			Kind:           harnesstypes.OperationCommandCommit,
			Writes:         placement.Writes,
			OperationState: next,
			Lane:           lanePatch(placement.TipID, placement.Inbox, state.Configuration),
			Result:         boxProcedure(result),
		}, nil
	}, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if value.Kind == harnesstypes.ContinueOperationCancelRequested {
		return procedureContinue(), nil
	}
	if pending, ok := (*value.Value).(BoundaryFinishPending); ok {
		return FinishRunBoundary(lane, drive, run, run.Continuation, pending.EntryIDs, nil)
	}
	if result, ok := (*value.Value).(harnesstypes.ProcedureResult); ok {
		return result, nil
	}
	return procedureContinue(), nil
}

func entryIDs(entries []harnesstypes.NewEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entryIDOf(entry))
	}
	return ids
}
