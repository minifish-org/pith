package compaction

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	harnessmessages "github.com/minifish-org/pith/packages/agent/harness/messages"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// This file translates the upstream harness compaction and branch-summarization
// tests into deterministic Go self-tests. Provider/model interaction is
// exercised through the caller-owned SummaryRequest boundary instead of live
// credentials, which is the recorded limitation for this port.

func usageOf(input, output, cacheRead, cacheWrite float64) aitypes.Usage {
	return aitypes.Usage{
		Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite,
		TotalTokens: input + output + cacheRead + cacheWrite,
	}
}

func userMessageOf(text string) agenttypes.AgentMessage {
	return agenttypes.NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage(text, 1)))
}

func assistantMessageOf(text string, usage aitypes.Usage, stopReason aitypes.StopReason) agenttypes.AgentMessage {
	message := aitypes.NewAssistantMessage("anthropic-messages", "anthropic", "claude-sonnet-4-5", 1)
	message.Content = []aitypes.ContentBlock{aitypes.TextBlock(text)}
	message.Usage = usage
	message.StopReason = stopReason
	return agenttypes.NewAgentMessageFromMessage(aitypes.NewAssistantMessageVariant(message))
}

func messageEntryOf(id string, parent *string, message agenttypes.AgentMessage, seq int) harnesstypes.MessageEntry {
	return harnesstypes.MessageEntry{
		EntryBase: harnesstypes.EntryBase{
			ID: id, ParentID: parent, Seq: seq, Timestamp: float64(seq), Type: harnesstypes.EntryTypeMessage,
		},
		Message: message,
	}
}

func customEntryOf(id string, parent *string, seq int) harnesstypes.CustomEntry {
	return harnesstypes.CustomEntry{
		EntryBase: harnesstypes.EntryBase{
			ID: id, ParentID: parent, Seq: seq, Timestamp: float64(seq), Type: harnesstypes.EntryTypeCustom,
		},
		CustomType: "test",
	}
}

func compactionEntryOf(id string, parent *string, summary string, seq int, retained []agenttypes.AgentMessage, details any) harnesstypes.CompactionEntry {
	return harnesstypes.CompactionEntry{
		EntryBase: harnesstypes.EntryBase{
			ID: id, ParentID: parent, Seq: seq, Timestamp: float64(seq), Type: harnesstypes.EntryTypeCompaction,
		},
		Summary: summary, TokensBefore: 1234, RetainedTail: retained, Details: details,
	}
}

func branchSummaryEntryOf(id string, parent *string, seq int, summary string) harnesstypes.BranchSummaryEntry {
	return harnesstypes.BranchSummaryEntry{
		EntryBase: harnesstypes.EntryBase{
			ID: id, ParentID: parent, Seq: seq, Timestamp: float64(seq), Type: harnesstypes.EntryTypeBranchSummary,
		},
		Summary: summary,
	}
}

func strPtr(value string) *string { return &value }

func TestCompactionCalculateContextTokens(t *testing.T) {
	if got := CalculateContextTokens(usageOf(1000, 500, 200, 100)); got != 1800 {
		t.Fatalf("calculateContextTokens: want 1800, got %v", got)
	}
	if got := CalculateContextTokens(usageOf(0, 0, 0, 0)); got != 0 {
		t.Fatalf("calculateContextTokens empty: want 0, got %v", got)
	}
	withTotal := usageOf(1, 2, 3, 4)
	withTotal.TotalTokens = 42
	if got := CalculateContextTokens(withTotal); got != 42 {
		t.Fatalf("calculateContextTokens total: want 42, got %v", got)
	}
}

func TestCompactionShouldCompact(t *testing.T) {
	settings := CompactionSettings{Enabled: true, ReserveTokens: 10000, KeepRecentTokens: 20000}
	if !ShouldCompact(95000, 100000, settings) {
		t.Fatal("expected compaction when usage exceeds reserve")
	}
	if ShouldCompact(89000, 100000, settings) {
		t.Fatal("did not expect compaction below reserve")
	}
	disabled := settings
	disabled.Enabled = false
	if ShouldCompact(95000, 100000, disabled) {
		t.Fatal("disabled compaction must never compact")
	}
}

