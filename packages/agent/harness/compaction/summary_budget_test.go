package compaction

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSummaryExcerptsKeepUnicodeHeadTailWithinBytes(t *testing.T) {
	input := "HEAD\n" + strings.Repeat("中文🙂", 8000) + "\nERROR: final diagnostic"
	got := truncateForSummary(input, ToolResultMaxBytes)
	if !utf8.ValidString(got) || len(got) > 32<<10 || !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "ERROR: final diagnostic") || !strings.Contains(got, "middle omitted") {
		t.Fatal("summary excerpt lost boundary, tail or budget")
	}
	if truncateForSummary("untouched", 32<<10) != "untouched" {
		t.Fatal("small result changed")
	}
}

func TestSummaryOverallBudgetKeepsInstructionsAndStoredInput(t *testing.T) {
	input := "GOAL\n" + strings.Repeat("tool result\n", 100_000) + "\nLAST FAILURE"
	suffix := "<previous-summary>Important decisions</previous-summary>\nSummarize this task."
	prompt, err := BuildSummaryPrompt(input, suffix, 8000, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if (len(prompt)+len(SummarizationSystemPrompt)+3)/4+1024 > 8000 || !strings.HasSuffix(prompt, suffix) || !strings.Contains(prompt, "GOAL") || !strings.Contains(prompt, "LAST FAILURE") {
		t.Fatal("overall budget lost instructions or exceeded context estimate")
	}
	if !strings.HasSuffix(input, "LAST FAILURE") || !strings.Contains(input, strings.Repeat("tool result\n", 1000)) {
		t.Fatal("stored input changed")
	}
	if _, err := BuildSummaryPrompt(input, strings.Repeat("x", 40000), 8000, 1024); err == nil {
		t.Fatal("oversized instructions were accepted")
	}
}

func TestSummaryKeepsDiagnosticInOmittedMiddle(t *testing.T) {
	input := strings.Repeat("first\n", 10000) + "FATAL: preserve this failure\n" + strings.Repeat("last\n", 10000)
	got := truncateForSummary(input, ToolResultMaxBytes)
	if len(got) > ToolResultMaxBytes || !strings.Contains(got, "FATAL: preserve this failure") {
		t.Fatal("middle failure lost from summary excerpt")
	}
}

func TestSummarySmallBudgetsStillFitUnicodeAndDiagnostics(t *testing.T) {
	input := strings.Repeat("中文🙂\n", 100) + "ERROR: details\n" + strings.Repeat("尾部🙂\n", 100)
	for budget := 0; budget <= 512; budget++ {
		got := truncateForSummary(input, budget)
		if len(got) > budget || !utf8.ValidString(got) {
			t.Fatalf("invalid summary at budget %d: %d bytes", budget, len(got))
		}
	}
}
