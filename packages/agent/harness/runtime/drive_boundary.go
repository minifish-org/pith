// This file carries the run boundary of
// packages/agent/src/harness/runtime/drive/boundary.ts.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
//
// A boundary selects the lane-owned inbox input that becomes durable at the
// current tip, then either renews work or records the terminal run result.
package agentruntime

import (
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// BoundaryFinishPending is the cancellation-safe finish mediation value of a
// checkpoint procedure.
type BoundaryFinishPending struct {
	Kind     string   `json:"kind"`
	EntryIDs []string `json:"entryIds"`
}

// BoundaryPlacement is one materialized boundary input plus its writes.
type BoundaryPlacement struct {
	Entries        []harnesstypes.NewEntry
	Writes         []harnesstypes.Write
	TipID          *string
	Inbox          []harnesstypes.InboxItem
	TriggerEntryID *string
	Queues         []LaneQueuedItem
}

// NormalizedRetryPolicy snapshots the active retry policy for one generation.
func NormalizedRetryPolicy(lane *Lane) harnesstypes.NormalizedRetryPolicy {
	retry := lane.ReadConfig().RetryPolicy
	maxAttempts := 1
	if retry.Enabled {
		maxAttempts = retry.MaxRetries + 1
	}
	maxAgentDelay := aiutils.DefaultMaxAgentRetryDelayMs
	if retry.MaxAgentDelayMs != nil {
		maxAgentDelay = *retry.MaxAgentDelayMs
	}
	return harnesstypes.NormalizedRetryPolicy{
		MaxAttempts:     maxAttempts,
		BaseDelayMs:     retry.BaseDelayMs,
		MaxAgentDelayMs: maxAgentDelay,
	}
}

// AssistantReadyAtBoundary builds the next assistant generation state.
func AssistantReadyAtBoundary(
	lane *Lane,
	state *harnesstypes.RuntimeLaneState,
	scope harnesstypes.OperationScope,
	triggerEntryID string,
	overflowRecoveryUsed bool,
) *harnesstypes.AssistantReadyOperation {
	config := lane.ReadConfig()
	stepID := lane.Session.IDGenerator().Next(nil)
	return &harnesstypes.AssistantReadyOperation{
		OperationScope: scope,
		AssistantGenerationScope: harnesstypes.AssistantGenerationScope{
			GenerationContext: harnesstypes.GenerationContext{
				StepID:               stepID,
				TriggerEntryID:       triggerEntryID,
				Configuration:        state.Configuration,
				StreamOptions:        config.StreamOptions,
				RetryPolicy:          NormalizedRetryPolicy(lane),
				OverflowRecoveryUsed: overflowRecoveryUsed,
			},
		},
		At:          harnesstypes.OperationAtAssistantReady,
		NextAttempt: 1,
	}
}

func pendingEntryOf(reader harnesstypes.SessionReader, entryID string, ctx harnesstypes.Context) (harnesstypes.PendingEntry, error) {
	stored, ok, err := reader.GetValue(harnesssession.PendingEntry(entryID), ctx)
	if err != nil {
		return harnesstypes.PendingEntry{}, err
	}
	if !ok {
		return harnesstypes.PendingEntry{}, errPendingEntryMissingPayload
	}
	pending, ok := stored.Value.(harnesstypes.PendingEntry)
	if !ok {
		return harnesstypes.PendingEntry{}, errPendingEntryMissingPayload
	}
	return pending, nil
}

// PlanBoundaryInbox selects and materializes one boundary's lane-owned input
// without committing it.
func PlanBoundaryInbox(
	lane *Lane,
	drive *harnesstypes.Drive,
	state *harnesstypes.RuntimeLaneState,
	scope harnesstypes.OperationScope,
	reader harnesstypes.SessionReader,
	tipID *string,
	followUpWhenNoTrigger bool,
) (BoundaryPlacement, error) {
	steer := []harnesstypes.InboxItem{}
	for _, item := range state.Inbox {
		if item.Kind == harnesstypes.InboxItemSteer {
			steer = append(steer, item)
		}
	}
	selectedSteer := steer
	if scope.Settings.SteeringMode != agenttypes.QueueModeAll && len(steer) > 1 {
		selectedSteer = steer[:1]
	}
	selected := []harnesstypes.InboxItem{}
	for _, item := range state.Inbox {
		if item.Kind == harnesstypes.InboxItemWrite {
			selected = append(selected, item)
			continue
		}
		for _, candidate := range selectedSteer {
			if candidate.EntryID == item.EntryID {
				selected = append(selected, item)
			}
		}
	}

	projects := func(pending harnesstypes.PendingEntry) bool {
		if pending.Type == "message" {
			return true
		}
		config := lane.ReadConfig()
		_, ok := config.EntryProjectors[pending.CustomType]
		return ok
	}

	hasProjection := false
	for _, item := range selected {
		pending, err := pendingEntryOf(reader, item.EntryID, drive.Context)
		if err != nil {
			return BoundaryPlacement{}, err
		}
		if projects(pending) {
			hasProjection = true
		}
	}
	if followUpWhenNoTrigger && !hasProjection {
		followUp := []harnesstypes.InboxItem{}
		for _, item := range state.Inbox {
			if item.Kind == harnesstypes.InboxItemFollowUp {
				followUp = append(followUp, item)
			}
		}
		if scope.Settings.FollowUpMode != agenttypes.QueueModeAll && len(followUp) > 1 {
			followUp = followUp[:1]
		}
		selected = append(selected, followUp...)
	}

	parentID := tipID
	var triggerEntryID *string
	entries := []harnesstypes.NewEntry{}
	selectedIDs := map[string]bool{}
	for _, item := range state.Inbox {
		for _, candidate := range selected {
			if candidate.EntryID == item.EntryID {
				selectedIDs[item.EntryID] = true
			}
		}
	}
	ordered := []harnesstypes.InboxItem{}
	for _, item := range state.Inbox {
		if selectedIDs[item.EntryID] {
			ordered = append(ordered, item)
		}
	}
	for _, item := range ordered {
		pending, err := pendingEntryOf(reader, item.EntryID, drive.Context)
		if err != nil {
			return BoundaryPlacement{}, err
		}
		base := harnesstypes.EntryBase{
			ID:       item.EntryID,
			ParentID: parentID,
			Type:     harnesstypes.EntryTypeMessage,
		}
		var entry harnesstypes.NewEntry
		if pending.Type == "message" {
			message := agenttypes.AgentMessage{}
			if pending.Payload != nil {
				message = *pending.Payload
			}
			entry = harnesstypes.MessageEntry{EntryBase: base, Message: message}
		} else {
			base.Type = harnesstypes.EntryTypeCustom
			customType := pending.CustomType
			base.CustomType = &customType
			entry = harnesstypes.CustomEntry{EntryBase: base, CustomType: pending.CustomType, Data: pending.Data}
		}
		entries = append(entries, entry)
		id := item.EntryID
		parentID = &id
		if projects(pending) {
			triggerEntryID = &id
		}
	}

	inbox := []harnesstypes.InboxItem{}
	for _, item := range state.Inbox {
		if !selectedIDs[item.EntryID] {
			inbox = append(inbox, item)
		}
	}

	writes := []harnesstypes.Write{}
	for _, entry := range entries {
		writes = append(writes, harnesssession.InsertEntry(entry))
	}
	for _, item := range ordered {
		writes = append(writes, harnesssession.DeleteValue(harnesssession.PendingEntry(item.EntryID)))
	}
	if len(entries) > 0 {
		writes = append(writes, harnesssession.SetValue(harnesssession.BranchTip(lane.Name), parentID))
	}

	placement := BoundaryPlacement{
		Entries:        entries,
		Writes:         writes,
		TipID:          parentID,
		Inbox:          inbox,
		TriggerEntryID: triggerEntryID,
	}
	if len(ordered) > 0 {
		queues, err := ReadLaneQueues(reader, inbox, drive.Context)
		if err != nil {
			return BoundaryPlacement{}, err
		}
		placement.Queues = queues
	}
	return placement, nil
}

// BoundaryPlacementEvents builds the events for a committed boundary input.
func BoundaryPlacementEvents(
	placement BoundaryPlacement,
	commit harnesstypes.CommitResult,
	firstWriteIndex int,
	lane string,
	runID string,
) []Event {
	events := CommittedEntryEvents(placement.Entries, commit, lane, &runID, firstWriteIndex)
	if placement.Queues != nil {
		laneValue := lane
		events = append(events, Event{Type: "queue_update", Lane: &laneValue, Queues: placement.Queues})
	}
	return events
}

// FinishRunBoundary replans after before_run_end and commits either renewed
// work or the terminal run result.
func FinishRunBoundary(
	lane *Lane,
	drive *harnesstypes.Drive,
	capability harnesstypes.OperationState,
	continuation harnesstypes.Continuation,
	plannedEntryIDs []string,
	pendingEvents []Event,
) (harnesstypes.ProcedureResult, error) {
	context, err := ReadBoundedContext(lane, drive, capability)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	if context.Kind == harnesstypes.ContinueOperationCancelRequested {
		return procedureContinue(), nil
	}
	var followUp *harnesstypes.MessageEntry
	if lane.Hooks != nil {
		hookResult, err := lane.Hooks.RunWithGate("before_run_end", map[string]any{"runId": drive.OperationID}, drive.Gate, drive.Context)
		if err != nil {
			return harnesstypes.ProcedureResult{}, err
		}
		if followUpText, ok := hookResult.(string); ok && followUpText != "" {
			id := lane.Session.IDGenerator().Next(nil)
			message := agenttypes.NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage(followUpText, NowMs())))
			followUp = &harnesstypes.MessageEntry{
				EntryBase: harnesstypes.EntryBase{ID: id, Type: harnesstypes.EntryTypeMessage},
				Message:   message,
			}
		}
	}

	value, err := lane.SettleOperation(capability, func(
		state *harnesstypes.RuntimeLaneState,
		current harnesstypes.OperationState,
		meta harnesstypes.OperationMeta,
		reader harnesstypes.SessionReader,
	) (harnesstypes.OperationCommand[any], error) {
		scope := harnesstypes.OperationScopeOf(current)
		placement, err := PlanBoundaryInbox(lane, drive, state, scope, reader, state.TipID, true)
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
		if followUp != nil {
			followUp.ParentID = placement.TipID
			id := followUp.ID
			writes := append([]harnesstypes.Write{}, placement.Writes...)
			writes = append(writes, harnesssession.InsertEntry(followUp), harnesssession.SetValue(harnesssession.BranchTip(lane.Name), id))
			next := AssistantReadyAtBoundary(lane, state, scope, id, false)
			result := procedureContinue()
			return harnesstypes.OperationCommand[any]{
				Kind:           harnesstypes.OperationCommandCommit,
				Writes:         writes,
				OperationState: next,
				Lane:           lanePatch(&id, placement.Inbox, state.Configuration),
				Result:         boxProcedure(result),
			}, nil
		}
		if placement.TipID == nil {
			return harnesstypes.OperationCommand[any]{}, errRunOperationHasNoTip
		}
		record, err := OperationResultRecord(meta, harnesstypes.TerminalStatusCompleted, placement.TipID, nil)
		if err != nil {
			return harnesstypes.OperationCommand[any]{}, err
		}
		cleanup, err := OperationCleanupWrites(reader, drive.OperationID, current, drive.Context)
		if err != nil {
			return harnesstypes.OperationCommand[any]{}, err
		}
		writes := append([]harnesstypes.Write{}, placement.Writes...)
		writes = append(writes, cleanup...)
		result := procedureSettled(record)
		return harnesstypes.OperationCommand[any]{
			Kind:   harnesstypes.OperationCommandFinish,
			Writes: writes,
			Record: &record,
			Lane:   lanePatch(placement.TipID, placement.Inbox, state.Configuration),
			Result: boxProcedure(result),
		}, nil
	}, drive.Context)
	if err != nil {
		return harnesstypes.ProcedureResult{}, err
	}
	result, ok := value.(harnesstypes.ProcedureResult)
	if !ok {
		return procedureContinue(), nil
	}
	if len(pendingEvents) > 0 {
		if err := lane.EmitBatch(pendingEvents, drive.Context); err != nil {
			return harnesstypes.ProcedureResult{}, err
		}
	}
	return result, nil
}

// lanePatch builds a lane projection patch without touching the operation.
func lanePatch(tipID *string, inbox []harnesstypes.InboxItem, configuration harnesstypes.LaneConfiguration) *harnesstypes.RuntimeLaneState {
	return &harnesstypes.RuntimeLaneState{TipID: tipID, Inbox: inbox, Configuration: configuration}
}