func TestCompactionEstimateTokens(t *testing.T) {
	if got := EstimateTokens(userMessageOf("hello")); got != 2 {
		t.Fatalf("user estimate: want 2, got %d", got)
	}
	assistant := aitypes.NewAssistantMessage("openai-completions", "fixture", "fixture", 1)
	assistant.Content = []aitypes.ContentBlock{
		aitypes.ThinkingBlock("中文"),
		aitypes.TextBlock("answer"),
	}
	if got := EstimateTokens(agenttypes.NewAgentMessageFromMessage(aitypes.NewAssistantMessageVariant(assistant))); got != 2 {
		t.Fatalf("assistant estimate: want 2, got %d", got)
	}
	unknown := agenttypes.NewCustomMessage("unknown", json.RawMessage(`{"role":"unknown","timestamp":1}`))
	if got := EstimateTokens(unknown); got != 0 {
		t.Fatalf("unknown role estimate: want 0, got %d", got)
	}
	image := agenttypes.NewAgentMessageFromMessage(aitypes.NewToolResultMessageVariant(aitypes.NewToolResultMessage(
		"call-1", "read",
		[]aitypes.ContentBlock{
			aitypes.TextBlock("tool text"),
			aitypes.ImageBlock("abc", "image/png"),
		},
		false, 1,
	)))
	if got := EstimateTokens(image); got <= 1000 {
		t.Fatalf("image estimate: want > 1000, got %d", got)
	}
}

func TestCompactionGetLastAssistantUsage(t *testing.T) {
	usage := usageOf(10, 5, 3, 2)
	entries := []harnesstypes.Entry{
		messageEntryOf("u1", nil, userMessageOf("user"), 1),
		messageEntryOf("a1", strPtr("u1"), assistantMessageOf("assistant", usage, aitypes.StopReasonStop), 2),
	}
	got := GetLastAssistantUsage(entries)
	if got == nil || CalculateContextTokens(*got) != 20 {
		t.Fatalf("last assistant usage: want 20 tokens, got %+v", got)
	}
	aborted := []harnesstypes.Entry{
		messageEntryOf("a1", nil, assistantMessageOf("aborted", usage, aitypes.StopReasonAborted), 1),
		messageEntryOf("a2", nil, assistantMessageOf("error", usage, aitypes.StopReasonError), 2),
	}
	if got := GetLastAssistantUsage(aborted); got != nil {
		t.Fatalf("aborted/error usage must be ignored, got %+v", got)
	}
	partial := []harnesstypes.Entry{
		messageEntryOf("a1", nil, assistantMessageOf("partial", usageOf(0, 0, 0, 0), aitypes.StopReasonStop), 1),
	}
	if got := GetLastAssistantUsage(partial); got != nil {
		t.Fatalf("zero usage must be ignored, got %+v", got)
	}
}

func TestCompactionEstimateContextTokens(t *testing.T) {
	assistant := assistantMessageOf("assistant", usageOf(10, 5, 3, 2), aitypes.StopReasonStop)
	estimate := EstimateContextTokens([]agenttypes.AgentMessage{
		userMessageOf("Hello"),
		assistant,
		userMessageOf("continue"),
		assistantMessageOf("Partial thinking", usageOf(0, 0, 0, 0), aitypes.StopReasonStop),
	})
	if estimate.UsageTokens != 20 {
		t.Fatalf("usage tokens: want 20, got %v", estimate.UsageTokens)
	}
	if estimate.LastUsageIndex == nil || *estimate.LastUsageIndex != 1 {
		t.Fatalf("last usage index: want 1, got %v", estimate.LastUsageIndex)
	}
	if estimate.TrailingTokens <= 0 {
		t.Fatalf("trailing tokens: want > 0, got %v", estimate.TrailingTokens)
	}
	if estimate.Tokens != 20+estimate.TrailingTokens {
		t.Fatalf("total tokens mismatch: %v", estimate.Tokens)
	}
	noUsage := EstimateContextTokens([]agenttypes.AgentMessage{userMessageOf("no usage")})
	if noUsage.LastUsageIndex != nil {
		t.Fatalf("no-usage index: want nil, got %v", *noUsage.LastUsageIndex)
	}
}

