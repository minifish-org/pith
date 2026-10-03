package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
	"github.com/minifish-org/pith/packages/durable"
)

// GenerationCheckpoint is the durable checkpoint of the built-in generation
// task.
type GenerationCheckpoint struct {
	Phase         string                    `json:"phase"`
	Attempt       int                       `json:"attempt,omitempty"`
	Compacted     *durable.TaskID           `json:"compacted,omitempty"`
	Overflow      string                    `json:"overflow,omitempty"`
	Model         *ModelRef                 `json:"model,omitempty"`
	ThinkingLevel string                    `json:"thinkingLevel,omitempty"`
	StreamOptions ConversationStreamOptions `json:"streamOptions,omitempty"`
	Cutoff        *durable.EntryID          `json:"cutoff,omitempty"`
	Until         int64                     `json:"until,omitempty"`
	Handle        *types.DeferredHandle     `json:"handle,omitempty"`
	PollAt        int64                     `json:"pollAt,omitempty"`
	Assistant     *durable.EntryID          `json:"assistant,omitempty"`
	Tools         []durable.TaskID          `json:"tools,omitempty"`
	Pending       []string                  `json:"pending,omitempty"`
}

const defaultPollAfterMs = 5000

// GenerationTask returns the built-in generation task definition.
func GenerationTask() durable.TaskDefinition {
	return durable.TaskDefinition{
		Name:    generationTaskName,
		Version: 1,
		Initial: func(json.RawMessage) (json.RawMessage, error) {
			return mustJSON(GenerationCheckpoint{Phase: "prepare", Attempt: 1}), nil
		},
		Phases: map[string]durable.PhaseHandler{
			"prepare": generationPrepare,
			"request": generationRequest,
			"retry":   generationRetry,
			"poll":    generationPoll,
			"tools":   generationTools,
		},
		Abort: generationAbort,
	}
}

func parseGenerationCheckpoint(raw []byte) GenerationCheckpoint {
	var checkpoint GenerationCheckpoint
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &checkpoint)
	}
	return checkpoint
}

func generationPrepare(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	agent, err := runtime.agent(ctx)
	if err != nil {
		return err
	}
	settings := runtime.settings()
	checkpoint := parseGenerationCheckpoint(task.State.Checkpoint)
	if agent.Model == nil {
		return generationFailNoModel(ctx, runtime, nil)
	}
	model, err := runtime.modelRunner().Resolve(ctx, *agent.Model)
	if err != nil || model == nil {
		return generationFailNoModel(ctx, runtime, agent.Model)
	}
	if checkpoint.Compacted != nil && checkpoint.Overflow != "" {
		outcomes, err := runtime.Outcomes(ctx, []durable.TaskID{*checkpoint.Compacted})
		if err != nil {
			return err
		}
		result := outcomes[0].Result
		if outcomes[0].Status != durable.OutcomeCompleted || !entryIDPresent(result) {
			return generationFailModelError(ctx, runtime, checkpoint.Overflow)
		}
	}
	view, err := runtime.contextView(ctx, runtime.ConversationID(), nil)
	if err != nil {
		return err
	}
	shown := replaySections(view.Messages)
	var executionEnv interface{ ID() string }
	if built, err := runtime.environment(ctx); err == nil {
		executionEnv = built
	} else {
		runtime.Report(err)
	}
	input := PromptInput{ConversationID: runtime.ConversationID(), Agent: agent, Env: asEnv(executionEnv), Shown: shownToMap(shown), Read: runtime}
	desired := renderSections(ctx, agent.Sections, input, shown, runtime.Report)
	entries := planSystemEntries(view, desired, declarations(agent.Tools), float64(runtime.Now()))
	planned := make([]types.Message, 0)
	for _, entry := range entries {
		planned = append(planned, entry.Model...)
	}
	threshold := thresholdCompaction(view, planned, model.ContextWindow, settings.Compaction, checkpoint.Compacted == nil)
	if threshold == "blocking" {
		return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
			child, err := createCompaction(ctx, tx, runtime.ConversationID(), mustJSON(map[string]any{"reason": "threshold"}), &task.ID)
			if err != nil {
				return err
			}
			next := GenerationCheckpoint{Phase: "prepare", Attempt: checkpoint.Attempt, Compacted: &child}
			current.State = durable.TaskState{Status: durable.TaskStatusWaiting, Checkpoint: mustJSON(next), On: []durable.TaskID{child}, Policy: durable.PolicyAllSettled}
			return nil
		})
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		page, err := tx.ScanEntries(ctx, durable.EntryQuery{ConversationID: runtime.ConversationID()}, 1, nil)
		if err != nil {
			return err
		}
		var cutoff *durable.EntryID
		if len(page.Items) > 0 {
			value := page.Items[0].ID
			cutoff = &value
		}
		for _, entry := range entries {
			appended, err := tx.AppendEntry(ctx, runtime.ConversationID(), entry)
			if err != nil {
				return err
			}
			value := appended.ID
			cutoff = &value
		}
		if cutoff == nil {
			return fmt.Errorf("Conversation %d has no entries to send", runtime.ConversationID())
		}
		if threshold == "background" {
			live, err := loadLive(ctx, tx, runtime.ConversationID())
			if err != nil {
				return err
			}
			if live["compactions"] == nil {
				if _, err := createCompaction(ctx, tx, runtime.ConversationID(), mustJSON(map[string]any{"reason": "threshold"}), nil); err != nil {
					return err
				}
			}
		}
		next := GenerationCheckpoint{
			Phase:         "request",
			Attempt:       checkpoint.Attempt,
			Compacted:     checkpoint.Compacted,
			Model:         agent.Model,
			ThinkingLevel: string(agent.ThinkingLevel),
			StreamOptions: settings.Stream,
			Cutoff:        cutoff,
		}
		current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(next)}
		return nil
	})
}

