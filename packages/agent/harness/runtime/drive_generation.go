// This file carries the assistant generation procedure of
// packages/agent/src/harness/runtime/drive/generation.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// One generation reserves a response entry and usage id, opens the provider
// stream through the injected Models facade, records bounded frames, and then
// publishes the settled message.
package agentruntime

import (
	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnessexecution "github.com/minifish-org/pith/packages/agent/harness/execution"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// frameObserver records bounded assistant frames while forwarding lifecycle
// observation to the caller-provided observer.
type frameObserver struct {
	frames  ProgressChannel[aiutils.AssistantMessageFrame]
	encoder *aiutils.AssistantMessageFrameEncoder
}

func (o *frameObserver) record(event aitypes.AssistantMessageEvent) error {
	if o.frames == nil || o.encoder == nil {
		return nil
	}
	frame, err := o.encoder.Encode(event)
	if err != nil {
		return err
	}
	if frame != nil {
		o.frames.Write(*frame)
	}
	return nil
}

// Start records the start event.
func (o *frameObserver) Start(_ aitypes.AssistantMessage, event aitypes.AssistantMessageEvent, _ harnesscontext.Context) error {
	return o.record(event)
}

// Update records an update event.
func (o *frameObserver) Update(_ aitypes.AssistantMessage, event aitypes.AssistantMessageEvent, _ harnesscontext.Context) error {
	return o.record(event)
}

// End records the terminal event.
func (o *frameObserver) End(_ aitypes.AssistantMessage, _ harnesscontext.Context) error { return nil }

// RunRetryWait advances or reports the wait for one retry phase.
func RunRetryWait(lane *Lane, drive *harnesstypes.Drive, wait *harnesstypes.AssistantRetryWaitOperation) (harnesstypes.ProcedureResult, error) {
	if !drive.WaitForRetry {
		outcome := harnesstypes.DriveOutcome{
			Kind:        "waiting",
			OperationID: drive.OperationID,
			Reason:      "retry",
			NotBefore:   &wait.NotBefore,
		}
		return procedureWaiting(outcome), nil
	}
	if err := WaitUntil(drive.Context, wait.NotBefore); err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	scope := harnesstypes.OperationScopeOf(wait)
	next := &harnesstypes.AssistantReadyOperation{
		OperationScope:           scope,
		AssistantGenerationScope: wait.AssistantGenerationScope,
		At:                       harnesstypes.OperationAtAssistantReady,
		NextAttempt:              wait.NextAttempt,
	}
	result, err := lane.SettleOperation(wait, func(
		_ *harnesstypes.RuntimeLaneState,
		_ harnesstypes.OperationState,
		_ harnesstypes.OperationMeta,
		_ harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		boxed := procedureContinue()
		return harnesstypes.OperationCommand[any]{
			Kind:           harnesstypes.OperationCommandCommit,
			OperationState: next,
			Result:         boxProcedure(boxed),
		}, nil
	}, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if value, ok := result.(harnesstypes.ProcedureResult); ok {
		return value, nil
	}
	return procedureContinue(), nil
}

// RunGeneration streams one assistant response and publishes it.
func RunGeneration(lane *Lane, drive *harnesstypes.Drive, ready *harnesstypes.AssistantReadyOperation) (harnesstypes.ProcedureResult, error) {
	scope := harnesstypes.OperationScopeOf(ready)
	if scope.Control.Status == "cancel_requested" {
		return procedureContinue(), nil
	}
	identity := ready.GenerationContext.Configuration.Model
	responseEntryID := lane.Session.IDGenerator().Next(nil)
	usageID := lane.Session.IDGenerator().Next(nil)
	model, ok := lane.Models.GetModel(identity.Provider, identity.ModelID)
	intendedOutputLimit := 0.0
	if model != nil {
		intendedOutputLimit = model.MaxTokens
	}
	effect := &harnesstypes.AssistantEffectPendingOperation{
		OperationScope:           scope,
		AssistantGenerationScope: ready.AssistantGenerationScope,
		At:                       harnesstypes.OperationAtAssistantEffectPending,
		Attempt:                  ready.NextAttempt,
		ResponseEntryID:          responseEntryID,
		UsageID:                  usageID,
		IntendedOutputLimit:      intendedOutputLimit,
	}
	if !ok {
		return PublishConfigurationFailure(lane, drive, effect, harnesstypes.OperationError{
			Code:    "model_unavailable",
			Message: "The configured model is unavailable in this process",
		})
	}
	lifecycle := OpenAssistantResponse(lane, drive, responseEntryID, false)
	message, err := lane.generateAssistant(drive, ready, model, lifecycle)
	if err != nil {
		_ = lifecycle.Close()
		return harnesstypes.ProcedureResult{}, err
	}
	if err := lifecycle.Close(); err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	return PublishResponse(lane, drive, effect, message, PublishOptions{})
}

func (l *Lane) generateAssistant(
	drive *harnesstypes.Drive,
	ready *harnesstypes.AssistantReadyOperation,
	model *aitypes.Model,
	lifecycle *AssistantResponseLifecycle,
) (aitypes.AssistantMessage, error) {
	contextResult, err := ReadBoundedContext(l, drive, ready)
	if err != nil {
		return aitypes.AssistantMessage{}, err
	}
	messages := []agenttypes.AgentMessage{}
	if contextResult.Value != nil {
		messages = *contextResult.Value
	}
	config := l.ReadConfig()
	systemPrompt := ""
	if config.SystemPrompt != nil {
		systemPrompt = *config.SystemPrompt
	} else if config.SystemPromptFn != nil {
		prompt, err := config.SystemPromptFn(nil, drive.Context)
		if err != nil {
			return aitypes.AssistantMessage{}, err
		}
		systemPrompt = prompt
	}
	providerMessages := []aitypes.Message{}
	if config.ToProviderMessages != nil {
		providerMessages, err = config.ToProviderMessages(messages, drive.Context)
		if err != nil {
			return aitypes.AssistantMessage{}, err
		}
	}
	requestContext := harnessexecution.HarnessAssistantRequestContext{
		SystemPrompt: systemPrompt,
		Messages:     providerMessages,
	}
	options := aitypes.SimpleStreamOptions{}
	stream, err := l.Models.StreamAssistant(model, requestContext, &options, drive.Context)
	if err != nil {
		return aitypes.AssistantMessage{}, err
	}
	observer := &frameObserver{frames: lifecycle.Frames, encoder: lifecycle.encoder}
	return harnessexecution.ConsumeAssistantStream(stream, observer, nil, drive.Context)
}
