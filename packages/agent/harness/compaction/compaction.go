// This file carries compaction.ts: token estimation, cut-point selection,
// summary request boundaries and prepare/generate/commit.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package compaction

import (
	"encoding/json"
	"math"
	"strconv"
	"time"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	harnessmessages "github.com/minifish-org/pith/packages/agent/harness/messages"
	harnesssession "github.com/minifish-org/pith/packages/agent/harness/session"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	usageutils "github.com/minifish-org/pith/packages/agent/harness/utils/usage"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	ai "github.com/minifish-org/pith/packages/ai"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// CompactionDetails are the file-operation details stored on generated
// compaction entries.
type CompactionDetails struct {
	// ReadFiles are files read in the compacted history.
	ReadFiles []string `json:"readFiles"`
	// ModifiedFiles are files modified in the compacted history.
	ModifiedFiles []string `json:"modifiedFiles"`
}

// CompactResult is generated compaction data ready to be persisted as a
// compaction entry.
type CompactResult struct {
	// Summary replaces compacted history in future context.
	Summary string `json:"summary"`
	// TokensBefore is the estimated context tokens before compaction.
	TokensBefore float64 `json:"tokensBefore"`
	// Usage is the usage from the LLM call(s) that generated this summary.
	Usage *aitypes.Usage `json:"usage,omitempty"`
	// RetainedTail are the recent messages retained after compaction.
	RetainedTail []agenttypes.AgentMessage `json:"retainedTail"`
	// Details are the optional implementation-specific compaction details.
	Details *CompactionDetails `json:"details,omitempty"`
}

// SummaryRequest is the caller-owned one-request boundary used to generate one
// summary. It returns the raw assistant message; terminal provider failures are
// encoded in that message. A returned error aborts the caller.
type SummaryRequest func(
	aiContext aitypes.Context,
	options aitypes.SimpleStreamOptions,
	context harnesstypes.Context,
) (aitypes.AssistantMessage, error)

// CreateSummaryRequestOptions derives the isolated summary request options
// from the invocation context.
func CreateSummaryRequestOptions(options aitypes.SimpleStreamOptions, context harnesstypes.Context) aitypes.SimpleStreamOptions {
	out := options
	if context != nil {
		out.Signal = context.Done()
	}
	out.TelemetryContext = harnesscontext.GetTelemetryContext(context)
	none := aitypes.CacheRetentionNone
	out.CacheRetention = &none
	if out.SessionId == nil {
		if generated, err := aiutils.UUIDv7(nil); err == nil {
			out.SessionId = &generated
		}
	}
	return out
}

// CompleteSimpleWithRetries performs one assistant completion through the
// models collection with bounded retry. Summaries are standalone requests, so
// routing is isolated and cache writes that cannot be reused are disabled.
func CompleteSimpleWithRetries(
	models ai.Models,
	model aitypes.Model,
	aiContext aitypes.Context,
	options aitypes.SimpleStreamOptions,
	retry *aiutils.RetryPolicy,
	callbacks *aiutils.RetryCallbacks,
	context harnesstypes.Context,
) (aitypes.AssistantMessage, error) {
	requestOptions := CreateSummaryRequestOptions(options, context)
	return aiutils.RetryAssistantCall(func() (aitypes.AssistantMessage, error) {
		response, err := models.CompleteSimple(
			context,
			model,
			aiContext,
			&ai.ModelsSimpleStreamOptions{SimpleStreamOptions: requestOptions},
		)
		if err != nil {
			return aitypes.AssistantMessage{}, err
		}
		if response == nil {
			return aitypes.AssistantMessage{}, nil
		}
		return *response, nil
	}, retry, context, callbacks)
}

// CompactionSettings are the compaction thresholds and retention settings. It
// is the shared harness type, re-exported here so the compaction package is the
// mapping target for the upstream declaration.
type CompactionSettings = harnesstypes.CompactionSettings

// DefaultCompactionSettings are the defaults used by the harness.
var DefaultCompactionSettings = harnesstypes.DefaultCompactionSettings

// CalculateContextTokens calculates total context tokens from provider usage.
func CalculateContextTokens(usage aitypes.Usage) float64 {
	if usage.TotalTokens != 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

func assistantUsage(message agenttypes.AgentMessage) *aitypes.Usage {
	if message.Message == nil || message.Message.Assistant == nil {
		return nil
	}
	assistant := message.Message.Assistant
	if assistant.StopReason == aitypes.StopReasonAborted || assistant.StopReason == aitypes.StopReasonError {
		return nil
	}
	if CalculateContextTokens(assistant.Usage) <= 0 {
		return nil
	}
	usage := assistant.Usage
	return &usage
}

// GetLastAssistantUsage returns usage from the last valid assistant message in
// session entries.
func GetLastAssistantUsage(entries []harnesstypes.Entry) *aitypes.Usage {
	for index := len(entries) - 1; index >= 0; index-- {
		message, ok := messageFromMessageEntry(entries[index])
		if !ok {
			continue
		}
		if usage := assistantUsage(message); usage != nil {
			return usage
		}
	}
	return nil
}

// ContextUsageEstimate is the estimated context-token usage for a message list.
type ContextUsageEstimate struct {
	// Tokens is the estimated total context tokens.
	Tokens float64 `json:"tokens"`
	// UsageTokens are the tokens reported by the most recent usage block.
	UsageTokens float64 `json:"usageTokens"`
	// TrailingTokens are the estimated tokens after that block.
	TrailingTokens float64 `json:"trailingTokens"`
	// LastUsageIndex is the message index that provided usage, or null.
	LastUsageIndex *int `json:"lastUsageIndex"`
}

type assistantUsageInfo struct {
	usage aitypes.Usage
	index int
}

func lastAssistantUsageInfo(messages []agenttypes.AgentMessage) *assistantUsageInfo {
	for index := len(messages) - 1; index >= 0; index-- {
		if usage := assistantUsage(messages[index]); usage != nil {
			return &assistantUsageInfo{usage: *usage, index: index}
		}
	}
	return nil
}

// EstimateContextTokens estimates context tokens for messages using provider
// usage when available.
func EstimateContextTokens(messages []agenttypes.AgentMessage) ContextUsageEstimate {
	info := lastAssistantUsageInfo(messages)
	if info == nil {
		estimated := 0
		for _, message := range messages {
			estimated += EstimateTokens(message)
		}
		return ContextUsageEstimate{
			Tokens:         float64(estimated),
			UsageTokens:    0,
			TrailingTokens: float64(estimated),
			LastUsageIndex: nil,
		}
	}
	usageTokens := CalculateContextTokens(info.usage)
	trailing := 0
	for index := info.index + 1; index < len(messages); index++ {
		trailing += EstimateTokens(messages[index])
	}
	lastIndex := info.index
	return ContextUsageEstimate{
		Tokens:         usageTokens + float64(trailing),
		UsageTokens:    usageTokens,
		TrailingTokens: float64(trailing),
		LastUsageIndex: &lastIndex,
	}
}

// ShouldCompact reports whether context usage exceeds the configured
// compaction threshold.
func ShouldCompact(contextTokens float64, contextWindow float64, settings CompactionSettings) bool {
	if !settings.Enabled {
		return false
	}
	return contextTokens > contextWindow-float64(settings.ReserveTokens)
}

// EstimatedImageChars is the conservative character estimate for one image.
const EstimatedImageChars = 4800

func estimateUserContentChars(content aitypes.UserContent) int {
	if !content.Structured {
		return utf16Len(content.Text)
	}
	return estimateBlocksChars(content.Blocks)
}

func estimateBlocksChars(blocks []aitypes.ContentBlock) int {
	chars := 0
	for _, block := range blocks {
		switch block.Type {
		case aitypes.ContentTypeText:
			if block.Text != nil && block.Text.Text != "" {
				chars += utf16Len(block.Text.Text)
			}
		case aitypes.ContentTypeImage:
			chars += EstimatedImageChars
		}
	}
	return chars
}

func estimateCustomContentChars(message agenttypes.AgentMessage) int {
	var parsed harnessmessages.CustomMessage
	if !decodeCustomMessage(message, &parsed) {
		return 0
	}
	return estimateUserContentChars(parsed.Content)
}

func decodeCustomMessage(message agenttypes.AgentMessage, target any) bool {
	if message.Custom == nil || len(message.Custom.Raw) == 0 {
		return false
	}
	return json.Unmarshal(message.Custom.Raw, target) == nil
}

func agentMessageRole(message agenttypes.AgentMessage) string {
	if message.Message != nil {
		return message.Message.Role
	}
	if message.Custom != nil {
		return message.Custom.Role
	}
	return ""
}

func ceilDiv4(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + 3) / 4
}

// EstimateTokens estimates the token count for one message using the upstream
// conservative character heuristic.
func EstimateTokens(message agenttypes.AgentMessage) int {
	switch agentMessageRole(message) {
	case "user":
		content := aitypes.UserContent{}
		if message.Message != nil && message.Message.User != nil {
			content = message.Message.User.Content
		}
		return ceilDiv4(estimateUserContentChars(content))
	case "assistant":
		if message.Message == nil || message.Message.Assistant == nil {
			return 0
		}
		chars := 0
		for _, block := range message.Message.Assistant.Content {
			switch block.Type {
			case aitypes.ContentTypeText:
				if block.Text != nil {
					chars += utf16Len(block.Text.Text)
				}
			case aitypes.ContentTypeThinking:
				if block.Thinking != nil {
					chars += utf16Len(block.Thinking.Thinking)
				}
			case aitypes.ContentTypeToolCall:
				if block.ToolCall != nil {
					chars += utf16Len(block.ToolCall.Name) + utf16Len(safeJSONStringify(block.ToolCall.Arguments))
				}
			}
		}
		return ceilDiv4(chars)
	case "custom":
		return ceilDiv4(estimateCustomContentChars(message))
	case "toolResult":
		if message.Message == nil || message.Message.ToolResult == nil {
			return 0
		}
		return ceilDiv4(estimateBlocksChars(message.Message.ToolResult.Content))
	case "bashExecution":
		var parsed harnessmessages.BashExecutionMessage
		if !decodeCustomMessage(message, &parsed) {
			return 0
		}
		return ceilDiv4(utf16Len(parsed.Command) + utf16Len(parsed.Output))
	case "branchSummary":
		var parsed harnessmessages.BranchSummaryMessage
		if !decodeCustomMessage(message, &parsed) {
			return 0
		}
		return ceilDiv4(utf16Len(parsed.Summary))
	case "compactionSummary":
		var parsed harnessmessages.CompactionSummaryMessage
		if !decodeCustomMessage(message, &parsed) {
			return 0
		}
		return ceilDiv4(utf16Len(parsed.Summary))
	default:
		return 0
	}
}

func findValidCutPoints(entries []harnesstypes.Entry, startIndex int, endIndex int) []int {
	cutPoints := []int{}
	for index := startIndex; index < endIndex; index++ {
		entry := entries[index]
		switch asEntryKind(entry) {
		case harnesstypes.EntryTypeMessage:
			message, ok := messageFromMessageEntry(entry)
			if ok {
				switch agentMessageRole(message) {
				case "bashExecution", "custom", "branchSummary", "compactionSummary", "user", "assistant":
					cutPoints = append(cutPoints, index)
				}
			}
		}
		if asEntryKind(entry) == harnesstypes.EntryTypeBranchSummary {
			cutPoints = append(cutPoints, index)
		}
	}
	return cutPoints
}

// FindTurnStartIndex finds the user-visible message that starts the turn
// containing an entry.
func FindTurnStartIndex(entries []harnesstypes.Entry, entryIndex int, startIndex int) int {
	for index := entryIndex; index >= startIndex; index-- {
		entry := entries[index]
		if asEntryKind(entry) == harnesstypes.EntryTypeBranchSummary {
			return index
		}
		if asEntryKind(entry) == harnesstypes.EntryTypeMessage {
			if message, ok := messageFromMessageEntry(entry); ok {
				role := agentMessageRole(message)
				if role == "user" || role == "bashExecution" {
					return index
				}
			}
		}
	}
	return -1
}

// CutPointResult is the cut point selected for compaction.
type CutPointResult struct {
	// FirstKeptEntryIndex is the first entry retained after compaction.
	FirstKeptEntryIndex int `json:"firstKeptEntryIndex"`
	// TurnStartIndex is the turn start when the cut splits a turn, else -1.
	TurnStartIndex int `json:"turnStartIndex"`
	// IsSplitTurn reports whether the cut splits an in-progress turn.
	IsSplitTurn bool `json:"isSplitTurn"`
}

// FindCutPoint finds the compaction cut point that keeps approximately the
// requested recent-token budget.
func FindCutPoint(entries []harnesstypes.Entry, startIndex int, endIndex int, keepRecentTokens int) CutPointResult {
	cutPoints := findValidCutPoints(entries, startIndex, endIndex)
	if len(cutPoints) == 0 {
		return CutPointResult{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1, IsSplitTurn: false}
	}
	accumulatedTokens := 0
	cutIndex := cutPoints[0]
	for index := endIndex - 1; index >= startIndex; index-- {
		message, ok := messageFromMessageEntry(entries[index])
		if !ok {
			continue
		}
		accumulatedTokens += EstimateTokens(message)
		if accumulatedTokens >= keepRecentTokens {
			for _, candidate := range cutPoints {
				if candidate >= index {
					cutIndex = candidate
					break
				}
			}
			break
		}
	}
	for cutIndex > startIndex {
		previous := entries[cutIndex-1]
		if asEntryKind(previous) == harnesstypes.EntryTypeCompaction {
			break
		}
		if asEntryKind(previous) == harnesstypes.EntryTypeMessage {
			break
		}
		cutIndex--
	}
	cutEntry := entries[cutIndex]
	isUserMessage := false
	if message, ok := messageFromMessageEntry(cutEntry); ok {
		isUserMessage = agentMessageRole(message) == "user"
	}
	turnStartIndex := -1
	if !isUserMessage {
		turnStartIndex = FindTurnStartIndex(entries, cutIndex, startIndex)
	}
	return CutPointResult{
		FirstKeptEntryIndex: cutIndex,
		TurnStartIndex:      turnStartIndex,
		IsSplitTurn:         !isUserMessage && turnStartIndex != -1,
	}
}

// SummarizationSystemPrompt is the fixed system prompt for summary generation.
const SummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

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

const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// SummaryUsage is the text and provider usage of one generated summary.
type SummaryUsage struct {
	Text  string        `json:"text"`
	Usage aitypes.Usage `json:"usage"`
}

// SummaryGenerationOptions are the inputs of one summary generation.
type SummaryGenerationOptions struct {
	Model              aitypes.Model
	ReserveTokens      int
	CustomInstructions *string
	PreviousSummary    *string
	ThinkingLevel      *agenttypes.ThinkingLevel
}

// GenerateSummary generates or updates a conversation summary for compaction.
func GenerateSummary(
	currentMessages []agenttypes.AgentMessage,
	models ai.Models,
	model aitypes.Model,
	reserveTokens int,
	customInstructions *string,
	previousSummary *string,
	thinkingLevel *agenttypes.ThinkingLevel,
	retry *aiutils.RetryPolicy,
	callbacks *aiutils.RetryCallbacks,
	context harnesstypes.Context,
) (harnesstypes.Result[string, *harnesstypes.CompactionError], error) {
	result, err := GenerateSummaryWithUsage(
		currentMessages,
		models,
		model,
		reserveTokens,
		customInstructions,
		previousSummary,
		thinkingLevel,
		retry,
		callbacks,
		context,
	)
	if err != nil {
		return harnesstypes.Result[string, *harnesstypes.CompactionError]{}, err
	}
	if !result.OK {
		return harnesstypes.Err[string, *harnesstypes.CompactionError](result.Error), nil
	}
	return harnesstypes.Ok[string, *harnesstypes.CompactionError](result.Value.Text), nil
}

// GenerateSummaryWithUsage generates or updates a conversation summary and
// returns its provider usage.
func GenerateSummaryWithUsage(
	currentMessages []agenttypes.AgentMessage,
	models ai.Models,
	model aitypes.Model,
	reserveTokens int,
	customInstructions *string,
	previousSummary *string,
	thinkingLevel *agenttypes.ThinkingLevel,
	retry *aiutils.RetryPolicy,
	callbacks *aiutils.RetryCallbacks,
	context harnesstypes.Context,
) (harnesstypes.Result[SummaryUsage, *harnesstypes.CompactionError], error) {
	return GenerateSummaryWithRequest(
		currentMessages,
		SummaryGenerationOptions{
			Model:              model,
			ReserveTokens:      reserveTokens,
			CustomInstructions: customInstructions,
			PreviousSummary:    previousSummary,
			ThinkingLevel:      thinkingLevel,
		},
		func(aiContext aitypes.Context, options aitypes.SimpleStreamOptions, requestContext harnesstypes.Context) (aitypes.AssistantMessage, error) {
			return CompleteSimpleWithRetries(models, model, aiContext, options, retry, callbacks, requestContext)
		},
		context,
	)
}

// GenerateSummaryWithRequest generates one summary through a caller-owned
// one-request boundary.
func GenerateSummaryWithRequest(
	currentMessages []agenttypes.AgentMessage,
	options SummaryGenerationOptions,
	request SummaryRequest,
	context harnesstypes.Context,
) (harnesstypes.Result[SummaryUsage, *harnesstypes.CompactionError], error) {
	model := options.Model
	limit := math.Floor(0.8 * float64(options.ReserveTokens))
	if model.MaxTokens > 0 && model.MaxTokens < limit {
		limit = model.MaxTokens
	}
	maxTokens := int(limit)
	basePrompt := summarizationPrompt
	if options.PreviousSummary != nil {
		basePrompt = updateSummarizationPrompt
	}
	if options.CustomInstructions != nil && *options.CustomInstructions != "" {
		basePrompt = basePrompt + "\n\nAdditional focus: " + *options.CustomInstructions
	}
	llmMessages, err := harnessmessages.ConvertToLlm(currentMessages)
	if err != nil {
		return harnesstypes.Result[SummaryUsage, *harnesstypes.CompactionError]{}, err
	}
	conversationText := SerializeConversation(llmMessages)
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n"
	if options.PreviousSummary != nil {
		promptText += "<previous-summary>\n" + *options.PreviousSummary + "\n</previous-summary>\n\n"
	}
	promptText += basePrompt

	summarizationMessages := []aitypes.Message{
		aitypes.NewUserMessageVariant(aitypes.NewUserMessageBlocks(
			[]aitypes.ContentBlock{aitypes.TextBlock(promptText)},
			currentTimeMillis(),
		)),
	}

	completionOptions := aitypes.SimpleStreamOptions{StreamOptions: aitypes.StreamOptions{MaxTokens: &maxTokens}}
	if model.Reasoning && options.ThinkingLevel != nil && *options.ThinkingLevel != aitypes.ThinkingOff {
		completionOptions.Reasoning = options.ThinkingLevel
	}

	systemPrompt := SummarizationSystemPrompt
	response, err := request(
		aitypes.Context{SystemPrompt: &systemPrompt, Messages: summarizationMessages},
		CreateSummaryRequestOptions(completionOptions, context),
		context,
	)
	if err != nil {
		return harnesstypes.Result[SummaryUsage, *harnesstypes.CompactionError]{}, err
	}
	if response.StopReason == aitypes.StopReasonAborted {
		return harnesstypes.Err[SummaryUsage, *harnesstypes.CompactionError](
			harnesstypes.NewCompactionError(harnesstypes.CompactionErrorAborted, errorMessageOr(response, "Summarization aborted"), nil),
		), nil
	}
	if response.StopReason == aitypes.StopReasonError {
		return harnesstypes.Err[SummaryUsage, *harnesstypes.CompactionError](
			harnesstypes.NewCompactionError(
				harnesstypes.CompactionErrorSummarizationFailed,
				"Summarization failed: "+errorMessageOr(response, "Unknown error"),
				nil,
			),
		), nil
	}
	return harnesstypes.Ok[SummaryUsage, *harnesstypes.CompactionError](SummaryUsage{
		Text:  aiutils.ContentText(response.Content),
		Usage: response.Usage,
	}), nil
}

func errorMessageOr(message aitypes.AssistantMessage, fallback string) string {
	if message.ErrorMessage != nil && *message.ErrorMessage != "" {
		return *message.ErrorMessage
	}
	return fallback
}

func currentTimeMillis() float64 {
	return float64(time.Now().UnixMilli())
}

// CompactionPreparation is the prepared input for a compaction run. It is the
// shared harness type, re-exported as the mapping target for the upstream
// declaration.
type CompactionPreparation = harnesstypes.CompactionPreparation

// PrepareCompaction prepares session entries for compaction, or returns a nil
// success when compaction is not applicable.
func PrepareCompaction(
	pathEntries []harnesstypes.Entry,
	settings CompactionSettings,
) (harnesstypes.Result[*CompactionPreparation, *harnesstypes.CompactionError], error) {
	if len(pathEntries) == 0 || asEntryKind(pathEntries[len(pathEntries)-1]) == harnesstypes.EntryTypeCompaction {
		return harnesstypes.Ok[*CompactionPreparation, *harnesstypes.CompactionError](nil), nil
	}

	prevCompactionIndex := -1
	for index := len(pathEntries) - 1; index >= 0; index-- {
		if asEntryKind(pathEntries[index]) == harnesstypes.EntryTypeCompaction {
			prevCompactionIndex = index
			break
		}
	}

	var previousSummary *string
	compactableEntries := pathEntries
	if prevCompactionIndex >= 0 {
		if prevCompaction, ok := asCompactionEntry(pathEntries[prevCompactionIndex]); ok {
			summary := prevCompaction.Summary
			previousSummary = &summary
			virtualRetained := make([]harnesstypes.Entry, 0, len(prevCompaction.RetainedTail))
			for index, message := range prevCompaction.RetainedTail {
				var parentID string
				if index == 0 {
					parentID = prevCompaction.ID
				} else {
					parentID = retainedEntryID(prevCompaction.ID, index-1)
				}
				parent := parentID
				virtualRetained = append(virtualRetained, harnesstypes.MessageEntry{
					EntryBase: harnesstypes.EntryBase{
						ID:        retainedEntryID(prevCompaction.ID, index),
						ParentID:  &parent,
						Seq:       prevCompaction.Seq,
						Timestamp: messageTimestamp(message),
						Type:      harnesstypes.EntryTypeMessage,
					},
					Message: message,
				})
			}
			tail := pathEntries[prevCompactionIndex+1:]
			combined := make([]harnesstypes.Entry, 0, len(virtualRetained)+len(tail))
			combined = append(combined, virtualRetained...)
			combined = append(combined, tail...)
			compactableEntries = combined
		}
	}
	boundaryEnd := len(compactableEntries)

	contextMessages := []agenttypes.AgentMessage{}
	for _, entry := range harnesssession.BuildContextEntries(pathEntries) {
		contextMessages = append(contextMessages, harnesssession.SessionEntryToContextMessages(entry)...)
	}
	tokensBefore := EstimateContextTokens(contextMessages).Tokens

	cutPoint := FindCutPoint(compactableEntries, 0, boundaryEnd, settings.KeepRecentTokens)
	historyEnd := cutPoint.FirstKeptEntryIndex
	if cutPoint.IsSplitTurn {
		historyEnd = cutPoint.TurnStartIndex
	}
	messagesToSummarize := []agenttypes.AgentMessage{}
	for index := 0; index < historyEnd && index < len(compactableEntries); index++ {
		if message, ok := compactionMessageFromEntry(compactableEntries[index]); ok {
			messagesToSummarize = append(messagesToSummarize, message)
		}
	}
	turnPrefixMessages := []agenttypes.AgentMessage{}
	if cutPoint.IsSplitTurn {
		for index := cutPoint.TurnStartIndex; index < cutPoint.FirstKeptEntryIndex; index++ {
			if message, ok := compactionMessageFromEntry(compactableEntries[index]); ok {
				turnPrefixMessages = append(turnPrefixMessages, message)
			}
		}
	}
	retainedTail := []agenttypes.AgentMessage{}
	for index := cutPoint.FirstKeptEntryIndex; index < boundaryEnd; index++ {
		if message, ok := compactionMessageFromEntry(compactableEntries[index]); ok {
			retainedTail = append(retainedTail, message)
		}
	}
	fileOps := extractFileOperations(messagesToSummarize, pathEntries, prevCompactionIndex)
	if cutPoint.IsSplitTurn {
		for _, message := range turnPrefixMessages {
			ExtractFileOpsFromMessage(message, &fileOps)
		}
	}

	return harnesstypes.Ok[*CompactionPreparation, *harnesstypes.CompactionError](&CompactionPreparation{
		MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages:  turnPrefixMessages,
		RetainedTail:        retainedTail,
		IsSplitTurn:         cutPoint.IsSplitTurn,
		TokensBefore:        tokensBefore,
		PreviousSummary:     previousSummary,
		FileOps:             fileOps,
		Settings:            settings,
	}), nil
}

func retainedEntryID(compactionID string, index int) string {
	return compactionID + ":retained:" + strconv.Itoa(index)
}

func extractFileOperations(
	messages []agenttypes.AgentMessage,
	entries []harnesstypes.Entry,
	prevCompactionIndex int,
) FileOperations {
	fileOps := CreateFileOps()
	if prevCompactionIndex >= 0 && prevCompactionIndex < len(entries) {
		if prevCompaction, ok := asCompactionEntry(entries[prevCompactionIndex]); ok {
			readFiles, modifiedFiles := compactionDetailsFileLists(prevCompaction.Details)
			for _, path := range readFiles {
				fileOps.Read = addUnique(fileOps.Read, path)
			}
			for _, path := range modifiedFiles {
				fileOps.Edited = addUnique(fileOps.Edited, path)
			}
		}
	}
	for _, message := range messages {
		ExtractFileOpsFromMessage(message, &fileOps)
	}
	return fileOps
}

func compactionDetailsFileLists(details any) (readFiles []string, modifiedFiles []string) {
	if details == nil {
		return nil, nil
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, nil
	}
	readFiles = stringList(parsed["readFiles"])
	modifiedFiles = stringList(parsed["modifiedFiles"])
	return readFiles, modifiedFiles
}

func stringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

const turnPrefixSummarizationPrompt = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the retained recent work]

