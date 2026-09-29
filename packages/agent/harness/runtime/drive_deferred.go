// This file carries deferred poll recovery of
// packages/agent/src/harness/runtime/drive/deferred.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A deferred source handle is read from the durable transcript. Polling is
// permit-gated: a pass without a deferred permit reports a durable wait rather
// than issuing a request. An orphaned poll is replaced under fresh ids and its
// unknown external outcome is represented, never claimed exactly-once.
package agentruntime

import (
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// ReadDeferredSourceHandle reads and validates the deferred handle recorded on
// the source assistant entry.
func ReadDeferredSourceHandle(
	reader harnesstypes.SessionReader,
	deferred *harnesstypes.DeferredScope,
	ctx harnesstypes.Context,
) (aitypes.DeferredHandle, error) {
	entries, err := reader.GetEntries([]string{deferred.SourceEntryID}, ctx)
	if err != nil {
		return aitypes.DeferredHandle{}, err
	}
	entry, ok := entries[deferred.SourceEntryID]
	if !ok {
		return aitypes.DeferredHandle{}, &laneInvariantError{message: "Deferred source " + deferred.SourceEntryID + " is missing its assistant handle"}
	}
	messageEntry, ok := entryMessage(entry)
	if !ok {
		return aitypes.DeferredHandle{}, &laneInvariantError{message: "Deferred source " + deferred.SourceEntryID + " is missing its assistant handle"}
	}
	message := messageEntry
	assistant, ok := agentMessageAssistant(message)
	if !ok || assistant.StopReason != aitypes.StopReasonDeferred || assistant.Deferred == nil {
		return aitypes.DeferredHandle{}, &laneInvariantError{message: "Deferred source " + deferred.SourceEntryID + " is missing its assistant handle"}
	}
	handle := *assistant.Deferred
	identity := deferred.Configuration.Model
	if handle.Id == "" ||
		string(handle.Provider) != identity.Provider ||
		handle.ModelId != identity.ModelID ||
		string(handle.Api) != string(assistant.Api) {
		return aitypes.DeferredHandle{}, &laneInvariantError{message: "Deferred source " + deferred.SourceEntryID + " has an invalid handle"}
	}
	return handle, nil
}

func agentMessageAssistant(message agenttypes.AgentMessage) (*aitypes.AssistantMessage, bool) {
	if message.Message == nil || message.Message.Assistant == nil {
		return nil, false
	}
	return message.Message.Assistant, true
}

func deferredScope(deferred harnesstypes.OperationState) *harnesstypes.DeferredScope {
	switch typed := deferred.(type) {
	case *harnesstypes.DeferredSuspendedOperation:
		return &typed.DeferredScope
	case *harnesstypes.DeferredEffectPendingOperation:
		return &typed.DeferredScope
	default:
		return nil
	}
}

// RunDeferredSuspended polls one durably suspended response when the pass
// carries a permit.
func RunDeferredSuspended(lane *Lane, drive *harnesstypes.Drive, deferred *harnesstypes.DeferredSuspendedOperation) (harnesstypes.ProcedureResult, error) {
	return runDeferredPoll(lane, drive, deferred, false)
}

// RecoverDeferredPoll replaces one orphaned unknown-outcome poll under fresh
// ids when the pass carries a permit.
func RecoverDeferredPoll(lane *Lane, drive *harnesstypes.Drive, deferred *harnesstypes.DeferredEffectPendingOperation) (harnesstypes.ProcedureResult, error) {
	return runDeferredPoll(lane, drive, deferred, true)
}

// RunDeferred advances or reports the wait for one deferred run phase.
func RunDeferred(lane *Lane, drive *harnesstypes.Drive, deferred harnesstypes.OperationState) (harnesstypes.ProcedureResult, error) {
	switch typed := deferred.(type) {
	case *harnesstypes.DeferredSuspendedOperation:
		return RunDeferredSuspended(lane, drive, typed)
	case *harnesstypes.DeferredEffectPendingOperation:
		return RecoverDeferredPoll(lane, drive, typed)
	default:
		return harnesstypes.ProcedureResult{}, &laneInvariantError{message: "Deferred procedure requires a deferred leaf"}
	}
}

func runDeferredPoll(
	lane *Lane,
	drive *harnesstypes.Drive,
	deferred harnesstypes.OperationState,
	recovery bool,
) (harnesstypes.ProcedureResult, error) {
	scope := deferredScope(deferred)
	if scope == nil {
		return harnesstypes.ProcedureResult{}, &laneInvariantError{message: "Deferred procedure requires a deferred leaf"}
	}
	if operationScope := harnesstypes.OperationScopeOf(deferred); operationScope.Control.Status == "cancel_requested" {
		return publishAbortedTerminal(lane, drive, deferred)
	}
	handle, err := ReadDeferredSourceHandle(lane.Session, scope, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if drive.DeferredPermits == 0 {
		outcome := harnesstypes.DriveOutcome{
			Kind:        "waiting",
			OperationID: drive.OperationID,
			Reason:      "deferred",
			Deferred:    &handle,
		}
		return procedureWaiting(outcome), nil
	}
	responseEntryID := lane.Session.IDGenerator().Next(nil)
	usageID := lane.Session.IDGenerator().Next(nil)
	effect := &harnesstypes.DeferredEffectPendingOperation{
		DeferredScope:   *scope,
		At:              harnesstypes.OperationAtDeferredEffectPending,
		ResponseEntryID: responseEntryID,
		UsageID:         usageID,
	}
	drive.DeferredPermits--
	message := interruptedAssistantMessage(scope.Configuration.Model, nil)
	return PublishResponse(lane, drive, effect, message, PublishOptions{Recovery: recovery})
}
