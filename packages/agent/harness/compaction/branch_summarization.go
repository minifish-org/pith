// This file carries branch-summarization.ts: collecting abandoned branch
// entries and generating a structured branch summary.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package compaction

import (
	"encoding/json"

	harnessmessages "github.com/minifish-org/pith/packages/agent/harness/messages"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	ai "github.com/minifish-org/pith/packages/ai"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	aiutils "github.com/minifish-org/pith/packages/ai/utils"
)

// BranchSummaryResult is generated branch summary data ready to be persisted
// as a branch-summary entry.
type BranchSummaryResult struct {
	Summary       string         `json:"summary"`
	Usage         *aitypes.Usage `json:"usage,omitempty"`
	ReadFiles     []string       `json:"readFiles"`
	ModifiedFiles []string       `json:"modifiedFiles"`
}

// BranchSummaryDetails are the file-operation details stored on generated
// branch summary entries.
type BranchSummaryDetails struct {
	// ReadFiles are files read while exploring the summarized branch.
	ReadFiles []string `json:"readFiles"`
	// ModifiedFiles are files modified while exploring the summarized branch.
	ModifiedFiles []string `json:"modifiedFiles"`
}

// BranchPreparation is the shared prepared branch content for summarization.
type BranchPreparation = harnesstypes.BranchPreparation

// CollectEntriesResult are the entries selected for branch summarization.
type CollectEntriesResult struct {
	// Entries are the entries to summarize in chronological order.
	Entries []harnesstypes.Entry `json:"entries"`
	// CommonAncestorID is the deepest common ancestor between the previous tip
	// and target entry, or null.
	CommonAncestorID *string `json:"commonAncestorId"`
}

// GenerateBranchSummaryOptions are the options for generating a branch summary.
type GenerateBranchSummaryOptions struct {
	// Models is the provider collection the request goes through.
	Models ai.Models
	// Model is the model used for summarization.
	Model aitypes.Model
	// CustomInstructions are appended to, or replace, the default prompt.
	CustomInstructions *string
	// ReplaceInstructions replaces the default prompt with custom instructions.
	ReplaceInstructions *bool
	// ReserveTokens are the tokens reserved for prompt and output. Defaults to
	// 16384.
	ReserveTokens *int
	// Retry is the optional retry policy for transient failures.
	Retry *aiutils.RetryPolicy
	// Callbacks are the optional retry reporting callbacks.
	Callbacks *aiutils.RetryCallbacks
}

// BranchEntryFinder is the minimal branch read capability used while
// collecting abandoned entries.
type BranchEntryFinder interface {
	FindEntries(query *harnesstypes.BranchScan, ctx harnesstypes.Context) ([]harnesstypes.Entry, error)
}

// SessionEntryGetter is the minimal session read capability used while
// collecting abandoned entries.
type SessionEntryGetter interface {
	GetEntry(id string, ctx harnesstypes.Context) (harnesstypes.Entry, bool, error)
}

// CollectEntriesForBranchSummary collects entries that should be summarized
// before navigating to a different session tree entry.
func CollectEntriesForBranchSummary(
	branch BranchEntryFinder,
	session SessionEntryGetter,
	oldTipID *string,
	targetID string,
	context harnesstypes.Context,
) (CollectEntriesResult, error) {
	if oldTipID == nil {
		return CollectEntriesResult{Entries: []harnesstypes.Entry{}, CommonAncestorID: nil}, nil
	}
	oldPathEntries, err := branch.FindEntries(&harnesstypes.BranchScan{Start: oldTipID}, context)
	if err != nil {
		return CollectEntriesResult{}, err
	}
	oldPath := map[string]struct{}{}
	for _, entry := range oldPathEntries {
		if base, ok := entryBaseOf(entry); ok {
			oldPath[base.ID] = struct{}{}
		}
	}
	targetPath, err := branch.FindEntries(&harnesstypes.BranchScan{Start: &targetID}, context)
	if err != nil {
		return CollectEntriesResult{}, err
	}
	var commonAncestorID *string
	for _, entry := range targetPath {
		if base, ok := entryBaseOf(entry); ok {
			if _, present := oldPath[base.ID]; present {
				ancestor := base.ID
				commonAncestorID = &ancestor
				break
			}
		}
	}
	entries := []harnesstypes.Entry{}
	current := oldTipID
	for current != nil && (commonAncestorID == nil || *current != *commonAncestorID) {
		entry, found, err := session.GetEntry(*current, context)
		if err != nil {
			return CollectEntriesResult{}, err
		}
		if !found {
			return CollectEntriesResult{}, &missingEntryError{id: *current}
		}
		entries = append(entries, entry)
		base, ok := entryBaseOf(entry)
		if !ok {
			break
		}
		current = base.ParentID
	}
	reverseEntries(entries)
	return CollectEntriesResult{Entries: entries, CommonAncestorID: commonAncestorID}, nil
}

type missingEntryError struct{ id string }

func (e *missingEntryError) Error() string {
	return "Corrupt session: entry " + e.id + " not found"
}

