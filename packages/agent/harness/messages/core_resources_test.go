package harnessmessages

import (
	"encoding/json"
	"strings"
	"testing"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestBashExecutionToText(t *testing.T) {
	exit := 1
	fullPath := "/tmp/out.txt"
	base := BashExecutionMessage{Role: BashExecutionRole, Command: "ls", Output: "file.txt", ExitCode: &exit, Truncated: true, FullOutputPath: &fullPath}
	got := BashExecutionToText(base)
	want := "Ran `ls`\n```\nfile.txt\n```\n\nCommand exited with code 1\n\n[Output truncated. Full output: /tmp/out.txt]"
	if got != want {
		t.Fatalf("BashExecutionToText mismatch:\n got %q\nwant %q", got, want)
	}

	noOutput := BashExecutionToText(BashExecutionMessage{Command: "true"})
	if noOutput != "Ran `true`\n(no output)" {
		t.Fatalf("no-output rendering mismatch: %q", noOutput)
	}
	cancelled := BashExecutionToText(BashExecutionMessage{Command: "sleep", Cancelled: true})
	if !strings.HasSuffix(cancelled, "(command cancelled)") {
		t.Fatalf("cancelled rendering mismatch: %q", cancelled)
	}
}

func TestCreateSummaryAndCustomMessages(t *testing.T) {
	fromID := "entry-1"
	summary := CreateBranchSummaryMessage("branch text", &fromID, TimestampFromNumber(42))
	if summary.Role != BranchSummaryRole || summary.FromID == nil || *summary.FromID != "entry-1" || summary.Timestamp != 42 {
		t.Fatalf("unexpected branch summary: %#v", summary)
	}
	compaction := CreateCompactionSummaryMessage("compacted", 100, TimestampFromNumber(7))
	if compaction.Role != CompactionSummaryRole || compaction.TokensBefore != 100 || compaction.Timestamp != 7 {
		t.Fatalf("unexpected compaction summary: %#v", compaction)
	}
	custom := CreateCustomMessage("note", aitypes.UserContentText("hello"), true, json.RawMessage(`{"k":1}`), TimestampFromString("1970-01-01T00:00:01Z"))
	if custom.Role != CustomRole || custom.Timestamp != 1000 {
		t.Fatalf("unexpected custom message: %#v", custom)
	}
}

func TestConvertToLlm(t *testing.T) {
	branch := CreateBranchSummaryMessage("branch text", nil, TimestampFromNumber(1))
	branchRaw, _ := json.Marshal(branch)
	compaction := CreateCompactionSummaryMessage("compacted", 10, TimestampFromNumber(2))
	compactionRaw, _ := json.Marshal(compaction)
	excluded := true
	bashRaw, _ := json.Marshal(BashExecutionMessage{Role: BashExecutionRole, Command: "ls", Output: "x", ExcludeFromContext: &excluded, Timestamp: 3})
	visibleRaw, _ := json.Marshal(BashExecutionMessage{Role: BashExecutionRole, Command: "pwd", Output: "/", Timestamp: 4})
	customRaw, _ := json.Marshal(CreateCustomMessage("note", aitypes.UserContentText("custom"), true, nil, TimestampFromNumber(5)))

	messages := []agenttypes.AgentMessage{
		agenttypes.NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("hi", 0))),
		agenttypes.NewCustomMessage(BashExecutionRole, bashRaw),
		agenttypes.NewCustomMessage(BashExecutionRole, visibleRaw),
		agenttypes.NewCustomMessage(CustomRole, customRaw),
		agenttypes.NewCustomMessage(BranchSummaryRole, branchRaw),
		agenttypes.NewCustomMessage(CompactionSummaryRole, compactionRaw),
		agenttypes.NewCustomMessage("unknown", json.RawMessage(`{"role":"unknown"}`)),
	}
	converted, err := ConvertToLlm(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(converted) != 5 {
		t.Fatalf("converted %d messages, want 5", len(converted))
	}
	for _, message := range converted {
		if message.Role != aitypes.UserMessageRole {
			t.Fatalf("harness messages must convert to user messages: %#v", message)
		}
	}
	first := converted[1]
	if first.User == nil || len(first.User.Content.Blocks) != 1 || first.User.Content.Blocks[0].Text == nil || !strings.Contains(first.User.Content.Blocks[0].Text.Text, "Ran `pwd`") {
		t.Fatalf("unexpected bash conversion: %#v", first)
	}
	branchText := converted[3].User.Content.Blocks[0].Text.Text
	if !strings.Contains(branchText, BranchSummaryPrefix) || !strings.Contains(branchText, "branch text") || !strings.Contains(branchText, BranchSummarySuffix) {
		t.Fatalf("unexpected branch conversion: %q", branchText)
	}
}