func generationRequest(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	checkpoint := parseGenerationCheckpoint(task.State.Checkpoint)
	if err := runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		if err := convertPartial(ctx, tx, live, runtime.ConversationID()); err != nil {
			return err
		}
		live["generation"] = map[string]any{"attempt": checkpoint.Attempt}
		return nil
	}); err != nil {
		return err
	}
	if checkpoint.Model == nil {
		return generationFailNoModel(ctx, runtime, nil)
	}
	model, err := runtime.modelRunner().Resolve(ctx, *checkpoint.Model)
	if err != nil || model == nil {
		return generationFailNoModel(ctx, runtime, checkpoint.Model)
	}
	view, err := runtime.contextView(ctx, runtime.ConversationID(), checkpoint.Cutoff)
	if err != nil {
		return err
	}
	messages := view.Messages
	agent, err := runtime.agent(ctx)
	if err != nil {
		return err
	}
	if err := runtime.eachHook(ctx, generationTaskName, "beforeRequest", func(handler HookHandler) error {
		out, err := handler(ctx, mustJSON(map[string]any{"messages": messages}), runtime)
		if err != nil {
			return err
		}
		var replaced struct {
			Messages []types.Message `json:"messages"`
		}
		if err := json.Unmarshal(out, &replaced); err == nil && replaced.Messages != nil {
			messages = replaced.Messages
		}
		return nil
	}); err != nil {
		return err
	}
	request := ModelRequest{
		ConversationID: runtime.ConversationID(),
		Model:          *checkpoint.Model,
		Messages:       messages,
		Tools:          declarations(agent.Tools),
		ThinkingLevel:  types.ThinkingLevel(checkpoint.ThinkingLevel),
		Options:        checkpoint.StreamOptions,
	}
	message, err := streamResponse(ctx, runtime, request, checkpoint.Attempt)
	if err != nil {
		return err
	}
	return classifyResponse(ctx, runtime, checkpoint, message, view.Messages, 0)
}