Be concise. Focus on what's needed to understand the kept suffix.`

// CompactGenerationOptions are the inputs of one compaction generation.
type CompactGenerationOptions struct {
	Model              aitypes.Model
	CustomInstructions *string
	ThinkingLevel      *agenttypes.ThinkingLevel
}

// Compact generates compaction summary data from prepared session history.
func Compact(
	preparation CompactionPreparation,
	models ai.Models,
	model aitypes.Model,
	customInstructions *string,
	thinkingLevel *agenttypes.ThinkingLevel,
	retry *aiutils.RetryPolicy,
	callbacks *aiutils.RetryCallbacks,
	context harnesstypes.Context,
) (harnesstypes.Result[CompactResult, *harnesstypes.CompactionError], error) {
	return CompactWithRequest(
		preparation,
		CompactGenerationOptions{Model: model, CustomInstructions: customInstructions, ThinkingLevel: thinkingLevel},
		func(aiContext aitypes.Context, options aitypes.SimpleStreamOptions, requestContext harnesstypes.Context) (aitypes.AssistantMessage, error) {
			return CompleteSimpleWithRetries(models, model, aiContext, options, retry, callbacks, requestContext)
		},
		context,
	)
}

// CompactWithRequest generates compaction data through a caller-owned boundary
// for each provider request.
func CompactWithRequest(
	preparation CompactionPreparation,
	options CompactGenerationOptions,
	request SummaryRequest,
	context harnesstypes.Context,
) (harnesstypes.Result[CompactResult, *harnesstypes.CompactionError], error) {
	model := options.Model
	var summary string
	var summaryUsage aitypes.Usage

	if preparation.IsSplitTurn && len(preparation.TurnPrefixMessages) > 0 {
		historyText := "No prior history."
		var historyUsage *aitypes.Usage
		if len(preparation.MessagesToSummarize) > 0 {
			historyResult, err := GenerateSummaryWithRequest(
				preparation.MessagesToSummarize,
				SummaryGenerationOptions{
					Model:              model,
					ReserveTokens:      preparation.Settings.ReserveTokens,
					CustomInstructions: options.CustomInstructions,
					PreviousSummary:    preparation.PreviousSummary,
					ThinkingLevel:      options.ThinkingLevel,
				},
				request,
				context,
			)
			if err != nil {
				return harnesstypes.Result[CompactResult, *harnesstypes.CompactionError]{}, err
			}
			if !historyResult.OK {
				return harnesstypes.Err[CompactResult, *harnesstypes.CompactionError](historyResult.Error), nil
			}
			historyText = historyResult.Value.Text
			usage := historyResult.Value.Usage
			historyUsage = &usage
		}
		turnPrefixResult, err := generateTurnPrefixSummary(
			preparation.TurnPrefixMessages,
			model,
			preparation.Settings.ReserveTokens,
			options.ThinkingLevel,
			request,
			context,
		)
		if err != nil {
			return harnesstypes.Result[CompactResult, *harnesstypes.CompactionError]{}, err
		}
		if !turnPrefixResult.OK {
			return harnesstypes.Err[CompactResult, *harnesstypes.CompactionError](turnPrefixResult.Error), nil
		}
		summary = historyText + "\n\n---\n\n**Turn Context (split turn):**\n\n" + turnPrefixResult.Value.Text
		if historyUsage != nil {
			summaryUsage = usageutils.AddUsage(*historyUsage, turnPrefixResult.Value.Usage)
		} else {
			summaryUsage = turnPrefixResult.Value.Usage
		}
	} else {
		summaryResult, err := GenerateSummaryWithRequest(
			preparation.MessagesToSummarize,
			SummaryGenerationOptions{
				Model:              model,
				ReserveTokens:      preparation.Settings.ReserveTokens,
				CustomInstructions: options.CustomInstructions,
				PreviousSummary:    preparation.PreviousSummary,
				ThinkingLevel:      options.ThinkingLevel,
			},
			request,
			context,
		)
		if err != nil {
			return harnesstypes.Result[CompactResult, *harnesstypes.CompactionError]{}, err
		}
		if !summaryResult.OK {
			return harnesstypes.Err[CompactResult, *harnesstypes.CompactionError](summaryResult.Error), nil
		}
		summary = summaryResult.Value.Text
		summaryUsage = summaryResult.Value.Usage
	}

	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)

	return harnesstypes.Ok[CompactResult, *harnesstypes.CompactionError](CompactResult{
		Summary:      summary,
		TokensBefore: preparation.TokensBefore,
		Usage:        &summaryUsage,
		RetainedTail: preparation.RetainedTail,
		Details:      &CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles},
	}), nil
}

