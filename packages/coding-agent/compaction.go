// Context compaction for the embedded SDK.
//
// This file ports packages/coding-agent/src/core/compaction/compaction.ts,
// utils.ts and branch-summarization.ts from Pi at revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe. The accepted Pith harness already
// implements the token estimators, cut-point search, file-operation tracking
// and summarization prompts, so this layer adapts those primitives to the
// coding-agent SessionEntry tree and exposes the source-owned names instead of
// re-deriving the algorithms.
//
// A length-truncated summary is never persisted: GetSummarizationFailure
// rejects "length" and "error" stops, and the caller appends the checkpoint
// only after a summary is available. A failed summarization therefore leaves
// the durable history untouched.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	harnesscompaction "github.com/minifish-org/pith/packages/agent/harness/compaction"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// defaultKeepRecentMessages is the verbatim tail retained by Compact when the
// caller leaves RunPolicy.KeepRecentMessages at zero.
const defaultKeepRecentMessages = 2

// CompactionDetails are the file-operation details stored on a compaction entry.
type CompactionDetails = harnesscompaction.CompactionDetails

// FileOperations are the file operations extracted from summarized history.
type FileOperations = harnesscompaction.FileOperations

// ContextUsageEstimate is the estimated context-token usage for a message list.
type ContextUsageEstimate = harnesscompaction.ContextUsageEstimate

// CutPointResult is the selected compaction boundary.
type CutPointResult = harnesscompaction.CutPointResult

// CompactionPolicy mirrors the upstream CompactionSettings shape (enabled,
// reserveTokens, keepRecentTokens). It is distinct from the settings-manager
// CompactionSettings because that type also carries per-model overrides.
type CompactionPolicy struct {
	Enabled          bool `json:"enabled"`
	ReserveTokens    int  `json:"reserveTokens"`
	KeepRecentTokens int  `json:"keepRecentTokens"`
}

// DefaultCompactionPolicy are the upstream defaults.
var DefaultCompactionPolicy = CompactionPolicy{
	Enabled:          true,
	ReserveTokens:    harnesscompaction.DefaultCompactionSettings.ReserveTokens,
	KeepRecentTokens: harnesscompaction.DefaultCompactionSettings.KeepRecentTokens,
}

// CompactionResult is the outcome of one compaction run. SessionManager adds
// the uuid/parent uuid when the checkpoint is appended.
type CompactionResult struct {
	Summary              string         `json:"summary"`
	FirstKeptEntryID     string         `json:"firstKeptEntryId"`
	TokensBefore         float64        `json:"tokensBefore"`
	EstimatedTokensAfter float64        `json:"estimatedTokensAfter,omitempty"`
	Usage                *aitypes.Usage `json:"usage,omitempty"`
	Details              []byte         `json:"details,omitempty"`
}

// CompactionPreparation are the prepared inputs for a compaction run.
type CompactionPreparation struct {
	FirstKeptEntryID    string
	MessagesToSummarize []agenttypes.AgentMessage
	TurnPrefixMessages  []agenttypes.AgentMessage
	RetainedTail        []agenttypes.AgentMessage
	IsSplitTurn         bool
	TokensBefore        float64
	PreviousSummary     *string
	FileOps             FileOperations
	Settings            CompactionPolicy
}

// SummarizationSystemPrompt is the fixed system prompt for summary generation.
const SummarizationSystemPrompt = harnesscompaction.SummarizationSystemPrompt

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
- PRESERVE exact file paths, function names, and error messages