func generationRetry(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	checkpoint := parseGenerationCheckpoint(task.State.Checkpoint)
	if err := runtime.Sleep(ctx, checkpoint.Until); err != nil {
		return err
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		live["generation"] = map[string]any{"attempt": checkpoint.Attempt + 1}
		next := GenerationCheckpoint{Phase: "prepare", Attempt: checkpoint.Attempt + 1, Compacted: checkpoint.Compacted}
		current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(next)}
		return nil
	})
}

func generationPoll(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	checkpoint := parseGenerationCheckpoint(task.State.Checkpoint)
	if checkpoint.Model == nil {
		return generationFailNoModel(ctx, runtime, nil)
	}
	if _, err := runtime.modelRunner().Resolve(ctx, *checkpoint.Model); err != nil {
		return generationFailNoModel(ctx, runtime, checkpoint.Model)
	}
	if err := runtime.Sleep(ctx, checkpoint.PollAt); err != nil {
		return err
	}
	deferred, ok := runtime.modelRunner().(DeferredModelRunner)
	if !ok {
		return generationFailModelError(ctx, runtime, "provider does not support deferred responses")
	}
	message, err := deferred.Poll(ctx, *checkpoint.Model, derefHandle(checkpoint.Handle))
	if err != nil {
		return err
	}
	return classifyResponse(ctx, runtime, checkpoint, message, nil, checkpoint.PollAt)
}

func generationTools(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	checkpoint := parseGenerationCheckpoint(task.State.Checkpoint)
	if len(checkpoint.Pending) == 0 {
		return finishToolRound(ctx, runtime, derefEntry(checkpoint.Assistant), checkpoint.Tools)
	}
	next := checkpoint.Pending[0]
	rest := checkpoint.Pending[1:]
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		assistant := derefEntry(checkpoint.Assistant)
		taskID, err := createToolTask(ctx, tx, runtime.TaskID(), assistant, next)
		if err != nil {
			return err
		}
		if slot := toolSlotByCall(live, next); slot != nil {
			slot["taskId"] = int64(taskID)
		}
		updated := GenerationCheckpoint{
			Phase:     "tools",
			Assistant: checkpoint.Assistant,
			Tools:     append(append([]durable.TaskID{}, checkpoint.Tools...), taskID),
			Pending:   rest,
		}
		current.State = durable.TaskState{Status: durable.TaskStatusWaiting, Checkpoint: mustJSON(updated), On: []durable.TaskID{taskID}, Policy: durable.PolicyAllSettled}
		return nil
	})
}

func generationAbort(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	checkpoint := parseGenerationCheckpoint(task.State.Checkpoint)
	if checkpoint.Phase == "poll" && checkpoint.Model != nil {
		if deferred, ok := runtime.modelRunner().(DeferredModelRunner); ok {
			if err := deferred.Cancel(ctx, *checkpoint.Model, derefHandle(checkpoint.Handle)); err != nil {
				runtime.Report(err)
			}
		}
	}
	var unstarted []types.ToolCall
	if checkpoint.Phase == "tools" {
		unstarted = readCalls(ctx, runtime, derefEntry(checkpoint.Assistant), checkpoint.Pending)
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		if err := convertPartial(ctx, tx, live, runtime.ConversationID()); err != nil {
			return err
		}
		for _, call := range unstarted {
			result := harnessError("aborted", fmt.Sprintf("Tool %s was aborted", call.Name))
			if _, err := appendToolResult(ctx, tx, runtime.ConversationID(), call, result, runtime.Now()); err != nil {
				return err
			}
		}
		if err := endRun(ctx, tx, live, runtime.TaskID(), durable.SubmissionSettlement{Status: durable.SubmissionStatusUnanswered, Reason: "aborted"}); err != nil {
			return err
		}
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeAborted}}
		return nil
	})
}