func reverseEntries(entries []harnesstypes.Entry) {
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
}

// branchMessageFromEntry projects one entry onto a branch-summary message.
// Tool results and application custom entries contribute nothing.
func branchMessageFromEntry(entry harnesstypes.Entry) (agenttypes.AgentMessage, bool) {
	switch asEntryKind(entry) {
	case harnesstypes.EntryTypeMessage:
		message, ok := messageFromMessageEntry(entry)
		if !ok {
			return agenttypes.AgentMessage{}, false
		}
		if agentMessageRole(message) == "toolResult" {
			return agenttypes.AgentMessage{}, false
		}
		return message, true
	case harnesstypes.EntryTypeBranchSummary:
		if branch, ok := asBranchSummaryEntry(entry); ok {
			return branchSummaryAgentMessage(branch.Summary, branch.FromID, branch.Timestamp), true
		}
	case harnesstypes.EntryTypeCompaction:
		if compaction, ok := asCompactionEntry(entry); ok {
			return compactionSummaryAgentMessage(compaction.Summary, compaction.TokensBefore, compaction.Timestamp), true
		}
	}
	return agenttypes.AgentMessage{}, false
}

// PrepareBranchEntries prepares branch entries for summarization within an
// optional token budget.
func PrepareBranchEntries(entries []harnesstypes.Entry, tokenBudget int) BranchPreparation {
	messages := []agenttypes.AgentMessage{}
	fileOps := CreateFileOps()
	totalTokens := 0

	for _, entry := range entries {
		if asEntryKind(entry) != harnesstypes.EntryTypeBranchSummary {
			continue
		}
		branch, ok := asBranchSummaryEntry(entry)
		if !ok {
			continue
		}
		readFiles, modifiedFiles := branchSummaryFileLists(branch.Details)
		for _, path := range readFiles {
			fileOps.Read = addUnique(fileOps.Read, path)
		}
		for _, path := range modifiedFiles {
			fileOps.Edited = addUnique(fileOps.Edited, path)
		}
	}

	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		message, ok := branchMessageFromEntry(entry)
		if !ok {
			continue
		}
		ExtractFileOpsFromMessage(message, &fileOps)
		tokens := EstimateTokens(message)
		if tokenBudget > 0 && totalTokens+tokens > tokenBudget {
			if asEntryKind(entry) == harnesstypes.EntryTypeCompaction || asEntryKind(entry) == harnesstypes.EntryTypeBranchSummary {
				if float64(totalTokens) < float64(tokenBudget)*0.9 {
					messages = prependMessage(messages, message)
					totalTokens += tokens
				}
			}
			break
		}
		messages = prependMessage(messages, message)
		totalTokens += tokens
	}

	return BranchPreparation{Messages: messages, FileOps: fileOps, TotalTokens: float64(totalTokens)}
}

func prependMessage(messages []agenttypes.AgentMessage, message agenttypes.AgentMessage) []agenttypes.AgentMessage {
	out := make([]agenttypes.AgentMessage, 0, len(messages)+1)
	out = append(out, message)
	out = append(out, messages...)
	return out
}

func branchSummaryFileLists(details any) (readFiles []string, modifiedFiles []string) {
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
	return stringList(parsed["readFiles"]), stringList(parsed["modifiedFiles"])
}

const branchSummaryPreamble = `The user explored a different conversation branch before returning here.
Summary of that exploration:

`

const branchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

Use this EXACT format:

## Goal
[What was the user trying to accomplish in this branch?]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Work that was started but not finished]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next to continue this work]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// GenerateBranchSummary generates a summary for abandoned branch entries.
func GenerateBranchSummary(
	entries []harnesstypes.Entry,
	options GenerateBranchSummaryOptions,
	context harnesstypes.Context,
) (harnesstypes.Result[BranchSummaryResult, *harnesstypes.BranchSummaryError], error) {
	model := options.Model
	reserveTokens := 16384
	if options.ReserveTokens != nil {
		reserveTokens = *options.ReserveTokens
	}
	contextWindow := model.ContextWindow
	if contextWindow == 0 {
		contextWindow = 128000
	}
	preparation := PrepareBranchEntries(entries, int(contextWindow)-reserveTokens)
	return GenerateBranchSummaryWithRequest(
		preparation,
		PreparedBranchSummaryOptions{
			Model:               &model,
			CustomInstructions:  options.CustomInstructions,
			ReplaceInstructions: options.ReplaceInstructions,
		},
		func(aiContext aitypes.Context, requestOptions aitypes.SimpleStreamOptions, requestContext harnesstypes.Context) (aitypes.AssistantMessage, error) {
			return CompleteSimpleWithRetries(options.Models, model, aiContext, requestOptions, options.Retry, options.Callbacks, requestContext)
		},
		context,
	)
}