func TestCompactionFileOperations(t *testing.T) {
	fileOps := CreateFileOps()
	fileOps.Read = append(fileOps.Read, "a.txt", "中.txt", "b.txt")
	fileOps.Written = append(fileOps.Written, "b.txt")
	readFiles, modifiedFiles := ComputeFileLists(fileOps)
	if strings.Join(readFiles, ",") != "a.txt,中.txt" {
		t.Fatalf("read files: %v", readFiles)
	}
	if strings.Join(modifiedFiles, ",") != "b.txt" {
		t.Fatalf("modified files: %v", modifiedFiles)
	}
	formatted := FormatFileOperations([]string{"a.txt", "中.txt"}, []string{"b.txt"})
	want := "\n\n<read-files>\na.txt\n中.txt\n</read-files>\n\n<modified-files>\nb.txt\n</modified-files>"
	if formatted != want {
		t.Fatalf("formatFileOperations: want %q, got %q", want, formatted)
	}
	if got := FormatFileOperations(nil, nil); got != "" {
		t.Fatalf("empty formatFileOperations: want empty, got %q", got)
	}
}

func TestCompactionSerializeConversation(t *testing.T) {
	longContent := strings.Repeat("x", 5000)
	messages := []aitypes.Message{
		aitypes.NewToolResultMessageVariant(aitypes.NewToolResultMessage(
			"tc1", "read", []aitypes.ContentBlock{aitypes.TextBlock(longContent)}, false, 1,
		)),
	}
	result := SerializeConversation(messages)
	if !strings.Contains(result, "[Tool result]:") {
		t.Fatalf("serializeConversation missing tool result: %q", result)
	}
	if !strings.Contains(result, "[... 3000 more characters truncated]") {
		t.Fatalf("serializeConversation missing truncation marker: %q", result)
	}
}

func TestCompactionFindCutPoint(t *testing.T) {
	entries := []harnesstypes.Entry{}
	var parent *string
	for index := 0; index < 10; index++ {
		user := messageEntryOf("u"+itoaTest(index), parent, userMessageOf("User "+itoaTest(index)), index*2)
		userID := user.ID
		entries = append(entries, user)
		assistant := messageEntryOf(
			"a"+itoaTest(index), &userID,
			assistantMessageOf("Assistant "+itoaTest(index), usageOf(0, 100, float64((index+1)*1000), 0), aitypes.StopReasonStop),
			index*2+1,
		)
		entries = append(entries, assistant)
		parent = &assistant.ID
	}
	result := FindCutPoint(entries, 0, len(entries), 2500)
	if asEntryKind(entries[result.FirstKeptEntryIndex]) != harnesstypes.EntryTypeMessage {
		t.Fatalf("cut point entry type: %v", asEntryKind(entries[result.FirstKeptEntryIndex]))
	}

	firstCustom := customEntryOf("first", nil, 1)
	secondCustom := customEntryOf("second", strPtr(firstCustom.ID), 2)
	if got := FindCutPoint([]harnesstypes.Entry{firstCustom, secondCustom}, 0, 2, 1); got != (CutPointResult{FirstKeptEntryIndex: 0, TurnStartIndex: -1, IsSplitTurn: false}) {
		t.Fatalf("custom-only cut point: %+v", got)
	}
	branchSummary := branchSummaryEntryOf("branch", strPtr(secondCustom.ID), 3, "branch summary")
	if got := FindTurnStartIndex([]harnesstypes.Entry{firstCustom, branchSummary}, 1, 0); got != 1 {
		t.Fatalf("turn start on branch summary: %d", got)
	}
	if got := FindTurnStartIndex([]harnesstypes.Entry{firstCustom, secondCustom}, 1, 0); got != -1 {
		t.Fatalf("turn start without user: %d", got)
	}
	if got := FindCutPoint([]harnesstypes.Entry{firstCustom, branchSummary}, 0, 2, 1).FirstKeptEntryIndex; got != 0 {
		t.Fatalf("branch summary cut point: %d", got)
	}
	toolResult := messageEntryOf("tr", nil, agenttypes.NewAgentMessageFromMessage(aitypes.NewToolResultMessageVariant(
		aitypes.NewToolResultMessage("call-1", "read", []aitypes.ContentBlock{aitypes.TextBlock("tool output")}, false, 1),
	)), 1)
	if got := FindCutPoint([]harnesstypes.Entry{toolResult}, 0, 1, 1); got != (CutPointResult{FirstKeptEntryIndex: 0, TurnStartIndex: -1, IsSplitTurn: false}) {
		t.Fatalf("tool-result-only cut point: %+v", got)
	}
	user := messageEntryOf("user", nil, userMessageOf("user"), 1)
	compactionEntry := compactionEntryOf("compaction", strPtr(user.ID), "summary", 2, nil, nil)
	assistant := messageEntryOf("assistant", strPtr(compactionEntry.ID), assistantMessageOf("assistant", usageOf(0, 0, 0, 0), aitypes.StopReasonStop), 3)
	if got := FindCutPoint([]harnesstypes.Entry{user, compactionEntry, assistant}, 0, 3, 1).FirstKeptEntryIndex; got != 2 {
		t.Fatalf("compaction boundary cut point: %d", got)
	}
}