func readCalls(ctx context.Context, runtime taskRuntime, assistant durable.EntryID, callIDs []string) []types.ToolCall {
	entry, err := runtime.entryOf(ctx, entryKindAssistant, assistant)
	if err != nil || entry == nil || len(entry.Model) == 0 || entry.Model[0].Assistant == nil {
		return nil
	}
	var calls []types.ToolCall
	for _, block := range entry.Model[0].Assistant.Content {
		if block.ToolCall != nil {
			calls = append(calls, *block.ToolCall)
		}
	}
	var selected []types.ToolCall
	for _, id := range callIDs {
		for _, call := range calls {
			if call.Id == id {
				selected = append(selected, call)
				break
			}
		}
	}
	return selected
}

// streamResponse runs one request and returns the terminal message.
func streamResponse(ctx context.Context, runtime taskRuntime, request ModelRequest, attempt int) (types.AssistantMessage, error) {
	emit := func(partial types.AssistantMessage) error {
		return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
			live, err := loadLive(ctx, tx, runtime.ConversationID())
			if err != nil {
				return err
			}
			generation, _ := live["generation"].(map[string]any)
			if generation == nil {
				generation = map[string]any{"attempt": attempt}
				live["generation"] = generation
			}
			assignJSON(generation, "message", jsonValueOf(partial))
			return nil
		})
	}
	return runtime.modelRunner().Run(ctx, request, emit)
}

func jsonValueOf(message types.AssistantMessage) any {
	data, err := json.Marshal(message)
	if err != nil {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return map[string]any{}
	}
	return value
}

// classifyResponse classifies a terminal provider message.
func classifyResponse(ctx context.Context, runtime taskRuntime, checkpoint GenerationCheckpoint, message types.AssistantMessage, previous []types.Message, pollAt int64) error {
	if message.StopReason == types.StopReasonDeferred && message.Deferred != nil {
		handle := *message.Deferred
		delay := int64(defaultPollAfterMs)
		if handle.PollAfterMs != nil {
			delay = *handle.PollAfterMs
		}
		next := runtime.Now() + delay
		if pollAt != 0 && next <= pollAt {
			next = pollAt + 1
		}
		return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
			live, err := loadLive(ctx, tx, runtime.ConversationID())
			if err != nil {
				return err
			}
			live["generation"] = map[string]any{"attempt": checkpoint.Attempt, "deferred": map[string]any{"pollAt": next}}
			updated := GenerationCheckpoint{
				Phase:     "poll",
				Attempt:   checkpoint.Attempt,
				Compacted: checkpoint.Compacted,
				Model:     checkpoint.Model,
				Cutoff:    checkpoint.Cutoff,
				Handle:    &handle,
				PollAt:    next,
			}
			current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(updated)}
			return nil
		})
	}
	if err := runtime.eachHook(ctx, generationTaskName, "afterResponse", func(handler HookHandler) error {
		_, err := handler(ctx, mustJSON(message), runtime)
		return err
	}); err != nil {
		return err
	}
	calls := toolCallsOf(message)
	if message.StopReason == types.StopReasonToolUse && len(calls) > 0 {
		return startToolRound(ctx, runtime, checkpoint, message, calls, previous)
	}
	if message.StopReason == types.StopReasonStop || message.StopReason == types.StopReasonLength || message.StopReason == types.StopReasonToolUse {
		return answerRun(ctx, runtime, message)
	}
	settings := runtime.settings()
	overflow := message.StopReason == types.StopReasonError && utils.IsContextOverflow(message, nil)
	if overflow && checkpoint.Compacted == nil && settings.Compaction.Enabled {
		view, err := runtime.contextView(ctx, runtime.ConversationID(), checkpoint.Cutoff)
		if err == nil && selectCut(view, settings.Compaction.KeepRecentTokens) >= 0 {
			text := ""
			if message.ErrorMessage != nil {
				text = *message.ErrorMessage
			} else {
				text = "Context overflow"
			}
			return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
				live, err := loadLive(ctx, tx, runtime.ConversationID())
				if err != nil {
					return err
				}
				if _, err := appendAssistant(ctx, tx, runtime.ConversationID(), message); err != nil {
					return err
				}
				delete(live, "generation")
				child, err := createCompaction(ctx, tx, runtime.ConversationID(), mustJSON(map[string]any{"reason": "overflow"}), &current.ID)
				if err != nil {
					return err
				}
				updated := GenerationCheckpoint{Phase: "prepare", Attempt: checkpoint.Attempt, Compacted: &child, Overflow: text}
				current.State = durable.TaskState{Status: durable.TaskStatusWaiting, Checkpoint: mustJSON(updated), On: []durable.TaskID{child}, Policy: durable.PolicyAllSettled}
				return nil
			})
		}
	}
	policy := settings.Retry
	retry := message.StopReason == types.StopReasonError && !overflow && utils.IsRetryableAssistantError(message) && policy.Enabled && checkpoint.Attempt <= policy.MaxRetries
	until := int64(0)
	if retry {
		until = runtime.Now() + int64(utils.RetryDelayMs(toRetryPolicy(policy), checkpoint.Attempt))
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		if _, err := appendAssistant(ctx, tx, runtime.ConversationID(), message); err != nil {
			return err
		}
		if retry {
			errorText := ""
			if message.ErrorMessage != nil {
				errorText = *message.ErrorMessage
			}
			live["generation"] = map[string]any{"attempt": checkpoint.Attempt, "retry": map[string]any{"at": until, "error": errorText}}
			updated := GenerationCheckpoint{Phase: "retry", Attempt: checkpoint.Attempt, Compacted: checkpoint.Compacted, Until: until}
			current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(updated)}
			return nil
		}
		text := ""
		if message.ErrorMessage != nil {
			text = *message.ErrorMessage
		} else {
			text = fmt.Sprintf("Model response ended with stop reason %s", message.StopReason)
		}
		if err := endRun(ctx, tx, live, runtime.TaskID(), durable.SubmissionSettlement{Status: durable.SubmissionStatusUnanswered, Reason: "model_error"}); err != nil {
			return err
		}
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeFailed, Error: &durable.TaskOutcomeError{Message: text, Detail: mustJSON(map[string]any{"reason": "model_error"})}}}
		return nil
	})
}