Use the same structured format as the existing summary.`

// ---------------------------------------------------------------------------
// Token calculation
// ---------------------------------------------------------------------------

// CalculateContextTokens calculates total context tokens from provider usage.
func CalculateContextTokens(usage aitypes.Usage) float64 {
	return harnesscompaction.CalculateContextTokens(usage)
}

// EstimateTokens estimates the token count of one agent message.
func EstimateTokens(message agenttypes.AgentMessage) int {
	return harnesscompaction.EstimateTokens(message)
}

// EstimateContextTokens estimates context tokens for a message list.
func EstimateContextTokens(messages []agenttypes.AgentMessage) ContextUsageEstimate {
	return harnesscompaction.EstimateContextTokens(messages)
}

// GetLastAssistantUsage returns usage from the last valid assistant message.
func GetLastAssistantUsage(entries []SessionEntry) *aitypes.Usage {
	for index := len(entries) - 1; index >= 0; index-- {
		for _, message := range SessionEntryToContextMessages(entries[index]) {
			if usage := validAssistantUsage(message); usage != nil {
				return usage
			}
		}
	}
	return nil
}

func validAssistantUsage(message agenttypes.AgentMessage) *aitypes.Usage {
	assistant, ok := assistantMessageOf(&message)
	if !ok {
		return nil
	}
	if assistant.StopReason == aitypes.StopReasonAborted || assistant.StopReason == aitypes.StopReasonError {
		return nil
	}
	if CalculateContextTokens(assistant.Usage) <= 0 {
		return nil
	}
	usage := assistant.Usage
	return &usage
}

// EstimateProjectedContextTokens estimates projected context without trusting
// usage captured before a later context edit or compaction.
func EstimateProjectedContextTokens(projection SessionProjection, branchEntries []SessionEntry) ContextUsageEstimate {
	for _, entry := range branchEntries {
		if entry.Type == "context_edit" || entry.Type == "compaction" {
			return pureMessageEstimate(projection.Messages)
		}
	}
	return EstimateContextTokens(projection.Messages)
}

func pureMessageEstimate(messages []agenttypes.AgentMessage) ContextUsageEstimate {
	total := 0
	for _, message := range messages {
		total += EstimateTokens(message)
	}
	return ContextUsageEstimate{
		Tokens:         float64(total),
		UsageTokens:    0,
		TrailingTokens: float64(total),
		LastUsageIndex: nil,
	}
}

// ShouldCompact reports whether context usage exceeds the configured threshold.
func ShouldCompact(contextTokens float64, contextWindow float64, settings CompactionPolicy) bool {
	if !settings.Enabled {
		return false
	}
	reserve := settings.ReserveTokens
	if reserve <= 0 {
		reserve = DefaultCompactionPolicy.ReserveTokens
	}
	return contextTokens > contextWindow-float64(reserve)
}

// ---------------------------------------------------------------------------
// Cut point detection
// ---------------------------------------------------------------------------

// FindTurnStartIndex returns the context-visible turn-start entry at or before
// entryIndex, or -1 when none is found.
func FindTurnStartIndex(entries []SessionEntry, entryIndex int, startIndex int) int {
	if entryIndex >= len(entries) {
		entryIndex = len(entries) - 1
	}
	for index := entryIndex; index >= startIndex; index-- {
		if index < 0 || index >= len(entries) {
			continue
		}
		for _, message := range SessionEntryToContextMessages(entries[index]) {
			if isTurnStartRole(messageRoleOf(message)) {
				return index
			}
		}
	}
	return -1
}

// FindCutPoint finds the compaction cut point that keeps approximately the
// requested recent-token budget without cutting at a tool result.
func FindCutPoint(entries []SessionEntry, startIndex int, endIndex int, keepRecentTokens int) CutPointResult {
	if startIndex < 0 {
		startIndex = 0
	}
	if endIndex > len(entries) {
		endIndex = len(entries)
	}
	cutPoints := make([]int, 0, endIndex-startIndex)
	for index := startIndex; index < endIndex; index++ {
		if entries[index].Type == "compaction" {
			continue
		}
		for _, message := range SessionEntryToContextMessages(entries[index]) {
			if isCutPointRole(messageRoleOf(message)) {
				cutPoints = append(cutPoints, index)
				break
			}
		}
	}
	if len(cutPoints) == 0 {
		return CutPointResult{FirstKeptEntryIndex: startIndex, TurnStartIndex: -1, IsSplitTurn: false}
	}
	accumulated := 0
	cutIndex := cutPoints[0]
	for index := endIndex - 1; index >= startIndex; index-- {
		messageTokens := 0
		for _, message := range SessionEntryToContextMessages(entries[index]) {
			messageTokens += EstimateTokens(message)
		}
		if messageTokens == 0 {
			continue
		}
		accumulated += messageTokens
		if accumulated >= keepRecentTokens {
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
		if previous.Type == "compaction" {
			break
		}
		if len(SessionEntryToContextMessages(previous)) > 0 {
			break
		}
		cutIndex--
	}
	startsTurn := false
	for _, message := range SessionEntryToContextMessages(entries[cutIndex]) {
		if isTurnStartRole(messageRoleOf(message)) {
			startsTurn = true
			break
		}
	}
	turnStart := -1
	if !startsTurn {
		turnStart = FindTurnStartIndex(entries, cutIndex, startIndex)
	}
	return CutPointResult{
		FirstKeptEntryIndex: cutIndex,
		TurnStartIndex:      turnStart,
		IsSplitTurn:         !startsTurn && turnStart != -1,
	}
}

// selectCompactionCut returns the index of the first entry kept verbatim, or
// -1 when no safe cut exists. It always cuts at a turn-start (user-like)
// message so no assistant tool call is separated from its tool results, and it
// keeps at least `keep` context messages when the tree allows it.
func selectCompactionCut(entries []SessionEntry, keep int) int {
	if keep <= 0 {
		keep = defaultKeepRecentMessages
	}
	type messageRef struct {
		entryIndex int
		role       string
	}
	refs := make([]messageRef, 0, len(entries))
	for index, entry := range entries {
		for _, message := range SessionEntryToContextMessages(entry) {
			refs = append(refs, messageRef{entryIndex: index, role: messageRoleOf(message)})
		}
	}
	if len(refs) <= keep {
		return -1
	}
	for refIndex := len(refs) - 1; refIndex >= 0; refIndex-- {
		if refs[refIndex].role != aitypes.UserMessageRole {
			continue
		}
		count := 0
		for _, ref := range refs {
			if ref.entryIndex >= refs[refIndex].entryIndex {
				count++
			}
		}
		if count >= keep {
			return refs[refIndex].entryIndex
		}
	}
	return -1
}

// messageRoleOf returns the role of an agent message, including harness-only
// custom messages.
func messageRoleOf(message agenttypes.AgentMessage) string {
	if message.Message != nil {
		return string(message.Message.Role)
	}
	if message.Custom != nil {
		return message.Custom.Role
	}
	return ""
}

func isTurnStartRole(role string) bool {
	switch role {
	case aitypes.UserMessageRole, CustomRole, BranchSummaryRole, CompactionSummaryRole, BashExecutionRole:
		return true
	}
	return false
}

func isCutPointRole(role string) bool {
	switch role {
	case aitypes.UserMessageRole, aitypes.AssistantMessageRole, CustomRole, BranchSummaryRole, CompactionSummaryRole, BashExecutionRole:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// File operations
// ---------------------------------------------------------------------------

// CreateFileOps returns an empty file-operation accumulator.
func CreateFileOps() FileOperations { return harnesscompaction.CreateFileOps() }

// ExtractFileOpsFromMessage folds one message's tool calls into fileOps.
func ExtractFileOpsFromMessage(message agenttypes.AgentMessage, fileOps *FileOperations) {
	harnesscompaction.ExtractFileOpsFromMessage(message, fileOps)
}

// ComputeFileLists returns the sorted read and modified file lists.
func ComputeFileLists(fileOps FileOperations) (readFiles []string, modifiedFiles []string) {
	return harnesscompaction.ComputeFileLists(fileOps)
}

// FormatFileOperations renders file lists for a summary prompt.
func FormatFileOperations(readFiles []string, modifiedFiles []string) string {
	return harnesscompaction.FormatFileOperations(readFiles, modifiedFiles)
}

// SerializeConversation renders provider messages as a plain transcript.
func SerializeConversation(messages []aitypes.Message) string {
	return harnesscompaction.SerializeConversation(messages)
}

// ---------------------------------------------------------------------------
// Summarization
// ---------------------------------------------------------------------------

// GetSummarizationFailure returns a non-empty error string when a summary
// response cannot safely be persisted. A length stop carries partial text and
// must never become a checkpoint.
func GetSummarizationFailure(response aitypes.AssistantMessage, label string) string {
	if response.StopReason == aitypes.StopReasonError {
		message := "Unknown error"
		if response.ErrorMessage != nil && *response.ErrorMessage != "" {
			message = *response.ErrorMessage
		}
		return fmt.Sprintf("%s failed: %s", label, message)
	}
	if response.StopReason == aitypes.StopReasonLength {
		return fmt.Sprintf("%s failed: generation hit the token cap and the summary is incomplete", label)
	}
	return ""
}

// CompleteSummarization runs one summary request through a caller-owned
// stream function. It never mutates model limits and reports errors verbatim.
func CompleteSummarization(
	ctx context.Context,
	model *aitypes.Model,
	transcript *aitypes.TranscriptContext,
	options aitypes.SimpleStreamOptions,
	streamFn agenttypes.StreamFn,
) (aitypes.AssistantMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if model == nil {
		return aitypes.AssistantMessage{}, errors.New("summarization requires a model")
	}
	if streamFn == nil {
		return aitypes.AssistantMessage{}, errors.New("summarization requires a stream function")
	}
	if transcript == nil {
		return aitypes.AssistantMessage{}, errors.New("summarization requires a context")
	}
	options.Signal = ctx.Done()
	response := streamFn(model, transcript, &options)
	if response == nil {
		return aitypes.AssistantMessage{}, errors.New("summarization stream is nil")
	}
	return response.Result(ctx)
}

// GenerateSummary generates or updates a conversation summary and returns its
// text.
func GenerateSummary(
	ctx context.Context,
	currentMessages []agenttypes.AgentMessage,
	model *aitypes.Model,
	reserveTokens int,
	streamFn agenttypes.StreamFn,
	previousSummary *string,
	customInstructions *string,
) (string, error) {
	text, _, err := GenerateSummaryWithUsage(ctx, currentMessages, model, reserveTokens, streamFn, previousSummary, customInstructions)
	return text, err
}

// GenerateSummaryWithUsage generates or updates a conversation summary and
// returns its text plus provider usage.
func GenerateSummaryWithUsage(
	ctx context.Context,
	currentMessages []agenttypes.AgentMessage,
	model *aitypes.Model,
	reserveTokens int,
	streamFn agenttypes.StreamFn,
	previousSummary *string,
	customInstructions *string,
) (string, aitypes.Usage, error) {
	basePrompt := summarizationPrompt
	if previousSummary != nil {
		basePrompt = updateSummarizationPrompt
	}
	if customInstructions != nil && *customInstructions != "" {
		basePrompt = basePrompt + "\n\nAdditional focus: " + *customInstructions
	}
	return summarizeConversation(ctx, currentMessages, model, reserveTokens, streamFn, basePrompt, previousSummary)
}

func summarizeConversation(
	ctx context.Context,
	currentMessages []agenttypes.AgentMessage,
	model *aitypes.Model,
	reserveTokens int,
	streamFn agenttypes.StreamFn,
	basePrompt string,
	previousSummary *string,
) (string, aitypes.Usage, error) {
	if model == nil {
		return "", aitypes.Usage{}, errors.New("summarization requires a model")
	}
	if reserveTokens <= 0 {
		reserveTokens = DefaultCompactionPolicy.ReserveTokens
	}
	maxTokens := int(math.Floor(0.8 * float64(reserveTokens)))
	if model.MaxTokens > 0 && float64(maxTokens) > model.MaxTokens {
		maxTokens = int(model.MaxTokens)
	}
	if maxTokens <= 0 {
		maxTokens = 1
	}

	llmMessages, err := ConvertToLlm(currentMessages)
	if err != nil {
		return "", aitypes.Usage{}, err
	}
	conversationText := SerializeConversation(llmMessages)
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n"
	if previousSummary != nil {
		promptText += "<previous-summary>\n" + *previousSummary + "\n</previous-summary>\n\n"
	}
	promptText += basePrompt

	systemPrompt := SummarizationSystemPrompt
	userMessage := aitypes.NewUserMessageBlocks(
		[]aitypes.ContentBlock{aitypes.TextBlock(promptText)},
		float64(time.Now().UnixMilli()),
	)
	transcript := aitypes.NormalizeContext(aitypes.Context{
		SystemPrompt: &systemPrompt,
		Messages:     []aitypes.Message{aitypes.NewUserMessageVariant(userMessage)},
	})
	options := aitypes.SimpleStreamOptions{}
	options.MaxTokens = &maxTokens

	response, err := CompleteSummarization(ctx, model, transcript, options, streamFn)
	if err != nil {
		return "", aitypes.Usage{}, err
	}
	if failure := GetSummarizationFailure(response, "Summarization"); failure != "" {
		return "", aitypes.Usage{}, errors.New(failure)
	}
	for _, block := range response.Content {
		if block.IsToolCall() {
			return "", aitypes.Usage{}, errors.New("summarization attempted to call a tool")
		}
	}
	return aiutils.ContentText(response.Content), response.Usage, nil
}

// PrepareCompaction prepares session entries for compaction, or returns a nil
// success when compaction is not applicable.
func PrepareCompaction(entries []SessionEntry, settings CompactionPolicy) (*CompactionPreparation, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	if entries[len(entries)-1].Type == "compaction" {
		return nil, nil
	}
	keepRecentTokens := settings.KeepRecentTokens
	if keepRecentTokens <= 0 {
		keepRecentTokens = DefaultCompactionPolicy.KeepRecentTokens
	}
	cut := FindCutPoint(entries, 0, len(entries), keepRecentTokens)
	firstKept := entries[cut.FirstKeptEntryIndex]

	var messagesToSummarize []agenttypes.AgentMessage
	tokensBefore := float64(0)
	for index := 0; index < cut.FirstKeptEntryIndex; index++ {
		for _, message := range SessionEntryToContextMessages(entries[index]) {
			if messageRoleOf(message) == aitypes.SystemMessageRole {
				continue
			}
			messagesToSummarize = append(messagesToSummarize, message)
			tokensBefore += float64(EstimateTokens(message))
		}
	}
	var turnPrefix []agenttypes.AgentMessage
	if cut.IsSplitTurn && cut.TurnStartIndex >= 0 {
		for index := cut.TurnStartIndex; index < cut.FirstKeptEntryIndex; index++ {
			for _, message := range SessionEntryToContextMessages(entries[index]) {
				if messageRoleOf(message) == aitypes.SystemMessageRole {
					continue
				}
				turnPrefix = append(turnPrefix, message)
			}
		}
	}
	if len(messagesToSummarize) == 0 && len(turnPrefix) == 0 {
		return nil, nil
	}

	fileOps := CreateFileOps()
	for _, message := range messagesToSummarize {
		ExtractFileOpsFromMessage(message, &fileOps)
	}
	for _, message := range turnPrefix {
		ExtractFileOpsFromMessage(message, &fileOps)
	}

	var retained []agenttypes.AgentMessage
	for index := cut.FirstKeptEntryIndex; index < len(entries); index++ {
		retained = append(retained, SessionEntryToContextMessages(entries[index])...)
	}

	return &CompactionPreparation{
		FirstKeptEntryID:    firstKept.ID,
		MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages:  turnPrefix,
		RetainedTail:        retained,
		IsSplitTurn:         cut.IsSplitTurn,
		TokensBefore:        tokensBefore,
		FileOps:             fileOps,
		Settings:            settings,
	}, nil
}

// Compact summarizes prepared messages and returns a checkpoint result. The
// caller persists it; a failed summary returns an error and writes nothing.
func Compact(
	ctx context.Context,
	preparation CompactionPreparation,
	model *aitypes.Model,
	streamFn agenttypes.StreamFn,
) (CompactionResult, error) {
	if len(preparation.MessagesToSummarize) == 0 && len(preparation.TurnPrefixMessages) == 0 {
		return CompactionResult{}, errors.New("compaction requires messages to summarize")
	}
	messages := preparation.MessagesToSummarize
	if preparation.IsSplitTurn && len(preparation.TurnPrefixMessages) > 0 {
		messages = append(append([]agenttypes.AgentMessage{}, messages...), preparation.TurnPrefixMessages...)
	}
	summary, usage, err := GenerateSummaryWithUsage(
		ctx,
		messages,
		model,
		preparation.Settings.ReserveTokens,
		streamFn,
		preparation.PreviousSummary,
		nil,
	)
	if err != nil {
		return CompactionResult{}, err
	}
	if strings.TrimSpace(summary) == "" {
		return CompactionResult{}, errors.New("compaction summary is empty")
	}
	tokensAfter := float64(0)
	for _, message := range preparation.RetainedTail {
		tokensAfter += float64(EstimateTokens(message))
	}
	return CompactionResult{
		Summary:              summary,
		FirstKeptEntryID:     preparation.FirstKeptEntryID,
		TokensBefore:         preparation.TokensBefore,
		EstimatedTokensAfter: tokensAfter,
		Usage:                &usage,
	}, nil
}

// ---------------------------------------------------------------------------
// Branch summarization
// ---------------------------------------------------------------------------

// BranchSummaryResult is generated branch-summary data ready to be persisted.
type BranchSummaryResult struct {
	Summary       string         `json:"summary,omitempty"`
	Usage         *aitypes.Usage `json:"usage,omitempty"`
	ReadFiles     []string       `json:"readFiles,omitempty"`
	ModifiedFiles []string       `json:"modifiedFiles,omitempty"`
	Aborted       bool           `json:"aborted,omitempty"`
	Error         string         `json:"error,omitempty"`
}

// BranchSummaryDetails are the file-operation details stored on generated
// branch summary entries.
type BranchSummaryDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// BranchPreparation is the prepared branch content for summarization.
type BranchPreparation struct {
	Messages    []agenttypes.AgentMessage `json:"messages"`
	FileOps     FileOperations            `json:"fileOps"`
	TotalTokens float64                   `json:"totalTokens"`
}

// CollectEntriesResult are the entries selected for branch summarization.
type CollectEntriesResult struct {
	Entries          []SessionEntry `json:"entries"`
	CommonAncestorID *string        `json:"commonAncestorId"`
}

// GenerateBranchSummaryOptions are the options for generating a branch summary.
type GenerateBranchSummaryOptions struct {
	Model               *aitypes.Model
	StreamFn            agenttypes.StreamFn
	Signal              context.Context
	CustomInstructions  *string
	ReplaceInstructions *bool
	ReserveTokens       int
}

const branchSummaryPreamble = `The user explored a different conversation branch before returning here.
The following is a summary of that branch.`

const branchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

## Branch Goal
[What was the user trying to accomplish on this branch?]

## Progress
- [What was done]

## Outcomes
- [What was learned or changed]

## Files
- [Files read or modified]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// CollectEntriesForBranchSummary walks from oldLeafID back to the common
// ancestor with targetID and returns the abandoned entries in chronological
// order. It does not stop at compaction boundaries.
func CollectEntriesForBranchSummary(manager ReadonlySessionManager, oldLeafID *string, targetID string) CollectEntriesResult {
	if manager == nil || oldLeafID == nil || *oldLeafID == "" {
		return CollectEntriesResult{}
	}
	oldPath := manager.GetBranch(*oldLeafID)
	targetPath := manager.GetBranch(targetID)
	targetIDs := make(map[string]bool, len(targetPath))
	for _, entry := range targetPath {
		targetIDs[entry.ID] = true
	}
	var collected []SessionEntry
	var commonAncestor *string
	for index := len(oldPath) - 1; index >= 0; index-- {
		entry := oldPath[index]
		if targetIDs[entry.ID] {
			id := entry.ID
			commonAncestor = &id
			break
		}
		collected = append(collected, entry)
	}
	for left, right := 0, len(collected)-1; left < right; left, right = left+1, right-1 {
		collected[left], collected[right] = collected[right], collected[left]
	}
	return CollectEntriesResult{Entries: collected, CommonAncestorID: commonAncestor}
}

// PrepareBranchEntries selects branch messages up to tokenBudget and extracts
// their file operations.
func PrepareBranchEntries(entries []SessionEntry, tokenBudget int) BranchPreparation {
	if tokenBudget <= 0 {
		tokenBudget = DefaultCompactionPolicy.ReserveTokens
	}
	preparation := BranchPreparation{FileOps: CreateFileOps()}
	for index := len(entries) - 1; index >= 0; index-- {
		for _, message := range SessionEntryToContextMessages(entries[index]) {
			tokens := EstimateTokens(message)
			if len(preparation.Messages) > 0 && preparation.TotalTokens+float64(tokens) > float64(tokenBudget) {
				return preparation
			}
			preparation.Messages = append([]agenttypes.AgentMessage{message}, preparation.Messages...)
			preparation.TotalTokens += float64(tokens)
			ExtractFileOpsFromMessage(message, &preparation.FileOps)
		}
	}
	return preparation
}

// GenerateBranchSummary summarizes a prepared branch. A caller-supplied
// transaction that cannot be summarized returns an error and persists nothing.
func GenerateBranchSummary(
	ctx context.Context,
	preparation BranchPreparation,
	options GenerateBranchSummaryOptions,
) (BranchSummaryResult, error) {
	if len(preparation.Messages) == 0 {
		return BranchSummaryResult{}, nil
	}
	if ctx == nil {
		ctx = options.Signal
	}
	if ctx == nil {
		ctx = context.Background()
	}
	basePrompt := branchSummaryPreamble + "\n\n" + branchSummaryPrompt
	if options.CustomInstructions != nil && *options.CustomInstructions != "" {
		if options.ReplaceInstructions != nil && *options.ReplaceInstructions {
			basePrompt = *options.CustomInstructions
		} else {
			basePrompt = basePrompt + "\n\nAdditional focus: " + *options.CustomInstructions
		}
	}
	reserve := options.ReserveTokens
	if reserve <= 0 {
		reserve = DefaultCompactionPolicy.ReserveTokens
	}
	text, usage, err := summarizeConversation(
		ctx,
		preparation.Messages,
		options.Model,
		reserve,
		options.StreamFn,
		basePrompt,
		nil,
	)
	if err != nil {
		return BranchSummaryResult{}, err
	}
	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	return BranchSummaryResult{
		Summary:       text,
		Usage:         &usage,
		ReadFiles:     readFiles,
		ModifiedFiles: modifiedFiles,
	}, nil
}
