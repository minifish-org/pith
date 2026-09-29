// This file carries the drive dispatcher of
// packages/agent/src/harness/runtime/drive.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// driveOperation runs one installed pass through durable procedures until it
// settles or reaches a durable wait. A procedure that makes no progress from
// its starting leaf is an invariant violation, never a silent retry.
package agentruntime

import (
	"errors"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// procedureContinue is the shared continue result.
func procedureContinue() harnesstypes.ProcedureResult {
	return harnesstypes.ProcedureResult{Kind: harnesstypes.ProcedureResultContinue}
}

func procedureSettled(record harnesstypes.OperationResultRecord) harnesstypes.ProcedureResult {
	copy := record
	return harnesstypes.ProcedureResult{Kind: harnesstypes.ProcedureResultSettled, Record: &copy}
}

func procedureWaiting(outcome harnesstypes.DriveOutcome) harnesstypes.ProcedureResult {
	copy := outcome
	return harnesstypes.ProcedureResult{Kind: harnesstypes.ProcedureResultWaiting, Outcome: &copy}
}

// boxProcedure boxes a procedure result for the type-erased command result.
func boxProcedure(value harnesstypes.ProcedureResult) *any {
	var boxed any = value
	return &boxed
}

// currentOperation returns the matching installed operation.
func currentOperation(lane *Lane, drive *harnesstypes.Drive) (*harnesstypes.Operation, error) {
	lane.mu.Lock()
	defer lane.mu.Unlock()
	operation := lane.state.Operation
	if operation == nil || operation.Meta.OperationID != drive.OperationID {
		return nil, &laneInvariantError{message: "Drive " + drive.OperationID + " has no matching current operation"}
	}
	return operation, nil
}

// DriveOperation drives one installed pass through direct durable procedures
// until settlement or a durable wait.
func DriveOperation(lane *Lane, drive *harnesstypes.Drive) (harnesstypes.DriveOutcome, error) {
	operation, err := currentOperation(lane, drive)
	if err != nil {
		return harnesstypes.DriveOutcome{}, err
	}
	scope := harnesstypes.OperationScopeOf(operation.State)
	if scope.Control.Status == "running" && lane.Hooks != nil {
		if _, err := lane.Hooks.RunWithGate("before_drive", drive.OperationID, drive.Gate, drive.Context); err != nil {
			return harnesstypes.DriveOutcome{}, err
		}
	}

	for {
		operation, err = currentOperation(lane, drive)
		if err != nil {
			return harnesstypes.DriveOutcome{}, err
		}
		state := operation.State
		var result harnesstypes.ProcedureResult
		if scope := harnesstypes.OperationScopeOf(state); scope.Control.Status == "cancel_requested" {
			result, err = ReconcileOperation(lane, drive)
		} else {
			switch typed := state.(type) {
			case *harnesstypes.StartingOperation:
				result, err = StartRun(lane, drive, typed)
			case *harnesstypes.CheckpointOperation:
				result, err = RunCheckpoint(lane, drive, typed)
			case *harnesstypes.AssistantReadyOperation:
				result, err = RunGeneration(lane, drive, typed)
			case *harnesstypes.AssistantRetryWaitOperation:
				result, err = RunRetryWait(lane, drive, typed)
			case *harnesstypes.AssistantEffectPendingOperation:
				result, err = RecoverAssistantGeneration(lane, drive, typed)
			case *harnesstypes.ToolsOperation:
				result, err = RunTools(lane, drive, typed)
			case *harnesstypes.DeferredSuspendedOperation:
				result, err = RunDeferred(lane, drive, typed)
			case *harnesstypes.DeferredEffectPendingOperation:
				result, err = RunDeferred(lane, drive, typed)
			case *harnesstypes.SummaryDecidingOperation:
				result, err = RunStructuralDecision(lane, drive, typed)
			case *harnesstypes.SummaryReadyOperation:
				result, err = RunStructuralGeneration(lane, drive, typed)
			case *harnesstypes.SummaryEffectPendingOperation:
				result, err = RecoverStructuralGeneration(lane, drive, typed)
			case *harnesstypes.SummaryRetryWaitOperation:
				result, err = RunStructuralRetryWait(lane, drive, typed)
			case *harnesstypes.NavigationReadyToCommitOperation:
				result, err = CommitNavigation(lane, drive, typed)
			default:
				err = errors.New("drive: unsupported operation leaf")
			}
		}
		if err != nil {
			return harnesstypes.DriveOutcome{}, err
		}

		switch result.Kind {
		case harnesstypes.ProcedureResultSettled:
			outcome := harnesstypes.DriveOutcome{Kind: "settled"}
			outcome.Outcome = result.Record
			return outcome, nil
		case harnesstypes.ProcedureResultWaiting:
			if result.Outcome != nil {
				return *result.Outcome, nil
			}
			return harnesstypes.DriveOutcome{}, errors.New("drive: waiting result without an outcome")
		default:
			next, nextErr := currentOperation(lane, drive)
			if nextErr != nil {
				if lane.ClosedError() != nil {
					return harnesstypes.DriveOutcome{}, lane.ClosedError()
				}
				return harnesstypes.DriveOutcome{}, nextErr
			}
			if next.State == state {
				scope := harnesstypes.OperationScopeOf(operation.State)
				if scope.Control.Status != "cancel_requested" {
					return harnesstypes.DriveOutcome{}, &laneInvariantError{message: "Drive procedure made no progress from " + string(next.State.StateAt())}
				}
			}
		}
	}
}

var _ = errors.New
