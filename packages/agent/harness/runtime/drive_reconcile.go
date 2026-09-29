// This file carries cancellation reconciliation of
// packages/agent/src/harness/runtime/drive/reconcile.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// Reconciliation never starts new ordinary work. Admitted effects settle, and
// every other leaf records an aborted terminal result.
package agentruntime

import (
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// ReconcileOperation advances one cancelled durable leaf.
func ReconcileOperation(lane *Lane, drive *harnesstypes.Drive) (harnesstypes.ProcedureResult, error) {
	operation, err := currentOperation(lane, drive)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if scope := harnesstypes.OperationScopeOf(operation.State); scope.Control.Status != "cancel_requested" {
		return harnesstypes.ProcedureResult{}, &laneInvariantError{message: "Operation " + drive.OperationID + " is not cancelled"}
	}
	drive.BeginAbort(nil)
	drive.SignalAbort()

	switch typed := operation.State.(type) {
	case *harnesstypes.AssistantEffectPendingOperation:
		return RecoverCancelledAssistantEffect(lane, drive, typed)
	case *harnesstypes.ToolsOperation:
		return RunTools(lane, drive, typed)
	case *harnesstypes.DeferredSuspendedOperation:
		return publishAbortedTerminal(lane, drive, typed)
	case *harnesstypes.DeferredEffectPendingOperation:
		return recoverCancelledAndPublish(lane, drive, typed)
	default:
		return publishAbortedTerminal(lane, drive, operation.State)
	}
}

func recoverCancelledAndPublish(lane *Lane, drive *harnesstypes.Drive, effect *harnesstypes.DeferredEffectPendingOperation) (harnesstypes.ProcedureResult, error) {
	if _, err := RecoverCancelledAssistantEffect(lane, drive, effect); err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	return procedureContinue(), nil
}

// publishAbortedTerminal records an aborted terminal result.
func publishAbortedTerminal(lane *Lane, drive *harnesstypes.Drive, capability harnesstypes.OperationState) (harnesstypes.ProcedureResult, error) {
	value, err := lane.SettleOperation(capability, func(
		state *harnesstypes.RuntimeLaneState,
		current harnesstypes.OperationState,
		meta harnesstypes.OperationMeta,
		reader harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		if scope := harnesstypes.OperationScopeOf(current); scope.Control.Status != "cancel_requested" {
			return harnesstypes.OperationCommand[any]{}, &laneInvariantError{message: "Cancellation reconciliation requires cancelled durable control"}
		}
		record, err := OperationResultRecord(meta, harnesstypes.TerminalStatusAborted, state.TipID, nil)
		if err != nil {
			return harnesstypes.OperationCommand[any]{}, err
		}
		cleanup, err := OperationCleanupWrites(reader, drive.OperationID, current, drive.Context)
		if err != nil {
			return harnesstypes.OperationCommand[any]{}, err
		}
		result := procedureSettled(record)
		return harnesstypes.OperationCommand[any]{
			Kind:   harnesstypes.OperationCommandFinish,
			Writes: cleanup,
			Record: &record,
			Result: boxProcedure(result),
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