func TestCompactionPrepareCompaction(t *testing.T) {
	u1 := messageEntryOf("u1", nil, userMessageOf("user msg 1"), 1)
	a1 := messageEntryOf("a1", strPtr("u1"), assistantMessageOf("assistant msg 1", usageOf(0, 0, 0, 0), aitypes.StopReasonStop), 2)
	u2 := messageEntryOf("u2", strPtr("a1"), userMessageOf("user msg 2"), 3)
	a2 := messageEntryOf("a2", strPtr("u2"), assistantMessageOf("assistant msg 2", usageOf(5000, 1000, 0, 0), aitypes.StopReasonStop), 4)
	compaction1 := compactionEntryOf("c1", strPtr("a2"), "First summary", 5, nil, nil)
	u3 := messageEntryOf("u3", strPtr("c1"), userMessageOf("user msg 3"), 6)
	a3 := messageEntryOf("a3", strPtr("u3"), assistantMessageOf("assistant msg 3", usageOf(8000, 2000, 0, 0), aitypes.StopReasonStop), 7)
	pathEntries := []harnesstypes.Entry{u1, a1, u2, a2, compaction1, u3, a3}

	result, err := PrepareCompaction(pathEntries, DefaultCompactionSettings)
	if err != nil {
		t.Fatalf("prepareCompaction: %v", err)
	}
	if !result.OK || result.Value == nil {
		t.Fatalf("expected a preparation, got %+v", result)
	}
	if result.Value.PreviousSummary == nil || *result.Value.PreviousSummary != "First summary" {
		t.Fatalf("previous summary: %+v", result.Value.PreviousSummary)
	}
	if len(result.Value.RetainedTail) == 0 {
		t.Fatal("expected a non-empty retained tail")
	}
	if result.Value.TokensBefore <= 0 {
		t.Fatalf("tokens before: %v", result.Value.TokensBefore)
	}

	if result, _ := PrepareCompaction([]harnesstypes.Entry{compactionEntryOf("c", nil, "already compacted", 1, nil, nil)}, DefaultCompactionSettings); !result.OK || result.Value != nil {
		t.Fatalf("compaction-only path must not prepare: %+v", result)
	}
	if result, _ := PrepareCompaction(nil, DefaultCompactionSettings); !result.OK || result.Value != nil {
		t.Fatalf("empty path must not prepare: %+v", result)
	}
}

func TestCompactionCarriesRetainedTail(t *testing.T) {
	retainedUser := userMessageOf("retained user")
	retainedAssistant := assistantMessageOf("retained assistant", usageOf(0, 0, 0, 0), aitypes.StopReasonStop)
	compaction := compactionEntryOf("c1", nil, "previous summary", 1, []agenttypes.AgentMessage{retainedUser, retainedAssistant}, nil)
	user := messageEntryOf("u1", strPtr("c1"), userMessageOf("new user"), 2)
	assistant := messageEntryOf("a1", strPtr("u1"), assistantMessageOf("new assistant", usageOf(0, 0, 0, 0), aitypes.StopReasonStop), 3)

	result, err := PrepareCompaction(
		[]harnesstypes.Entry{compaction, user, assistant},
		CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 1},
	)
	if err != nil {
		t.Fatalf("prepareCompaction: %v", err)
	}
	if result.Value == nil || result.Value.PreviousSummary == nil || *result.Value.PreviousSummary != "previous summary" {
		t.Fatalf("previous summary: %+v", result.Value)
	}
	combined := []agenttypes.AgentMessage{}
	combined = append(combined, result.Value.MessagesToSummarize...)
	combined = append(combined, result.Value.TurnPrefixMessages...)
	combined = append(combined, result.Value.RetainedTail...)
	if len(combined) != 4 {
		t.Fatalf("combined messages: want 4, got %d", len(combined))
	}
	if agentMessageRole(combined[0]) != "user" || agentMessageRole(combined[1]) != "assistant" ||
		agentMessageRole(combined[2]) != "user" || agentMessageRole(combined[3]) != "assistant" {
		t.Fatalf("combined order: %v", []string{
			agentMessageRole(combined[0]), agentMessageRole(combined[1]),
			agentMessageRole(combined[2]), agentMessageRole(combined[3]),
		})
	}
}