// PreparedBranchSummaryOptions are the prepared branch summary options.
type PreparedBranchSummaryOptions struct {
	// Model supplies the actual context/output budgets. Nil keeps the legacy
	// 128000-context fallback for callers owning the request boundary.
	Model               *aitypes.Model
	CustomInstructions  *string
	ReplaceInstructions *bool
}

// GenerateBranchSummaryWithRequest generates a prepared branch summary through
// a caller-owned one-request boundary.
func GenerateBranchSummaryWithRequest(
	preparation BranchPreparation,
	options PreparedBranchSummaryOptions,
	request SummaryRequest,
	context harnesstypes.Context,
) (harnesstypes.Result[BranchSummaryResult, *harnesstypes.BranchSummaryError], error) {
	messages := preparation.Messages
	if len(messages) == 0 {
		return harnesstypes.Ok[BranchSummaryResult, *harnesstypes.BranchSummaryError](BranchSummaryResult{
			Summary:       "No content to summarize",
			ReadFiles:     []string{},
			ModifiedFiles: []string{},
		}), nil
	}
	llmMessages, err := harnessmessages.ConvertToLlm(messages)
	if err != nil {
		return harnesstypes.Result[BranchSummaryResult, *harnesstypes.BranchSummaryError]{}, err
	}
	conversationText := SerializeConversation(llmMessages)
	instructions := branchSummaryPrompt
	if options.ReplaceInstructions != nil && *options.ReplaceInstructions && options.CustomInstructions != nil {
		instructions = *options.CustomInstructions
	} else if options.CustomInstructions != nil && *options.CustomInstructions != "" {
		instructions = branchSummaryPrompt + "\n\nAdditional focus: " + *options.CustomInstructions
	}
	contextWindow := float64(128000)
	maxTokens := 2048
	if options.Model != nil {
		contextWindow = options.Model.ContextWindow
		maxTokens = SummaryOutputTokenLimit(*options.Model, maxTokens)
	}
	promptText, err := BuildSummaryPrompt(conversationText, instructions, contextWindow, maxTokens)
	if err != nil {
		return harnesstypes.Result[BranchSummaryResult, *harnesstypes.BranchSummaryError]{}, err
	}

	summarizationMessages := []aitypes.Message{
		aitypes.NewUserMessageVariant(aitypes.NewUserMessageBlocks(
			[]aitypes.ContentBlock{aitypes.TextBlock(promptText)},
			currentTimeMillis(),
		)),
	}
	systemPrompt := SummarizationSystemPrompt
	response, err := request(
		aitypes.Context{SystemPrompt: &systemPrompt, Messages: summarizationMessages},
		CreateSummaryRequestOptions(aitypes.SimpleStreamOptions{StreamOptions: aitypes.StreamOptions{MaxTokens: &maxTokens}}, context),
		context,
	)
	if err != nil {
		return harnesstypes.Result[BranchSummaryResult, *harnesstypes.BranchSummaryError]{}, err
	}
	if response.StopReason == aitypes.StopReasonAborted {
		return harnesstypes.Err[BranchSummaryResult, *harnesstypes.BranchSummaryError](
			harnesstypes.NewBranchSummaryError(harnesstypes.BranchSummaryErrorAborted, errorMessageOr(response, "Branch summary aborted"), nil),
		), nil
	}
	if response.StopReason == aitypes.StopReasonError {
		return harnesstypes.Err[BranchSummaryResult, *harnesstypes.BranchSummaryError](
			harnesstypes.NewBranchSummaryError(
				harnesstypes.BranchSummaryErrorSummarizationFailed,
				"Branch summary failed: "+errorMessageOr(response, "Unknown error"),
				nil,
			),
		), nil
	}

	summary := aiutils.ContentText(response.Content)
	summary = branchSummaryPreamble + summary
	readFiles, modifiedFiles := ComputeFileLists(preparation.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)
	if summary == "" {
		summary = "No summary generated"
	}
	usage := response.Usage
	return harnesstypes.Ok[BranchSummaryResult, *harnesstypes.BranchSummaryError](BranchSummaryResult{
		Summary:       summary,
		Usage:         &usage,
		ReadFiles:     readFiles,
		ModifiedFiles: modifiedFiles,
	}), nil
}

// entryBaseOf extracts the shared entry header from a concrete entry.
func entryBaseOf(entry harnesstypes.Entry) (harnesstypes.EntryBase, bool) {
	switch typed := entry.(type) {
	case harnesstypes.MessageEntry:
		return typed.EntryBase, true
	case *harnesstypes.MessageEntry:
		return typed.EntryBase, true
	case harnesstypes.CompactionEntry:
		return typed.EntryBase, true
	case *harnesstypes.CompactionEntry:
		return typed.EntryBase, true
	case harnesstypes.BranchSummaryEntry:
		return typed.EntryBase, true
	case *harnesstypes.BranchSummaryEntry:
		return typed.EntryBase, true
	case harnesstypes.CustomEntry:
		return typed.EntryBase, true
	case *harnesstypes.CustomEntry:
		return typed.EntryBase, true
	default:
		return harnesstypes.EntryBase{}, false
	}
}
