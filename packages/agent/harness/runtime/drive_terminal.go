// This file carries the terminal operation transaction of
// packages/agent/src/harness/runtime/drive/terminal.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A terminal transaction deletes every value the operation owned and records
// the immutable observation. The clock is injectable so durable endedAt values
// stay reproducible in tests.
package agentruntime

import (
	"time"

	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
)

// NowMs is the runtime wall clock in Unix milliseconds. Tests may replace it
// to make terminal records deterministic.
var NowMs = func() float64 { return float64(time.Now().UnixMilli()) }

// OperationCleanupWrites builds the mechanical operation-owned suffix used by
// an owning procedure's terminal transaction.
func OperationCleanupWrites(
	reader harnesstypes.SessionReader,
	operationID string,
	state harnesstypes.OperationState,
	ctx harnesstypes.Context,
) ([]harnesstypes.Write, error) {
	toolArguments, err := reader.ScanValues(harnesssession.OperationToolArgsPrefix(operationID), ctx)
	if err != nil {
		return nil, err
	}
	toolMemos, err := reader.ScanValues(harnesssession.OperationToolMemoPrefix(operationID), ctx)
	if err != nil {
		return nil, err
	}
	preparations, err := reader.ScanValues(harnesssession.OperationPreparationPrefix(operationID), ctx)
	if err != nil {
		return nil, err
	}
	toolOutputs, err := reader.ScanValues(harnesssession.PendingToolOutputPrefix(operationID), ctx)
	if err != nil {
		return nil, err
	}

	pendingIDs := map[string]bool{}
	var frameDelete harnesstypes.Write
	switch typed := state.(type) {
	case *harnesstypes.ToolsOperation:
		for _, call := range typed.Batch.Calls {
			if call.Status == "outcome_ready" {
				pendingIDs[call.ResultEntryID] = true
			}
		}
	case *harnesstypes.AssistantEffectPendingOperation:
		frameDelete = harnesssession.DeleteList(harnesssession.PendingAssistantFrames(operationID, typed.ResponseEntryID))
	case *harnesstypes.DeferredEffectPendingOperation:
		frameDelete = harnesssession.DeleteList(harnesssession.PendingAssistantFrames(operationID, typed.ResponseEntryID))
	}

	writes := []harnesstypes.Write{
		harnesssession.DeleteValue(harnesssession.OperationMeta(operationID)),
		harnesssession.DeleteValue(harnesssession.OperationState(operationID)),
	}
	for _, stored := range toolArguments {
		writes = append(writes, harnesssession.DeleteValue(stored.Address))
	}
	for _, stored := range toolMemos {
		writes = append(writes, harnesssession.DeleteValue(stored.Address))
	}
	for _, stored := range preparations {
		writes = append(writes, harnesssession.DeleteValue(stored.Address))
	}
	for _, stored := range toolOutputs {
		writes = append(writes, harnesssession.DeleteValue(stored.Address))
	}
	if frameDelete != nil {
		writes = append(writes, frameDelete)
	}
	for id := range pendingIDs {
		writes = append(writes, harnesssession.DeleteValue(harnesssession.PendingEntry(id)))
	}
	return writes, nil
}

// OperationResultRecord constructs the immutable observation record for one
// terminal decision. Only a failed record may carry an error.
func OperationResultRecord(
	meta harnesstypes.OperationMeta,
	status harnesstypes.TerminalStatus,
	tipID *string,
	operationError *harnesstypes.OperationError,
) (harnesstypes.OperationResultRecord, error) {
	if (status == harnesstypes.TerminalStatusFailed) != (operationError != nil) {
		return harnesstypes.OperationResultRecord{}, errOnlyFailedCarriesError
	}
	return harnesstypes.OperationResultRecord{
		OperationID: meta.OperationID,
		Kind:        meta.Intent.Kind,
		Status:      status,
		Error:       operationError,
		FromTipID:   meta.SourceTipID,
		TipID:       tipID,
		StartedAt:   meta.StartedAt,
		EndedAt:     NowMs(),
	}, nil
}
