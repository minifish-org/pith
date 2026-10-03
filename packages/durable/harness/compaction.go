package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
	"github.com/minifish-org/pith/packages/durable"
)

// CompactionInput is the input of a compaction task.
type CompactionInput struct {
	Reason       string `json:"reason"`
	Instructions string `json:"instructions,omitempty"`
}

// CompactionCheckpoint is the durable checkpoint of a compaction task.
type CompactionCheckpoint struct {
	Phase         string                    `json:"phase"`
	Attempt       int                       `json:"attempt,omitempty"`
	Model         *ModelRef                 `json:"model,omitempty"`
	ThinkingLevel string                    `json:"thinkingLevel,omitempty"`
	StreamOptions ConversationStreamOptions `json:"streamOptions,omitempty"`
	MaxTokens     int                       `json:"maxTokens,omitempty"`
	Tail          *durable.EntryID          `json:"tail,omitempty"`
	FirstKept     *durable.EntryID          `json:"firstKept,omitempty"`
	Until         int64                     `json:"until,omitempty"`
}

const toolResultMaxChars = 2000

const summaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
const summarySuffix = "\n</summary>"

const summarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work. If the conversation starts with an earlier summary, preserve its information and fold the newer messages into it.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// CompactionTask returns the built-in compaction task definition.
func CompactionTask() durable.TaskDefinition {
	return durable.TaskDefinition{
		Name:    compactionTaskName,
		Version: 1,
		Initial: func(json.RawMessage) (json.RawMessage, error) {
			return mustJSON(CompactionCheckpoint{Phase: "select"}), nil
		},
		Phases: map[string]durable.PhaseHandler{
			"select":    compactionSelect,
			"summarize": compactionSummarize,
			"retry":     compactionRetry,
		},
		Abort: compactionAbort,
	}
}

func parseCompactionInput(raw []byte) CompactionInput {
	var input CompactionInput
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &input)
	}
	return input
}

func parseCompactionCheckpoint(raw []byte) CompactionCheckpoint {
	var checkpoint CompactionCheckpoint
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &checkpoint)
	}
	return checkpoint
}

func compactionSelect(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	agent, err := runtime.agent(ctx)
	if err != nil {
		return err
	}
	settings := runtime.settings()
	ref := agent.Model
	if ref == nil {
		return compactionFailNoModel(ctx, runtime, nil)
	}
	model, err := runtime.modelRunner().Resolve(ctx, *ref)
	if err != nil || model == nil {
		return compactionFailNoModel(ctx, runtime, ref)
	}
	view, err := runtime.contextView(ctx, runtime.ConversationID(), nil)
	if err != nil {
		return err
	}
	cut := selectCut(view, settings.Compaction.KeepRecentTokens)
	if cut < 0 {
		return compactionComplete(ctx, runtime)
	}
	firstKept := view.Entries[cut].ID
	input := parseCompactionInput(task.Input)
	entries := view.Entries[:cut]
	messages := summarizedMessages(view, cut)
	var decision *struct {
		Decline bool   `json:"decline"`
		Summary string `json:"summary"`
	}
	if err := runtime.eachHook(ctx, compactionTaskName, "beforeCompact", func(handler HookHandler) error {
		if decision != nil {
			return nil
		}
		payload := map[string]any{"reason": input.Reason, "entries": entries, "messages": messages, "firstKept": int64(firstKept)}
		if input.Instructions != "" {
			payload["instructions"] = input.Instructions
		}
		out, err := handler(ctx, mustJSON(payload), runtime)
		if err != nil {
			return err
		}
		var parsed struct {
			Decline bool   `json:"decline"`
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			return nil
		}
		decision = &parsed
		return nil
	}); err != nil {
		return err
	}
	if decision != nil && decision.Decline {
		return compactionComplete(ctx, runtime)
	}
	if decision != nil && decision.Summary != "" {
		return placeSummary(ctx, runtime, task, firstKept, decision.Summary)
	}
	tail := firstKept
	for _, entry := range view.Entries {
		if entry.ID > tail {
			tail = entry.ID
		}
	}
	maxTokens := int(0.8 * float64(settings.Compaction.ReserveTokens))
	if model.MaxTokens > 0 && int(model.MaxTokens) < maxTokens {
		maxTokens = int(model.MaxTokens)
	}
	request := CompactionCheckpoint{
		Phase:         "summarize",
		Attempt:       1,
		Model:         ref,
		ThinkingLevel: string(agent.ThinkingLevel),
		StreamOptions: settings.Stream,
		MaxTokens:     maxTokens,
		Tail:          &tail,
		FirstKept:     &firstKept,
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(request)}
		return nil
	})
}