func toRetryPolicy(policy RetryPolicy) utils.RetryPolicy {
	var max *float64
	if policy.MaxAgentDelayMs > 0 {
		value := float64(policy.MaxAgentDelayMs)
		max = &value
	}
	return utils.RetryPolicy{Enabled: policy.Enabled, MaxRetries: policy.MaxRetries, BaseDelayMs: float64(policy.BaseDelayMs), MaxAgentDelayMs: max}
}

func answerRun(ctx context.Context, runtime taskRuntime, message types.AssistantMessage) error {
	var continuation *types.UserContent
	if err := runtime.eachHook(ctx, generationTaskName, "onYield", func(handler HookHandler) error {
		if continuation != nil {
			return nil
		}
		out, err := handler(ctx, mustJSON(message), runtime)
		if err != nil {
			return err
		}
		var decision struct {
			Continue *types.UserContent `json:"continue"`
		}
		if err := json.Unmarshal(out, &decision); err == nil && decision.Continue != nil {
			continuation = decision.Continue
		}
		return nil
	}); err != nil {
		return err
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		b, err := prepareBoundary(ctx, tx, runtime.ConversationID(), runtime.settings().SteeringMode, runtime.settings().FollowUpMode)
		if err != nil {
			return err
		}
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		entry, err := appendAssistant(ctx, tx, runtime.ConversationID(), message)
		if err != nil {
			return err
		}
		result := durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: mustJSON(map[string]any{"entryId": int64(entry.ID)})}
		boundary, err := applyBoundary(ctx, tx, b, "final", runtime.Now())
		if err != nil {
			return err
		}
		if continuation != nil && len(boundary.users) == 0 && !boundary.reset {
			userMessage := userMessageFromContent(contentJSON(*continuation), runtime.Now())
			if _, err := tx.AppendEntry(ctx, runtime.ConversationID(), durable.EntryDraft{Kind: entryKindUser, Model: []types.Message{userMessage}}); err != nil {
				return err
			}
			successor, err := createGeneration(ctx, tx, runtime.ConversationID())
			if err != nil {
				return err
			}
			handOver(live, runtime.TaskID(), successor)
			delete(live, "generation")
			current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &result}
			return nil
		}
		if err := endRun(ctx, tx, live, runtime.TaskID(), durable.SubmissionSettlement{Status: durable.SubmissionStatusDone, Answer: &entry.ID}); err != nil {
			return err
		}
		if len(boundary.users) > 0 {
			if err := startRun(ctx, tx, runtime.ConversationID(), live, boundary.users); err != nil {
				return err
			}
		}
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &result}
		return nil
	})
}