func generateTurnPrefixSummary(
	messages []agenttypes.AgentMessage,
	model aitypes.Model,
	reserveTokens int,
	thinkingLevel *agenttypes.ThinkingLevel,
	request SummaryRequest,
	context harnesstypes.Context,
) (harnesstypes.Result[SummaryUsage, *harnesstypes.CompactionError], error) {
	limit := math.Floor(0.5 * float64(reserveTokens))
	if model.MaxTokens > 0 && model.MaxTokens < limit {
		limit = model.MaxTokens
	}
	maxTokens := int(limit)
	llmMessages, err := harnessmessages.ConvertToLlm(messages)
	if err != nil {
		return harnesstypes.Result[SummaryUsage, *harnesstypes.CompactionError]{}, err
	}
	conversationText := SerializeConversation(llmMessages)
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n" + turnPrefixSummarizationPrompt
	summarizationMessages := []aitypes.Message{
		aitypes.NewUserMessageVariant(aitypes.NewUserMessageBlocks(
			[]aitypes.ContentBlock{aitypes.TextBlock(promptText)},
			currentTimeMillis(),
		)),
	}
	completionOptions := aitypes.SimpleStreamOptions{StreamOptions: aitypes.StreamOptions{MaxTokens: &maxTokens}}
	if model.Reasoning && thinkingLevel != nil && *thinkingLevel != aitypes.ThinkingOff {
		completionOptions.Reasoning = thinkingLevel
	}
	systemPrompt := SummarizationSystemPrompt
	response, err := request(
		aitypes.Context{SystemPrompt: &systemPrompt, Messages: summarizationMessages},
		CreateSummaryRequestOptions(completionOptions, context),
		context,
	)
	if err != nil {
		return harnesstypes.Result[SummaryUsage, *harnesstypes.CompactionError]{}, err
	}
	if response.StopReason == aitypes.StopReasonAborted {
		return harnesstypes.Err[SummaryUsage, *harnesstypes.CompactionError](
			harnesstypes.NewCompactionError(harnesstypes.CompactionErrorAborted, errorMessageOr(response, "Turn prefix summarization aborted"), nil),
		), nil
	}
	if response.StopReason == aitypes.StopReasonError {
		return harnesstypes.Err[SummaryUsage, *harnesstypes.CompactionError](
			harnesstypes.NewCompactionError(
				harnesstypes.CompactionErrorSummarizationFailed,
				"Turn prefix summarization failed: "+errorMessageOr(response, "Unknown error"),
				nil,
			),
		), nil
	}
	return harnesstypes.Ok[SummaryUsage, *harnesstypes.CompactionError](SummaryUsage{
		Text:  aiutils.ContentText(response.Content),
		Usage: response.Usage,
	}), nil
}