func compactionSummarize(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	checkpoint := parseCompactionCheckpoint(task.State.Checkpoint)
	if checkpoint.Model == nil {
		return compactionFailNoModel(ctx, runtime, nil)
	}
	if _, err := runtime.modelRunner().Resolve(ctx, *checkpoint.Model); err != nil {
		return compactionFailNoModel(ctx, runtime, checkpoint.Model)
	}
	view, err := runtime.contextView(ctx, runtime.ConversationID(), checkpoint.Tail)
	if err != nil {
		return err
	}
	cut := 0
	for i, entry := range view.Entries {
		if entry.ID == derefEntry(checkpoint.FirstKept) {
			cut = i
			break
		}
	}
	now := runtime.Now()
	instruction := parseCompactionInput(task.Input).Instructions
	messages := []types.Message{
		types.NewSystemMessageVariant(types.NewSystemMessage(summarizationSystemPrompt, float64(now))),
		{User: &types.UserMessage{Role: types.UserMessageRole, Content: types.UserContentBlocks([]types.ContentBlock{types.TextBlock(summaryPrompt(summarizedMessages(view, cut), instruction))}), Timestamp: float64(now)}},
	}
	request := ModelRequest{
		ConversationID: runtime.ConversationID(),
		Model:          *checkpoint.Model,
		Messages:       messages,
		ThinkingLevel:  types.ThinkingLevel(checkpoint.ThinkingLevel),
		Options:        checkpoint.StreamOptions,
		Summary:        true,
		Instructions:   instruction,
	}
	message, err := runtime.modelRunner().Run(ctx, request, func(types.AssistantMessage) error { return nil })
	if err != nil {
		return err
	}
	summary := summaryText(message)
	policy := runtime.settings().Retry
	retry := message.StopReason == types.StopReasonError && utils.IsRetryableAssistantError(message) && policy.Enabled && checkpoint.Attempt <= policy.MaxRetries
	until := int64(0)
	if retry {
		until = runtime.Now() + int64(utils.RetryDelayMs(toRetryPolicy(policy), checkpoint.Attempt))
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		key := fmt.Sprintf("%s/%s", message.Provider, message.Model)
		if err := recordUsage(ctx, tx, runtime.ConversationID(), "models", key, message.Usage); err != nil {
			return err
		}
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		if summary != "" {
			return placeSummaryInCommit(ctx, runtime, task, tx, current, live, derefEntry(checkpoint.FirstKept), summary)
		}
		if retry {
			if status := compactionStatus(live, runtime.TaskID()); status != nil {
				status["retry"] = map[string]any{"at": until, "error": errorString(message.ErrorMessage)}
			}
			updated := checkpoint
			updated.Phase = "retry"
			updated.Until = until
			current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(updated)}
			return nil
		}
		removeCompactionStatus(live, runtime.TaskID())
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeFailed, Error: &durable.TaskOutcomeError{Message: summaryFailure(message), Detail: mustJSON(map[string]any{"reason": "model_error"})}}}
		return nil
	})
}

func compactionRetry(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	checkpoint := parseCompactionCheckpoint(task.State.Checkpoint)
	if err := runtime.Sleep(ctx, checkpoint.Until); err != nil {
		return err
	}
	checkpoint.Phase = "summarize"
	checkpoint.Attempt++
	checkpoint.Until = 0
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		if status := compactionStatus(live, runtime.TaskID()); status != nil {
			status["attempt"] = checkpoint.Attempt
			delete(status, "retry")
		}
		current.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: mustJSON(checkpoint)}
		return nil
	})
}