func startToolRound(ctx context.Context, runtime taskRuntime, checkpoint GenerationCheckpoint, message types.AssistantMessage, calls []types.ToolCall, previous []types.Message) error {
	messages := previous
	if messages == nil {
		view, err := runtime.contextView(ctx, runtime.ConversationID(), checkpoint.Cutoff)
		if err != nil {
			return err
		}
		messages = view.Messages
	}
	offered := map[string]bool{}
	for _, tool := range utils.GetCurrentTools(messages) {
		offered[tool.Name] = true
	}
	agent, err := runtime.agent(ctx)
	if err != nil {
		return err
	}
	sequential := runtime.settings().ToolExecution == ToolModeSequential
	if !sequential {
		for _, call := range calls {
			if !offered[call.Name] {
				continue
			}
			for _, tool := range agent.Tools {
				if tool.Declaration.Name == call.Name && tool.ExecutionMode == ToolModeSequential {
					sequential = true
				}
			}
		}
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		entry, err := appendAssistant(ctx, tx, runtime.ConversationID(), message)
		if err != nil {
			return err
		}
		var slots []any
		var tools []durable.TaskID
		var pending []string
		for _, call := range calls {
			if !offered[call.Name] {
				unavailable := harnessError("tool_unavailable", fmt.Sprintf("Tool %s is not available", call.Name))
				result, err := appendToolResult(ctx, tx, runtime.ConversationID(), call, unavailable, runtime.Now())
				if err != nil {
					return err
				}
				slots = append(slots, map[string]any{"callId": call.Id, "name": call.Name, "status": "done", "entry": int64(result.ID)})
				continue
			}
			if sequential && len(tools) > 0 {
				pending = append(pending, call.Id)
				slots = append(slots, map[string]any{"callId": call.Id, "name": call.Name, "status": "pending"})
				continue
			}
			taskID, err := createToolTask(ctx, tx, runtime.TaskID(), entry.ID, call.Id)
			if err != nil {
				return err
			}
			tools = append(tools, taskID)
			slots = append(slots, map[string]any{"callId": call.Id, "name": call.Name, "taskId": int64(taskID), "status": "pending"})
		}
		delete(live, "generation")
		live["tools"] = slots
		assistant := entry.ID
		updated := GenerationCheckpoint{Phase: "tools", Assistant: &assistant, Tools: tools, Pending: pending}
		current.State = durable.TaskState{Status: durable.TaskStatusWaiting, Checkpoint: mustJSON(updated), On: tools, Policy: durable.PolicyAllSettled}
		return nil
	})
}

