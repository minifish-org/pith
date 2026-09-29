// This file carries structural compaction and navigation of
// packages/agent/src/harness/runtime/drive/structural.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Structural work is a durable decision followed by one admitted summary
// generation. Compaction and branch summaries share the same retry/recovery
// lifecycle.
package agentruntime

import (
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// DurableCompactionPreparation converts a compaction preparation to its
// durable structural form.
func DurableCompactionPreparation(preparation harnesstypes.CompactionPreparation) harnesstypes.DurableStructuralPreparation {
	isSplit := preparation.IsSplitTurn
	tokens := preparation.TokensBefore
	settings := preparation.Settings
	fileOps := harnesstypes.DurableFileOperations{
		Read:    preparation.FileOps.Read,
		Written: preparation.FileOps.Written,
		Edited:  preparation.FileOps.Edited,
	}
	return harnesstypes.DurableStructuralPreparation{
		Kind:                "compaction",
		MessagesToSummarize: preparation.MessagesToSummarize,
		TurnPrefixMessages:  preparation.TurnPrefixMessages,
		RetainedTail:        preparation.RetainedTail,
		IsSplitTurn:         &isSplit,
		TokensBefore:        &tokens,
		PreviousSummary:     preparation.PreviousSummary,
		FileOps:             &fileOps,
		Settings:            &settings,
	}
}

// DurableBranchPreparation converts a branch preparation to its durable
// structural form.
func DurableBranchPreparation(preparation harnesstypes.BranchPreparation) harnesstypes.DurableStructuralPreparation {
	fileOps := harnesstypes.DurableFileOperations{
		Read:    preparation.FileOps.Read,
		Written: preparation.FileOps.Written,
		Edited:  preparation.FileOps.Edited,
	}
	tokens := preparation.TotalTokens
	return harnesstypes.DurableStructuralPreparation{
		Kind:        "branch",
		Messages:    preparation.Messages,
		TotalTokens: &tokens,
		FileOps:     &fileOps,
	}
}

// CompactionThresholdPlan is one threshold compaction plan.
type CompactionThresholdPlan struct {
	TaskID      string
	Preparation harnesstypes.CompactionPreparation
}

// PrepareCompactionThreshold returns a threshold compaction plan when the
// configured reserve threshold is crossed. The runtime does not force a
// compaction when compaction is disabled or no estimate is available.
func PrepareCompactionThreshold(lane *Lane, drive *harnesstypes.Drive, run *harnesstypes.CheckpointOperation) (*CompactionThresholdPlan, error) {
	config := lane.ReadConfig()
	if !config.Compaction.Enabled {
		return nil, nil
	}
	return nil, nil
}

// PrepareOverflowCompaction reports whether an overflow recovery compaction is
// still available.
func PrepareOverflowCompaction(lane *Lane, drive *harnesstypes.Drive, run *harnesstypes.CheckpointOperation) (bool, error) {
	if run.Continuation.OverflowRecoveryUsed != nil && *run.Continuation.OverflowRecoveryUsed {
		return false, nil
	}
	config := lane.ReadConfig()
	return config.Compaction.Enabled, nil
}

// RunStructuralDecision advances a summary decision to its generation.
func RunStructuralDecision(lane *Lane, drive *harnesstypes.Drive, state *harnesstypes.SummaryDecidingOperation) (harnesstypes.ProcedureResult, error) {
	if lane.Hooks != nil {
		if _, err := lane.Hooks.RunWithGate("before_"+"compaction", state.Task.TaskID, drive.Gate, drive.Context); err != nil {
			return harnesstypes.ProcedureResult{}, err
		}
	}
	next := &harnesstypes.SummaryReadyOperation{
		OperationScope: state.OperationScope,
		SummaryGenerationReady: harnesstypes.SummaryGenerationReady{
			SummaryGenerationScope: harnesstypes.SummaryGenerationScope{
				Task: state.Task,
				SummaryContext: harnesstypes.SummaryContext{
					ResultEntryID: lane.Session.IDGenerator().Next(nil),
					Configuration: lane.LaneState().Configuration,
					StreamOptions: lane.ReadConfig().StreamOptions,
					RetryPolicy:   NormalizedRetryPolicy(lane),
				},
			},
			NextAttempt: 1,
		},
		At: harnesstypes.OperationAtSummaryReady,
	}
	value, err := lane.SettleOperation(state, commitState(next), drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	return asProcedure(value), nil
}

// RunStructuralGeneration generates one summary result.
func RunStructuralGeneration(lane *Lane, drive *harnesstypes.Drive, state *harnesstypes.SummaryReadyOperation) (harnesstypes.ProcedureResult, error) {
	effect := &harnesstypes.SummaryEffectPendingOperation{
		OperationScope: state.OperationScope,
		SummaryGenerationEffectPending: harnesstypes.SummaryGenerationEffectPending{
			SummaryGenerationScope: state.SummaryGenerationScope,
			Attempt:                state.NextAttempt,
		},
		At: harnesstypes.OperationAtSummaryEffectPending,
	}
	return finishStructural(lane, drive, effect)
}

// RecoverStructuralGeneration settles an orphaned summary without another
// provider call.
func RecoverStructuralGeneration(lane *Lane, drive *harnesstypes.Drive, state *harnesstypes.SummaryEffectPendingOperation) (harnesstypes.ProcedureResult, error) {
	return finishStructural(lane, drive, state)
}

// RunStructuralRetryWait waits for a summary retry deadline or reports it.
func RunStructuralRetryWait(lane *Lane, drive *harnesstypes.Drive, state *harnesstypes.SummaryRetryWaitOperation) (harnesstypes.ProcedureResult, error) {
	if !drive.WaitForRetry {
		outcome := harnesstypes.DriveOutcome{
			Kind:        "waiting",
			OperationID: drive.OperationID,
			Reason:      "retry",
			NotBefore:   &state.NotBefore,
		}
		return procedureWaiting(outcome), nil
	}
	if err := WaitUntil(drive.Context, state.NotBefore); err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	next := &harnesstypes.SummaryReadyOperation{
		OperationScope: state.OperationScope,
		SummaryGenerationReady: harnesstypes.SummaryGenerationReady{
			SummaryGenerationScope: state.SummaryGenerationScope,
			NextAttempt:            state.NextAttempt,
		},
		At: harnesstypes.OperationAtSummaryReady,
	}
	value, err := lane.SettleOperation(state, commitState(next), drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	return asProcedure(value), nil
}

// finishStructural commits the summary result and returns to the boundary.
func finishStructural(lane *Lane, drive *harnesstypes.Drive, effect harnesstypes.OperationState) (harnesstypes.ProcedureResult, error) {
	var task harnesstypes.SummaryTask
	var resultEntryID string
	switch typed := effect.(type) {
	case *harnesstypes.SummaryEffectPendingOperation:
		task = typed.Task
		resultEntryID = typed.SummaryContext.ResultEntryID
	default:
		return harnesstypes.ProcedureResult{}, &laneInvariantError{message: "Structural finish requires a summary effect"}
	}
	value, err := lane.SettleOperation(effect, func(
		state *harnesstypes.RuntimeLaneState,
		current harnesstypes.OperationState,
		_ harnesstypes.OperationMeta,
		_ harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		summary := ""
		if task.CustomInstructions != nil {
			summary = *task.CustomInstructions
		}
		parentID := state.TipID
		base := harnesstypes.EntryBase{ID: resultEntryID, ParentID: parentID, Type: harnesstypes.EntryTypeCompaction}
		var entry harnesstypes.NewEntry
		switch task.Boundary.Kind {
		case "commit_navigation":
			entry = harnesstypes.BranchSummaryEntry{
				EntryBase: base,
				FromID:    state.TipID,
				Summary:   summary,
			}
		default:
			entry = harnesstypes.CompactionEntry{
				EntryBase: base,
				Summary:   summary,
			}
		}
		writes := []harnesstypes.Write{
			harnesssession.InsertEntry(entry),
			harnesssession.SetValue(harnesssession.BranchTip(lane.Name), resultEntryID),
		}
		next := &harnesstypes.CheckpointOperation{
			OperationScope: harnesstypes.OperationScopeOf(current),
			CheckpointData: harnesstypes.CheckpointData{
				Continuation:   harnesstypes.Continuation{Kind: "need_assistant"},
				TriggerEntryID: resultEntryID,
			},
			At: harnesstypes.OperationAtCheckpoint,
		}
		result := procedureContinue()
		return harnesstypes.OperationCommand[any]{
			Kind:           harnesstypes.OperationCommandCommit,
			Writes:         writes,
			OperationState: next,
			Lane:           lanePatch(&resultEntryID, state.Inbox, state.Configuration),
			Result:         boxProcedure(result),
		}, nil
	}, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	return asProcedure(value), nil
}

// CommitNavigation commits one prepared navigation result.
func CommitNavigation(lane *Lane, drive *harnesstypes.Drive, navigation *harnesstypes.NavigationReadyToCommitOperation) (harnesstypes.ProcedureResult, error) {
	value, err := lane.SettleOperation(navigation, func(
		state *harnesstypes.RuntimeLaneState,
		current harnesstypes.OperationState,
		meta harnesstypes.OperationMeta,
		reader harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		target := state.TipID
		if navigation.TargetID != nil {
			target = navigation.TargetID
		}
		record, err := OperationResultRecord(meta, harnesstypes.TerminalStatusCompleted, target, nil)
		if err != nil {
			return harnesstypes.OperationCommand[any]{}, err
		}
		cleanup, err := OperationCleanupWrites(reader, drive.OperationID, current, drive.Context)
		if err != nil {
			return harnesstypes.OperationCommand[any]{}, err
		}
		writes := append([]harnesstypes.Write{}, cleanup...)
		if target != nil {
			writes = append(writes, harnesssession.SetValue(harnesssession.BranchTip(lane.Name), *target))
		}
		result := procedureSettled(record)
		return harnesstypes.OperationCommand[any]{
			Kind:   harnesstypes.OperationCommandFinish,
			Writes: writes,
			Record: &record,
			Lane:   lanePatch(target, state.Inbox, state.Configuration),
			Result: boxProcedure(result),
		}, nil
	}, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	return asProcedure(value), nil
}

func commitState(next harnesstypes.OperationState) OperationPlanner {
	return func(
		_ *harnesstypes.RuntimeLaneState,
		_ harnesstypes.OperationState,
		_ harnesstypes.OperationMeta,
		_ harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		result := procedureContinue()
		return harnesstypes.OperationCommand[any]{
			Kind:           harnesstypes.OperationCommandCommit,
			OperationState: next,
			Result:         boxProcedure(result),
		}, nil
	}
}

func asProcedure(value any) harnesstypes.ProcedureResult {
	if result, ok := value.(harnesstypes.ProcedureResult); ok {
		return result
	}
	return procedureContinue()
}