func compactionAbort(ctx context.Context, task durable.TaskRecord, r durable.TaskRuntime) error {
	runtime := r.(taskRuntime)
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		removeCompactionStatus(live, runtime.TaskID())
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeAborted}}
		return nil
	})
}

// createCompaction creates a compaction task with its live status.
func createCompaction(ctx context.Context, tx durable.Tx, conversationID durable.ConversationID, input json.RawMessage, owner *durable.TaskID) (durable.TaskID, error) {
	parsed := parseCompactionInput(input)
	ownership := durable.TaskOwnership{Kind: "conversation"}
	background := false
	if owner != nil {
		ownership = durable.TaskOwnership{Kind: "task", TaskID: *owner}
	} else if parsed.Reason != "manual" {
		background = true
	}
	taskID, err := tx.CreateTask(ctx, CompactionTask(), input, durable.TaskOptions{
		Ownership:      ownership,
		ConversationID: conversationID,
		Background:     background,
	})
	if err != nil {
		return 0, err
	}
	live, err := loadLive(ctx, tx, conversationID)
	if err != nil {
		return 0, err
	}
	addCompactionStatus(live, map[string]any{"taskId": int64(taskID), "reason": parsed.Reason, "blocking": owner != nil, "attempt": 1})
	return taskID, nil
}

func placeSummary(ctx context.Context, runtime taskRuntime, task durable.TaskRecord, firstKept durable.EntryID, summary string) error {
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		return placeSummaryInCommit(ctx, runtime, task, tx, current, live, firstKept, summary)
	})
}

func placeSummaryInCommit(ctx context.Context, runtime taskRuntime, task durable.TaskRecord, tx durable.Tx, current *durable.TaskRecord, live map[string]any, firstKept durable.EntryID, summary string) error {
	removeCompactionStatus(live, runtime.TaskID())
	text := summaryPrefix + summary + summarySuffix
	entry := durable.EntryDraft{
		Kind:  entryKindCompaction,
		Head:  &firstKept,
		Model: []types.Message{types.NewUserMessageVariant(types.NewUserMessageBlocks([]types.ContentBlock{types.TextBlock(text)}, float64(runtime.Now())))},
		Data:  mustJSON(map[string]any{"reason": parseCompactionInput(task.Input).Reason}),
	}
	owner := task.Owner
	if owner == nil {
		requestID := fmt.Sprintf("compaction:%d", runtime.TaskID())
		submissionID, err := admitSubmission(ctx, tx, runtime.ConversationID(), SubmissionDraft{Type: durable.SubmissionTypeWrite, Entry: &entry, RequestID: requestID}, runtime.Now(), runtime.settings())
		if err != nil {
			return err
		}
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: mustJSON(map[string]any{"submissionId": int64(submissionID)})}}
		return nil
	}
	appended, err := tx.AppendEntry(ctx, runtime.ConversationID(), entry)
	if err != nil {
		return err
	}
	current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: mustJSON(map[string]any{"entryId": int64(appended.ID)})}}
	return nil
}

func compactionComplete(ctx context.Context, runtime taskRuntime) error {
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		removeCompactionStatus(live, runtime.TaskID())
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: mustJSON(map[string]any{})}}
		return nil
	})
}

func compactionFailNoModel(ctx context.Context, runtime taskRuntime, ref *ModelRef) error {
	message := "No model is configured"
	if ref != nil {
		message = fmt.Sprintf("Model %s/%s is not available", ref.Provider, ref.ModelID)
	}
	return runtime.Commit(ctx, func(tx durable.Tx, current *durable.TaskRecord) error {
		live, err := loadLive(ctx, tx, runtime.ConversationID())
		if err != nil {
			return err
		}
		removeCompactionStatus(live, runtime.TaskID())
		current.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeFailed, Error: &durable.TaskOutcomeError{Message: message, Detail: mustJSON(map[string]any{"reason": "no_model"})}}}
		return nil
	})
}