func finishToolRound(ctx context.Context, runtime taskRuntime, assistant durable.EntryID, tools []durable.TaskID) error {
	outcomes, err := runtime.Outcomes(ctx, tools)
	if err != nil {
		return err
	}
	controls := make([]*ToolControl, len(outcomes))
	for index, outcome := range outcomes {
		if outcome.Status == durable.OutcomeCompleted {
			controls[index] = controlFromResult(outcome.Result)
		}
	}
	var added []string
	handoff := ""
	var terminateControls []*ToolControl
	for _, control := range controls {
		if control == nil {
			continue
		}
		added = append(added, control.AddTools...)
		if control.Handoff != "" {
			handoff = control.Handoff
		}
		terminateControls = append(terminateControls, control)
	}
	liveState, err := runtime.snapshotDoc(ctx, *LiveDoc, runtime.ConversationID())
	if err != nil {
		return err
	}
	slots := liveTools(liveState)
	var results []durable.EntryID
	allTerminate := len(slots) > 0
	for _, slot := range slots {
		if entry, ok := asEntryID(slot["entry"]); ok {
			results = append(results, entry)
		} else {
			allTerminate = false
		}
		if _, ok := asTaskID(slot["taskId"]); !ok {
			allTerminate = false
		}
	}
	for _, control := range terminateControls {
		if !control.Terminate {
			allTerminate = false
		}
	}
	terminate := allTerminate
	if err := runtime.eachHook(ctx, generationTaskName, "afterTools", func(handler HookHandler) error {
		payload := map[string]any{"assistant": int64(assistant), "results": results}
		_, err := handler(ctx, mustJSON(payload), runtime)
		return err
	}); err != nil {
		return err
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		b, err := prepareBoundary(ctx, tx, runtime.ConversationID(), runtime.settings().SteeringMode, runtime.settings().FollowUpMode)
		if err != nil {
			return err
		}
		if len(added) > 0 {
			if err := addTools(ctx, tx, runtime.ConversationID(), added); err != nil {
				return err
			}
		}
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		now := runtime.Now()
		result := durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: mustJSON(map[string]any{"entryId": int64(assistant)})}
		if terminate || handoff != "" {
			if handoff != "" {
				handoffDraft := durable.EntryDraft{Kind: entryKindReset, HeadSelf: true, Model: []types.Message{types.NewUserMessageVariant(types.NewUserMessage(handoff, float64(now)))}}
				entry, err := tx.AppendEntry(ctx, runtime.ConversationID(), handoffDraft)
				if err != nil {
					return err
				}
				value := entry.ID
				b.head = &value
			}
			boundary, err := applyBoundary(ctx, tx, b, "final", now)
			if err != nil {
				return err
			}
			if err := endRun(ctx, tx, live, runtime.TaskID(), durable.SubmissionSettlement{Status: durable.SubmissionStatusDone, Answer: &assistant}); err != nil {
				return err
			}
			if len(boundary.users) > 0 {
				if err := startRun(ctx, tx, runtime.ConversationID(), live, boundary.users); err != nil {
					return err
				}
			}
		} else {
			boundary, err := applyBoundary(ctx, tx, b, "postTools", now)
			if err != nil {
				return err
			}
			if boundary.reset {
				if err := endRun(ctx, tx, live, runtime.TaskID(), durable.SubmissionSettlement{Status: durable.SubmissionStatusUnanswered, Reason: "reset"}); err != nil {
					return err
				}
				if len(boundary.users) > 0 {
					if err := startRun(ctx, tx, runtime.ConversationID(), live, boundary.users); err != nil {
						return err
					}
				}
			} else {
				delete(live, "tools")
				if run := liveRun(live); run != nil && idEqual(run["taskId"], int64(runtime.TaskID())) {
					inputs, _ := run["inputs"].([]any)
					for _, user := range boundary.users {
						inputs = append(inputs, int64(user))
					}
					run["inputs"] = inputs
				}
				successor, err := createGeneration(ctx, tx, runtime.ConversationID())
				if err != nil {
					return err
				}
				handOver(live, runtime.TaskID(), successor)
			}
		}
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &result}
		return nil
	})
}

func toolCallsOf(message types.AssistantMessage) []types.ToolCall {
	var calls []types.ToolCall
	for _, block := range message.Content {
		if block.ToolCall != nil {
			calls = append(calls, *block.ToolCall)
		}
	}
	return calls
}

// appendAssistant appends a provider result and records its usage.
func appendAssistant(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, message types.AssistantMessage) (*durable.EntryRecord, error) {
	key := fmt.Sprintf("%s/%s", message.Provider, message.Model)
	if err := recordUsage(ctx, tx, conversationID, "models", key, message.Usage); err != nil {
		return nil, err
	}
	return tx.AppendEntry(ctx, conversationID, durable.EntryDraft{Kind: entryKindAssistant, Model: []types.Message{{Role: types.AssistantMessageRole, Assistant: &message}}})
}