// asEntryKind returns the entry discriminator.
func asEntryKind(entry harnesstypes.Entry) harnesstypes.EntryType {
	if entry == nil {
		return ""
	}
	return entry.EntryKind()
}

func asMessageEntry(entry harnesstypes.Entry) (harnesstypes.MessageEntry, bool) {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		return typed, true
	case *harnesstypes.MessageEntry:
		return *typed, true
	default:
		return harnesstypes.MessageEntry{}, false
	}
}

func messageFromMessageEntry(entry harnesstypes.Entry) (agenttypes.AgentMessage, bool) {
	if messageEntry, ok := asMessageEntry(entry); ok {
		return messageEntry.Message, true
	}
	return agenttypes.AgentMessage{}, false
}

func asCompactionEntry(entry harnesstypes.Entry) (harnesstypes.CompactionEntry, bool) {
	switch typed := entry.(type) {
	case harnesstypes.CompactionEntry:
		return typed, true
	case *harnesstypes.CompactionEntry:
		return *typed, true
	default:
		return harnesstypes.CompactionEntry{}, false
	}
}

func asBranchSummaryEntry(entry harnesstypes.Entry) (harnesstypes.BranchSummaryEntry, bool) {
	switch typed := entry.(type) {
	case harnesstypes.BranchSummaryEntry:
		return typed, true
	case *harnesstypes.BranchSummaryEntry:
		return *typed, true
	default:
		return harnesstypes.BranchSummaryEntry{}, false
	}
}