func errorString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// selectCut returns the index of the first entry a summary keeps, or -1.
func selectCut(view ContextView, keepRecentTokens int) int {
	contributions := view.Contributions
	start := 0
	if view.Head != nil {
		start = 1
	}
	var candidates []int
	for index := start; index < len(contributions); index++ {
		if isCandidate(contributions, index) {
			candidates = append(candidates, index)
		}
	}
	kept := 0.0
	cut := -1
	for index := len(contributions) - 1; index >= start; index-- {
		for _, message := range contributions[index] {
			kept += utils.EstimateMessageTokens(message)
		}
		if kept < float64(keepRecentTokens) {
			continue
		}
		cut = -1
		for _, candidate := range candidates {
			if candidate >= index {
				cut = candidate
				break
			}
		}
		if cut == -1 && len(candidates) > 0 {
			cut = candidates[len(candidates)-1]
		}
		break
	}
	if cut == -1 {
		return -1
	}
	if cut > start {
		for index := start; index < cut; index++ {
			if len(contributions[index]) > 0 {
				return cut
			}
		}
		return -1
	}
	// cut == start: there is no entry before it to summarize, but keeping the
	// newer entries and heading them with a summary is still useful when more
	// than one entry exists.
	if len(contributions) > start+1 {
		return cut
	}
	return -1
}

func isCandidate(contributions [][]types.Message, index int) bool {
	first := firstMessage(contributions[index])
	if first == nil {
		return false
	}
	if first.Assistant != nil {
		return true
	}
	if first.User == nil {
		return false
	}
	calls := map[string]bool{}
	for before := index - 1; before >= 0; before-- {
		assistant := lastAssistant(contributions[before])
		if assistant == nil {
			continue
		}
		for _, block := range assistant.Content {
			if block.ToolCall != nil {
				calls[block.ToolCall.Id] = true
			}
		}
		break
	}
	if len(calls) == 0 {
		return true
	}
	for after := index; after < len(contributions); after++ {
		for position, message := range contributions[after] {
			if message.Assistant != nil && (after > index || position > 0) {
				return true
			}
			if message.ToolResult != nil && calls[message.ToolResult.ToolCallId] {
				return false
			}
		}
	}
	return true
}

func firstMessage(messages []types.Message) *types.Message {
	if len(messages) == 0 {
		return nil
	}
	return &messages[0]
}

func lastAssistant(messages []types.Message) *types.AssistantMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Assistant != nil {
			return messages[i].Assistant
		}
	}
	return nil
}

// summarizeMessages mirrors summarizedMessages.
func summarizedMessages(view ContextView, cut int) []types.Message {
	if cut > len(view.Contributions) {
		cut = len(view.Contributions)
	}
	var flat []types.Message
	for _, list := range view.Contributions[:cut] {
		flat = append(flat, list...)
	}
	return orderToolResults(flat)
}

// estimateContext estimates the token size of a request over view plus extra.
func estimateContext(view ContextView, extra []types.Message) float64 {
	var measured *types.AssistantMessage
	after := -1
	if view.Head != nil {
		after = int(view.Head.ID)
	}
	for index := len(view.Entries) - 1; index >= 0 && measured == nil; index-- {
		if int(view.Entries[index].ID) <= after {
			continue
		}
		for _, message := range view.Contributions[index] {
			if message.Assistant != nil && utils.CalculateContextTokens(message.Assistant.Usage) > 0 {
				measured = message.Assistant
			}
		}
	}
	from := 0
	if measured != nil {
		from = lastIndexMessage(view.Messages, measured) + 1
	}
	tokens := 0.0
	if measured != nil {
		tokens = utils.CalculateContextTokens(measured.Usage)
	}
	for _, message := range view.Messages[from:] {
		tokens += utils.EstimateMessageTokens(message)
	}
	for _, message := range extra {
		tokens += utils.EstimateMessageTokens(message)
	}
	return tokens
}

func lastIndexMessage(messages []types.Message, assistant *types.AssistantMessage) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Assistant == assistant {
			return i
		}
	}
	return -1
}