// startRun starts a generation for the given placed inputs.
func startRun(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, live map[string]any, inputs []durable.SubmissionID) error {
	taskID, err := createGeneration(ctx, tx, conversationID)
	if err != nil {
		return err
	}
	setRun(live, taskID, inputs)
	return nil
}

func createGeneration(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID) (durable.TaskID, error) {
	return tx.CreateTask(ctx, GenerationTask(), mustJSON(map[string]any{}), durable.TaskOptions{
		Ownership:      durable.TaskOwnership{Kind: "conversation"},
		ConversationID: conversationID,
	})
}

func createToolTask(ctx context.Context, tx durable.Tx, owner durable.TaskID, assistant durable.EntryID, callID string) (durable.TaskID, error) {
	input := mustJSON(map[string]any{"assistant": int64(assistant), "callId": callID})
	return tx.CreateTask(ctx, ToolTask(), input, durable.TaskOptions{Ownership: durable.TaskOwnership{Kind: "task", TaskID: owner}})
}

func handOver(live map[string]any, from, to durable.TaskID) {
	if run := liveRun(live); run != nil && idEqual(run["taskId"], from) {
		run["taskId"] = int64(to)
	}
}

// convertPartial appends a committed partial as an aborted assistant entry.
func convertPartial(ctx context.Context, tx durable.Tx, live map[string]any, conversationID durable.ConversationID) error {
	generation := liveGeneration(live)
	if generation == nil {
		return nil
	}
	raw, ok := generation["message"]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var message types.AssistantMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return err
	}
	message.StopReason = types.StopReasonAborted
	_, err = appendAssistant(ctx, tx, conversationID, message)
	return err
}

func generationFailNoModel(ctx context.Context, runtime taskRuntime, ref *ModelRef) error {
	message := "No model is configured"
	if ref != nil {
		message = fmt.Sprintf("Model %s/%s is not available", ref.Provider, ref.ModelID)
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		if err := endRun(ctx, tx, live, runtime.TaskID(), durable.SubmissionSettlement{Status: durable.SubmissionStatusUnanswered, Reason: "no_model"}); err != nil {
			return err
		}
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeFailed, Error: &durable.TaskOutcomeError{Message: message, Detail: mustJSON(map[string]any{"reason": "no_model"})}}}
		return nil
	})
}

func generationFailModelError(ctx context.Context, runtime taskRuntime, text string) error {
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		if err := endRun(ctx, tx, live, runtime.TaskID(), durable.SubmissionSettlement{Status: durable.SubmissionStatusUnanswered, Reason: "model_error"}); err != nil {
			return err
		}
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeFailed, Error: &durable.TaskOutcomeError{Message: text, Detail: mustJSON(map[string]any{"reason": "model_error"})}}}
		return nil
	})
}

func declarations(tools []ToolRegistration) []types.Tool {
	out := make([]types.Tool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Declaration)
	}
	return out
}

func shownToMap(shown *sectionValues) map[string]string {
	out := map[string]string{}
	for _, key := range shown.keys {
		if value, ok := shown.get(key); ok {
			out[key] = value
		}
	}
	return out
}

func entryIDPresent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value struct {
		EntryID *int64 `json:"entryId"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	return value.EntryID != nil
}

func controlFromResult(raw json.RawMessage) *ToolControl {
	if len(raw) == 0 {
		return nil
	}
	var value struct {
		Control *ToolControl `json:"control"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value.Control
}

func derefEntry(value *durable.EntryID) durable.EntryID {
	if value == nil {
		return 0
	}
	return *value
}

func derefHandle(value *types.DeferredHandle) types.DeferredHandle {
	if value == nil {
		return types.DeferredHandle{}
	}
	return *value
}

var _ = errors.New
