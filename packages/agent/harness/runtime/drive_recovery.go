// This file carries assistant effect recovery of
// packages/agent/src/harness/runtime/drive/recovery.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// An orphaned assistant request is settled from its bounded committed frame
// prefix without another provider call. The external outcome is explicitly
// unknown, so the synthetic message records that in its error text.
package agentruntime

import (
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

const interruptedWarning = "Assistant request was interrupted. The preceding content is the latest committed partial; newer live output may be missing and the external outcome is unknown."

func interruptedAssistantMessage(identity harnesstypes.ModelIdentity, partial *aitypes.AssistantMessage) aitypes.AssistantMessage {
	if partial == nil {
		message := aitypes.NewAssistantMessage("unknown", aitypes.ProviderId(identity.Provider), identity.ModelID, NowMs())
		message.Content = []aitypes.ContentBlock{}
		message.StopReason = aitypes.StopReasonError
		warning := interruptedWarning
		message.ErrorMessage = &warning
		return message
	}
	message := *partial
	message.Usage = aitypes.Usage{}
	message.StopReason = aitypes.StopReasonError
	warning := interruptedWarning
	message.ErrorMessage = &warning
	return message
}

// RecoverAssistantGeneration settles an orphaned assistant request without a
// provider call.
func RecoverAssistantGeneration(lane *Lane, drive *harnesstypes.Drive, generation *harnesstypes.AssistantEffectPendingOperation) (harnesstypes.ProcedureResult, error) {
	frames, err := ReadAssistantFrames(lane.Session, drive.OperationID, generation.ResponseEntryID, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if scope := harnesstypes.OperationScopeOf(generation); scope.Control.Status == "cancel_requested" {
		return recoverCancelled(lane, drive, generation, frames)
	}
	partial, err := aiutils.ReduceAssistantMessageFrames(frames)
	if err != nil {
		partial = nil
	}
	message := interruptedAssistantMessage(generation.GenerationContext.Configuration.Model, partial)
	return PublishResponse(lane, drive, generation, message, PublishOptions{Recovery: true})
}

// RecoverCancelledAssistantEffect synthetically settles one cancelled orphaned
// assistant or deferred effect under its reserved ids.
func RecoverCancelledAssistantEffect(lane *Lane, drive *harnesstypes.Drive, effect harnesstypes.OperationState) (harnesstypes.ProcedureResult, error) {
	responseEntryID := responseEntryIDOf(effect)
	frames, err := ReadAssistantFrames(lane.Session, drive.OperationID, responseEntryID, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	return recoverCancelled(lane, drive, effect, frames)
}

func recoverCancelled(
	lane *Lane,
	drive *harnesstypes.Drive,
	effect harnesstypes.OperationState,
	frames []aiutils.AssistantMessageFrame,
) (harnesstypes.ProcedureResult, error) {
	identity := responseConfigurationOf(effect).Model
	partial, err := aiutils.ReduceAssistantMessageFrames(frames)
	if err != nil {
		partial = nil
	}
	message := interruptedAssistantMessage(identity, partial)
	return PublishResponse(lane, drive, effect, message, PublishOptions{Recovery: true})
}