func TestCompactionSplitTurnWithFileOperations(t *testing.T) {
	u1 := messageEntryOf("u1", nil, userMessageOf("user msg 1"), 1)
	assistant := aitypes.NewAssistantMessage("anthropic-messages", "anthropic", "claude", 1)
	assistant.Content = []aitypes.ContentBlock{
		aitypes.ToolCallBlock(aitypes.NewToolCall("tool-1", "write", json.RawMessage(`{"path":"written.ts"}`))),
	}
	assistant.StopReason = aitypes.StopReasonStop
	a1 := messageEntryOf("a1", strPtr("u1"), agenttypes.NewAgentMessageFromMessage(aitypes.NewAssistantMessageVariant(assistant)), 2)
	details := CompactionDetails{ReadFiles: []string{"old-read.ts"}, ModifiedFiles: []string{"old-edit.ts", "written.ts"}}
	compaction1 := compactionEntryOf("c1", strPtr("a1"), "First summary", 3, nil, details)
	u2 := messageEntryOf("u2", strPtr("c1"), userMessageOf("large turn"), 4)
	a2 := messageEntryOf("a2", strPtr("u2"), assistantMessageOf("large assistant message", usageOf(0, 0, 0, 0), aitypes.StopReasonStop), 5)

	result, err := PrepareCompaction(
		[]harnesstypes.Entry{u1, a1, compaction1, u2, a2},
		CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 1},
	)
	if err != nil {
		t.Fatalf("prepareCompaction: %v", err)
	}
	if result.Value == nil || !result.Value.IsSplitTurn {
		t.Fatalf("expected a split turn: %+v", result.Value)
	}
	if result.Value.PreviousSummary == nil || *result.Value.PreviousSummary != "First summary" {
		t.Fatalf("previous summary: %+v", result.Value.PreviousSummary)
	}
	if len(result.Value.TurnPrefixMessages) != 1 || agentMessageRole(result.Value.TurnPrefixMessages[0]) != "user" {
		t.Fatalf("turn prefix messages: %+v", result.Value.TurnPrefixMessages)
	}
	if !containsString(result.Value.FileOps.Read, "old-read.ts") {
		t.Fatalf("read file ops: %+v", result.Value.FileOps.Read)
	}
	if !containsString(result.Value.FileOps.Edited, "old-edit.ts") || !containsString(result.Value.FileOps.Edited, "written.ts") {
		t.Fatalf("edited file ops: %+v", result.Value.FileOps.Edited)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// stubRequest records requests and returns scripted assistant responses.
type stubRequest struct {
	responses []aitypes.AssistantMessage
	seen      []aitypes.SimpleStreamOptions
	prompts   []string
}

func (s *stubRequest) fn(aiContext aitypes.Context, options aitypes.SimpleStreamOptions, context harnesstypes.Context) (aitypes.AssistantMessage, error) {
	s.seen = append(s.seen, options)
	prompt := ""
	for _, message := range aiContext.Messages {
		if message.User != nil {
			prompt = contentTextOf(message.User.Content)
			break
		}
	}
	s.prompts = append(s.prompts, prompt)
	if len(s.responses) == 0 {
		return aitypes.AssistantMessage{}, nil
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func contentTextOf(content aitypes.UserContent) string {
	if !content.Structured {
		return content.Text
	}
	parts := []string{}
	for _, block := range content.Blocks {
		if block.Type == aitypes.ContentTypeText && block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, "")
}

func scriptedAssistant(text string, usage aitypes.Usage, stopReason aitypes.StopReason) aitypes.AssistantMessage {
	message := aitypes.NewAssistantMessage("openai-completions", "fixture", "fixture", 1)
	message.Content = []aitypes.ContentBlock{aitypes.TextBlock(text)}
	message.Usage = usage
	message.StopReason = stopReason
	return message
}

func TestCompactionGenerateSummaryPromptAndReasoning(t *testing.T) {
	messages := []agenttypes.AgentMessage{userMessageOf("Summarize this.")}

	reasoningModel := aitypes.Model{Id: "reasoning", Reasoning: true, MaxTokens: 8192}
	medium := agenttypes.ThinkingMedium
	stub := &stubRequest{responses: []aitypes.AssistantMessage{scriptedAssistant("## Goal\nTest summary", usageOf(1, 2, 3, 4), aitypes.StopReasonStop)}}
	result, err := GenerateSummaryWithRequest(
		messages,
		SummaryGenerationOptions{Model: reasoningModel, ReserveTokens: 2000, ThinkingLevel: &medium},
		stub.fn,
		contextOf(),
	)
	if err != nil {
		t.Fatalf("generateSummaryWithRequest: %v", err)
	}
	if !result.OK || result.Value.Text != "## Goal\nTest summary" {
		t.Fatalf("summary result: %+v", result)
	}
	if stub.seen[0].Reasoning == nil || *stub.seen[0].Reasoning != agenttypes.ThinkingMedium {
		t.Fatalf("reasoning passthrough: %+v", stub.seen[0].Reasoning)
	}
	if stub.seen[0].CacheRetention == nil || *stub.seen[0].CacheRetention != aitypes.CacheRetentionNone {
		t.Fatalf("cache retention: %+v", stub.seen[0].CacheRetention)
	}

	off := agenttypes.ThinkingLevel(agenttypes.ThinkingOff)
	stubOff := &stubRequest{responses: []aitypes.AssistantMessage{scriptedAssistant("summary", usageOf(0, 0, 0, 0), aitypes.StopReasonStop)}}
	if _, err := GenerateSummaryWithRequest(messages, SummaryGenerationOptions{Model: reasoningModel, ReserveTokens: 2000, ThinkingLevel: &off}, stubOff.fn, contextOf()); err != nil {
		t.Fatalf("off reasoning: %v", err)
	}
	if stubOff.seen[0].Reasoning != nil {
		t.Fatalf("off reasoning must not be forwarded: %+v", stubOff.seen[0].Reasoning)
	}

	custom := "focus"
	previous := "old summary"
	stubPrompt := &stubRequest{responses: []aitypes.AssistantMessage{scriptedAssistant("summary", usageOf(0, 0, 0, 0), aitypes.StopReasonStop)}}
	if _, err := GenerateSummaryWithRequest(
		messages,
		SummaryGenerationOptions{Model: aitypes.Model{Id: "plain", MaxTokens: 8192}, ReserveTokens: 2000, CustomInstructions: &custom, PreviousSummary: &previous},
		stubPrompt.fn,
		contextOf(),
	); err != nil {
		t.Fatalf("prompt generation: %v", err)
	}
	if !strings.Contains(stubPrompt.prompts[0], "<previous-summary>\nold summary\n</previous-summary>") {
		t.Fatalf("previous summary prompt: %q", stubPrompt.prompts[0])
	}
	if !strings.Contains(stubPrompt.prompts[0], "Additional focus: focus") {
		t.Fatalf("custom instructions prompt: %q", stubPrompt.prompts[0])
	}
}

func TestCompactionErrorResults(t *testing.T) {
	messages := []agenttypes.AgentMessage{userMessageOf("Summarize this.")}
	model := aitypes.Model{Id: "fixture", MaxTokens: 8192}

	errorStub := &stubRequest{responses: []aitypes.AssistantMessage{scriptedAssistant("", usageOf(0, 0, 0, 0), aitypes.StopReasonError)}}
	errorStub.responses[0].ErrorMessage = strPtr("boom")
	result, err := GenerateSummaryWithRequest(messages, SummaryGenerationOptions{Model: model, ReserveTokens: 2000}, errorStub.fn, contextOf())
	if err != nil {
		t.Fatalf("error generation: %v", err)
	}
	if result.OK || result.Error.Code != harnesstypes.CompactionErrorSummarizationFailed || result.Error.Message != "Summarization failed: boom" {
		t.Fatalf("error result: %+v", result)
	}

	abortedStub := &stubRequest{responses: []aitypes.AssistantMessage{scriptedAssistant("", usageOf(0, 0, 0, 0), aitypes.StopReasonAborted)}}
	abortedStub.responses[0].ErrorMessage = strPtr("stopped")
	aborted, err := GenerateSummaryWithRequest(messages, SummaryGenerationOptions{Model: model, ReserveTokens: 2000}, abortedStub.fn, contextOf())
	if err != nil {
		t.Fatalf("aborted generation: %v", err)
	}
	if aborted.OK || aborted.Error.Code != harnesstypes.CompactionErrorAborted || aborted.Error.Message != "stopped" {
		t.Fatalf("aborted result: %+v", aborted)
	}
}

func TestCompactionSplitTurnUsageAndErrors(t *testing.T) {
	messages := []agenttypes.AgentMessage{userMessageOf("Summarize this.")}
	model := aitypes.Model{Id: "fixture", MaxTokens: 8192}
	preparation := CompactionPreparation{
		MessagesToSummarize: messages,
		TurnPrefixMessages:  messages,
		RetainedTail:        messages,
		IsSplitTurn:         true,
		TokensBefore:        100,
		FileOps:             CreateFileOps(),
		Settings:            CompactionSettings{Enabled: true, ReserveTokens: 2000, KeepRecentTokens: 20},
	}
	stub := &stubRequest{responses: []aitypes.AssistantMessage{
		scriptedAssistant("history summary", usageOf(1, 2, 3, 4), aitypes.StopReasonStop),
		scriptedAssistant("turn prefix summary", usageOf(5, 6, 7, 8), aitypes.StopReasonStop),
	}}
	result, err := CompactWithRequest(preparation, CompactGenerationOptions{Model: model}, stub.fn, contextOf())
	if err != nil {
		t.Fatalf("compactWithRequest: %v", err)
	}
	if !result.OK {
		t.Fatalf("compact result: %+v", result)
	}
	if result.Value.Usage == nil || CalculateContextTokens(*result.Value.Usage) != 36 {
		t.Fatalf("combined usage: %+v", result.Value.Usage)
	}

	historyError := &stubRequest{responses: []aitypes.AssistantMessage{scriptedAssistant("", usageOf(0, 0, 0, 0), aitypes.StopReasonError)}}
	historyError.responses[0].ErrorMessage = strPtr("history failed")
	failed, err := CompactWithRequest(preparation, CompactGenerationOptions{Model: model}, historyError.fn, contextOf())
	if err != nil {
		t.Fatalf("history error: %v", err)
	}
	if failed.OK || failed.Error.Code != harnesstypes.CompactionErrorSummarizationFailed || failed.Error.Message != "Summarization failed: history failed" {
		t.Fatalf("history failure result: %+v", failed)
	}

	prefixPreparation := preparation
	prefixPreparation.MessagesToSummarize = nil
	prefixError := &stubRequest{responses: []aitypes.AssistantMessage{scriptedAssistant("", usageOf(0, 0, 0, 0), aitypes.StopReasonError)}}
	prefixError.responses[0].ErrorMessage = strPtr("prefix failed")
	prefixFailed, err := CompactWithRequest(prefixPreparation, CompactGenerationOptions{Model: model}, prefixError.fn, contextOf())
	if err != nil {
		t.Fatalf("prefix error: %v", err)
	}
	if prefixFailed.OK || prefixFailed.Error.Message != "Turn prefix summarization failed: prefix failed" {
		t.Fatalf("prefix failure result: %+v", prefixFailed)
	}
}

// fakeBranch and fakeSession are the deterministic branch readers used by the
// branch collection test, replacing live session storage.
type fakeBranch struct{ byID map[string]harnesstypes.Entry }

func (b fakeBranch) FindEntries(query *harnesstypes.BranchScan, context harnesstypes.Context) ([]harnesstypes.Entry, error) {
	path := []harnesstypes.Entry{}
	var current *string
	if query != nil {
		current = query.Start
	}
	for current != nil {
		entry, ok := b.byID[*current]
		if !ok {
			return nil, &missingEntryError{id: *current}
		}
		path = append(path, entry)
		base, _ := entryBaseOf(entry)
		current = base.ParentID
	}
	return path, nil
}

type fakeSession struct{ byID map[string]harnesstypes.Entry }

func (s fakeSession) GetEntry(id string, context harnesstypes.Context) (harnesstypes.Entry, bool, error) {
	entry, ok := s.byID[id]
	return entry, ok, nil
}

func TestBranchCollectEntries(t *testing.T) {
	root := messageEntryOf("root", nil, userMessageOf("root"), 1)
	common := messageEntryOf("common", strPtr("root"), userMessageOf("common"), 2)
	abandoned1 := messageEntryOf("abandoned-1", strPtr("common"), userMessageOf("abandoned 1"), 3)
	abandoned2 := messageEntryOf("abandoned-2", strPtr("abandoned-1"), userMessageOf("abandoned 2"), 4)
	target := messageEntryOf("target", strPtr("common"), userMessageOf("target"), 5)
	entries := []harnesstypes.Entry{root, common, abandoned1, abandoned2, target}
	byID := map[string]harnesstypes.Entry{}
	for _, entry := range entries {
		base, _ := entryBaseOf(entry)
		byID[base.ID] = entry
	}
	branch := fakeBranch{byID: byID}
	sessionReader := fakeSession{byID: byID}

	result, err := CollectEntriesForBranchSummary(branch, sessionReader, strPtr("abandoned-2"), "target", contextOf())
	if err != nil {
		t.Fatalf("collectEntriesForBranchSummary: %v", err)
	}
	if result.CommonAncestorID == nil || *result.CommonAncestorID != "common" {
		t.Fatalf("common ancestor: %+v", result.CommonAncestorID)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries: want 2, got %d", len(result.Entries))
	}
	first, _ := entryBaseOf(result.Entries[0])
	second, _ := entryBaseOf(result.Entries[1])
	if first.ID != "abandoned-1" || second.ID != "abandoned-2" {
		t.Fatalf("entry order: %s, %s", first.ID, second.ID)
	}

	empty, err := CollectEntriesForBranchSummary(branch, sessionReader, nil, "target", contextOf())
	if err != nil {
		t.Fatalf("no previous leaf: %v", err)
	}
	if len(empty.Entries) != 0 || empty.CommonAncestorID != nil {
		t.Fatalf("no previous leaf result: %+v", empty)
	}
}

func TestBranchPrepareEntries(t *testing.T) {
	details := BranchSummaryDetails{ReadFiles: []string{"read.ts"}, ModifiedFiles: []string{"edit.ts"}}
	compaction := harnesstypes.BranchSummaryEntry{
		EntryBase: harnesstypes.EntryBase{ID: "c1", Seq: 1, Timestamp: 1, Type: harnesstypes.EntryTypeBranchSummary},
		Summary:   "previous", Details: details,
	}
	entries := []harnesstypes.Entry{
		compaction,
		messageEntryOf("u1", nil, userMessageOf("user"), 2),
		messageEntryOf("tr", strPtr("u1"), agenttypes.NewAgentMessageFromMessage(aitypes.NewToolResultMessageVariant(
			aitypes.NewToolResultMessage("c", "read", []aitypes.ContentBlock{aitypes.TextBlock("result")}, false, 3),
		)), 3),
	}
	result := PrepareBranchEntries(entries, 0)
	if len(result.Messages) != 2 {
		t.Fatalf("branch messages: want 2 (tool result filtered), got %d", len(result.Messages))
	}
	if !containsString(result.FileOps.Read, "read.ts") || !containsString(result.FileOps.Edited, "edit.ts") {
		t.Fatalf("branch file ops: %+v", result.FileOps)
	}
	if result.TotalTokens <= 0 {
		t.Fatalf("branch total tokens: %v", result.TotalTokens)
	}
}

func TestCompactionSummarizationSystemPrompt(t *testing.T) {
	if !strings.Contains(SummarizationSystemPrompt, "context summarization assistant") {
		t.Fatal("summarization system prompt is missing its role description")
	}
	if harnessmessages.CompactionSummaryRole == "" {
		t.Fatal("expected a compaction summary role")
	}
}

func itoaTest(value int) string {
	return strconv.Itoa(value)
}

func contextOf() harnesstypes.Context {
	return context.Background()
}