func branchSummaryAgentMessage(summary string, fromID *string, timestamp float64) agenttypes.AgentMessage {
	message := harnessmessages.CreateBranchSummaryMessage(summary, fromID, harnessmessages.TimestampFromNumber(timestamp))
	raw, err := json.Marshal(message)
	if err != nil {
		return agenttypes.AgentMessage{}
	}
	return agenttypes.NewCustomMessage(harnessmessages.BranchSummaryRole, raw)
}

func compactionSummaryAgentMessage(summary string, tokensBefore float64, timestamp float64) agenttypes.AgentMessage {
	message := harnessmessages.CreateCompactionSummaryMessage(summary, tokensBefore, harnessmessages.TimestampFromNumber(timestamp))
	raw, err := json.Marshal(message)
	if err != nil {
		return agenttypes.AgentMessage{}
	}
	return agenttypes.NewCustomMessage(harnessmessages.CompactionSummaryRole, raw)
}

func messageTimestamp(message agenttypes.AgentMessage) float64 {
	if message.Message != nil {
		switch {
		case message.Message.User != nil:
			return message.Message.User.Timestamp
		case message.Message.Assistant != nil:
			return message.Message.Assistant.Timestamp
		case message.Message.ToolResult != nil:
			return message.Message.ToolResult.Timestamp
		case message.Message.System != nil:
			return message.Message.System.Timestamp
		}
	}
	if message.Custom != nil {
		var probe struct {
			Timestamp float64 `json:"timestamp"`
		}
		if json.Unmarshal(message.Custom.Raw, &probe) == nil {
			return probe.Timestamp
		}
	}
	return 0
}

// compactionMessageFromEntry projects one entry onto a compaction message. A
// previous compaction entry contributes nothing directly: its retained tail
// was already expanded into virtual entries.
func compactionMessageFromEntry(entry harnesstypes.Entry) (agenttypes.AgentMessage, bool) {
	switch asEntryKind(entry) {
	case harnesstypes.EntryTypeCompaction:
		return agenttypes.AgentMessage{}, false
	case harnesstypes.EntryTypeMessage:
		return messageFromMessageEntry(entry)
	case harnesstypes.EntryTypeBranchSummary:
		if branch, ok := asBranchSummaryEntry(entry); ok {
			return branchSummaryAgentMessage(branch.Summary, branch.FromID, branch.Timestamp), true
		}
	}
	return agenttypes.AgentMessage{}, false
}
