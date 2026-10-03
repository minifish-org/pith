package harness

import (
	"context"
	"encoding/json"

	"github.com/minifish-org/pith/packages/durable"
)

// Built-in task kind names.
const (
	generationTaskName = "pi.generation"
	toolTaskName       = "pi.tool"
	compactionTaskName = "pi.compaction"
)

// LiveDoc is the built-in live conversation state document.
var LiveDoc, _ = durable.DefineDoc(durable.DocumentDefinition{
	Kind:    "pi.live",
	Version: 1,
	Scope:   durable.ScopeConversation,
	History: durable.HistoryLatest,
	Fork:    durable.ForkInitial,
	Initial: func(json.RawMessage) (durable.JsonObject, error) { return durable.JsonObject{}, nil },
	CheckpointWhen: func(value durable.JsonObject, _ []chordOp, _ durable.CheckpointInfo) (bool, error) {
		if _, ok := value["generation"]; ok {
			return false, nil
		}
		for _, slot := range liveTools(value) {
			if str(slot["status"]) == "running" {
				return false, nil
			}
		}
		return true, nil
	},
})

func liveAddress(conversationID durable.ConversationID) durable.DocumentAddress {
	return durable.ConversationAddress(LiveDoc, conversationID, nil)
}

// loadLive returns the live doc draft of a conversation.
func loadLive(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID) (map[string]any, error) {
	draft, err := tx.Doc(ctx, *LiveDoc, liveAddress(conversationID), nil)
	if err != nil {
		return nil, err
	}
	return draft.Value()
}

// liveTools returns the current tool slots of the live state.
func liveTools(live map[string]any) []map[string]any {
	raw, ok := live["tools"].([]any)
	if !ok {
		return nil
	}
	slots := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if slot, ok := item.(map[string]any); ok {
			slots = append(slots, slot)
		}
	}
	return slots
}

func liveRun(live map[string]any) map[string]any {
	run, _ := live["run"].(map[string]any)
	return run
}

func liveGeneration(live map[string]any) map[string]any {
	gen, _ := live["generation"].(map[string]any)
	return gen
}

func liveCompactions(live map[string]any) []map[string]any {
	raw, ok := live["compactions"].([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if status, ok := item.(map[string]any); ok {
			out = append(out, status)
		}
	}
	return out
}

// endRun ends the run owned by taskID, settles its inputs, and clears the
// generation and tool presentation.
func endRun(ctx context.Context, tx durable.Tx, live map[string]any, taskID durable.TaskID, settlement durable.SubmissionSettlement) error {
	run := liveRun(live)
	if run != nil && idEqual(run["taskId"], taskID) {
		if inputs, ok := run["inputs"].([]any); ok {
			for _, input := range inputs {
				if id, ok := asSubmissionID(input); ok {
					if err := tx.SettleSubmission(id, settlement); err != nil {
						return err
					}
				}
			}
		}
		delete(live, "run")
	}
	delete(live, "generation")
	delete(live, "tools")
	return nil
}

// runInputs returns the run's input submission IDs.
func runInputs(live map[string]any) []durable.SubmissionID {
	run := liveRun(live)
	if run == nil {
		return nil
	}
	inputs, _ := run["inputs"].([]any)
	var ids []durable.SubmissionID
	for _, input := range inputs {
		if id, ok := asSubmissionID(input); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// setRun records the current run owner and inputs.
func setRun(live map[string]any, taskID durable.TaskID, inputs []durable.SubmissionID) {
	list := make([]any, 0, len(inputs))
	for _, id := range inputs {
		list = append(list, int64(id))
	}
	live["run"] = map[string]any{"taskId": int64(taskID), "inputs": list}
}

func addCompactionStatus(live map[string]any, status map[string]any) {
	existing, _ := live["compactions"].([]any)
	live["compactions"] = append(existing, status)
}

func compactionStatus(live map[string]any, taskID durable.TaskID) map[string]any {
	for _, status := range liveCompactions(live) {
		if idEqual(status["taskId"], taskID) {
			return status
		}
	}
	return nil
}

func removeCompactionStatus(live map[string]any, taskID durable.TaskID) {
	list, _ := live["compactions"].([]any)
	kept := list[:0]
	for _, item := range list {
		status, ok := item.(map[string]any)
		if ok && idEqual(status["taskId"], taskID) {
			continue
		}
		kept = append(kept, item)
	}
	if len(kept) == 0 {
		delete(live, "compactions")
		return
	}
	live["compactions"] = kept
}

func toolSlot(live map[string]any, taskID durable.TaskID) map[string]any {
	for _, slot := range liveTools(live) {
		if idEqual(slot["taskId"], taskID) {
			return slot
		}
	}
	return nil
}

func toolSlotByCall(live map[string]any, callID string) map[string]any {
	for _, slot := range liveTools(live) {
		if str(slot["callId"]) == callID {
			return slot
		}
	}
	return nil
}

func finishSlot(slot map[string]any, entry *durable.EntryID) {
	slot["status"] = "done"
	if entry != nil {
		slot["entry"] = int64(*entry)
	}
	clearProgress(slot)
}

func clearProgress(slot map[string]any) {
	delete(slot, "output")
	delete(slot, "droppedBytes")
	delete(slot, "droppedLines")
	delete(slot, "details")
	delete(slot, "diagnostics")
}

// settleSchedulerOutcome is the harness cleanup for a scheduler-written outcome.
func settleSchedulerOutcome(ctx context.Context, tx durable.Tx, record durable.TaskRecord, outcome durable.TaskOutcome) error {
	switch record.Kind {
	case toolTaskName:
		live, err := loadLive(ctx, tx, record.ConversationID)
		if err != nil {
			return err
		}
		if slot := toolSlot(live, record.ID); slot != nil {
			finishSlot(slot, nil)
		}
		return nil
	case compactionTaskName:
		live, err := loadLive(ctx, tx, record.ConversationID)
		if err != nil {
			return err
		}
		removeCompactionStatus(live, record.ID)
		return nil
	case generationTaskName:
		live, err := loadLive(ctx, tx, record.ConversationID)
		if err != nil {
			return err
		}
		if !idEqual(liveRun(live)["taskId"], record.ID) {
			return nil
		}
		if err := convertPartial(ctx, tx, live, record.ConversationID); err != nil {
			return err
		}
		reason := outcome.Reason
		if outcome.Status == durable.OutcomeFaulted && outcome.Error != nil {
			reason = "faulted"
		}
		return endRun(ctx, tx, live, record.ID, durable.SubmissionSettlement{Status: "unanswered", Reason: reason})
	}
	return nil
}