func thresholdCompaction(view ContextView, planned []types.Message, contextWindow float64, policy CompactionPolicy, allowed bool) string {
	if !allowed || !policy.Enabled || contextWindow <= 0 {
		return ""
	}
	tokens := estimateContext(view, planned)
	blocking := contextWindow - float64(policy.ReserveTokens)
	background := blocking - float64(policy.BackgroundTokens)
	over := ""
	if tokens > blocking {
		over = "blocking"
	} else if policy.BackgroundTokens > 0 && tokens > background {
		over = "background"
	}
	if over == "" || selectCut(view, policy.KeepRecentTokens) < 0 {
		return ""
	}
	return over
}

func summaryText(message types.AssistantMessage) string {
	if message.StopReason != types.StopReasonStop {
		return ""
	}
	var parts []string
	for _, block := range message.Content {
		if block.ToolCall != nil {
			return ""
		}
		if block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func summaryFailure(message types.AssistantMessage) string {
	if message.StopReason == types.StopReasonError || message.StopReason == types.StopReasonAborted {
		if message.ErrorMessage != nil {
			return fmt.Sprintf("Summarization failed: %s", *message.ErrorMessage)
		}
		return fmt.Sprintf("Summarization failed: %s", message.StopReason)
	}
	if message.StopReason == types.StopReasonLength {
		return "Summarization hit the token limit; the summary is incomplete"
	}
	for _, block := range message.Content {
		if block.ToolCall != nil {
			return "Summarization attempted to call a tool"
		}
	}
	return "Summarization produced no text"
}

func summaryPrompt(messages []types.Message, instructions string) string {
	focus := ""
	if instructions != "" {
		focus = "\n\nAdditional focus: " + instructions
	}
	return "<conversation>\n" + serializeConversation(messages) + "\n</conversation>\n\n" + summarizationPrompt + focus
}

func serializeConversation(messages []types.Message) string {
	var parts []string
	for _, message := range messages {
		switch {
		case message.User != nil:
			text := contentText(message.User.Content)
			if text != "" {
				parts = append(parts, "[User]: "+text)
			}
		case message.Assistant != nil:
			var thinking, text, calls []string
			for _, block := range message.Assistant.Content {
				if block.Thinking != nil {
					thinking = append(thinking, block.Thinking.Thinking)
				}
				if block.Text != nil {
					text = append(text, block.Text.Text)
				}
				if block.ToolCall != nil {
					calls = append(calls, renderToolCall(*block.ToolCall))
				}
			}
			if len(thinking) > 0 {
				parts = append(parts, "[Assistant thinking]: "+strings.Join(thinking, "\n"))
			}
			if len(text) > 0 {
				parts = append(parts, "[Assistant]: "+strings.Join(text, "\n"))
			}
			if len(calls) > 0 {
				parts = append(parts, "[Assistant tool calls]: "+strings.Join(calls, "; "))
			}
		case message.ToolResult != nil:
			text := contentText(types.UserContentBlocks(message.ToolResult.Content))
			if text != "" {
				parts = append(parts, "[Tool result]: "+truncateText(text, toolResultMaxChars))
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func renderToolCall(call types.ToolCall) string {
	var args map[string]any
	if len(call.Arguments) > 0 {
		_ = json.Unmarshal(call.Arguments, &args)
	}
	names := make([]string, 0, len(args))
	for key := range args {
		names = append(names, key)
	}
	sortStrings(names)
	parts := make([]string, 0, len(names))
	for _, key := range names {
		parts = append(parts, fmt.Sprintf("%s=%s", key, mustJSONString(args[key])))
	}
	return fmt.Sprintf("%s(%s)", call.Name, strings.Join(parts, ", "))
}

func mustJSONString(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(data)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j-1] > values[j]; j-- {
			values[j-1], values[j] = values[j], values[j-1]
		}
	}
}

func contentText(content types.UserContent) string {
	if !content.Structured {
		return content.Text
	}
	var parts []string
	for _, block := range content.Blocks {
		if block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func truncateText(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars] + fmt.Sprintf("\n\n[... %d more characters truncated]", len(text)-maxChars)
}
