package agentruntime

import (
	"encoding/json"
	"testing"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// These self-tests translate the upstream reducer, retry and transcript
// scenarios from the pinned runtime test files into deterministic Go tests.

func newLaneSnapshot() LaneSnapshot {
	return LaneSnapshot{
		Lane:          "main",
		Transcript:    []json.RawMessage{},
		TipID:         nil,
		Configuration: harnesstypes.LaneConfiguration{Model: harnesstypes.ModelIdentity{Provider: "fixture", ModelID: "fixture"}, ThinkingLevel: agenttypes.ThinkingLevel("off"), ActiveToolNames: []string{}},
		Stats:         LaneStats{MessageCount: 0, Usage: json.RawMessage(`{}`)},
		Operation:     nil,
		Queues:        json.RawMessage(`{}`),
	}
}

func TestReduceLaneSnapshotRunToolAndTerminal(t *testing.T) {
	snapshot := newLaneSnapshot()
	lane := "main"
	runID := "r1"
	ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "run_start", Lane: &lane, RunID: runID, StartedAt: 1})
	if snapshot.Operation == nil || snapshot.Operation.ID != "r1" || snapshot.Operation.Kind != "run" || snapshot.Operation.Status != "open" {
		t.Fatalf("run_start operation = %+v", snapshot.Operation)
	}
	ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "tool_start", Lane: &lane, RunID: runID, ToolCallID: "c1", ToolName: "echo", Args: json.RawMessage(`{"x":1}`)})
	ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "tool_start", Lane: &lane, RunID: runID, ToolCallID: "c1", ToolName: "echo", Args: json.RawMessage(`{"x":2}`)})
	if len(snapshot.Operation.RunningTools) != 1 || string(snapshot.Operation.RunningTools[0].Args) != `{"x":2}` {
		t.Fatalf("tool upsert = %+v", snapshot.Operation.RunningTools)
	}
	ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "tool_end", Lane: &lane, RunID: runID, ToolCallID: "c1", ToolName: "echo", Result: json.RawMessage(`{"content":[{"type":"text","text":"2"}]}`)})
	settled := snapshot.Operation.RunningTools[0]
	if settled.Status != "settled" || settled.IsError == nil || *settled.IsError {
		t.Fatalf("settled tool = %+v", settled)
	}
	tipID := "e1"
	ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "run_end", Lane: &lane, RunID: runID, Status: "completed", TipID: &tipID, EndedAt: 8})
	if snapshot.Operation != nil {
		t.Fatal("run_end must clear the operation")
	}
	if snapshot.TipID == nil || *snapshot.TipID != "e1" {
		t.Fatalf("tip = %v", snapshot.TipID)
	}
	if snapshot.LastResult == nil || snapshot.LastResult.Status != harnesstypes.TerminalStatusCompleted || snapshot.LastResult.StartedAt != 1 || snapshot.LastResult.EndedAt != 8 {
		t.Fatalf("lastResult = %+v", snapshot.LastResult)
	}
}

func TestReduceLaneSnapshotFiltersOtherLane(t *testing.T) {
	snapshot := newLaneSnapshot()
	main := "main"
	other := "other"
	runID := "r1"
	ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "run_start", Lane: &main, RunID: runID, StartedAt: 1})
	ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "tool_start", Lane: &other, RunID: runID, ToolCallID: "c1", ToolName: "echo", Args: json.RawMessage(`{"x":1}`)})
	if len(snapshot.Operation.RunningTools) != 0 {
		t.Fatalf("foreign lane event leaked: %+v", snapshot.Operation.RunningTools)
	}
}

func TestReduceLaneSnapshotNavigationRebase(t *testing.T) {
	snapshot := newLaneSnapshot()
	if reduction := ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "navigation_end"}); reduction != LaneSnapshotReductionRebase {
		t.Fatalf("navigation reduction = %q", reduction)
	}
}

func TestReduceLaneSnapshotFault(t *testing.T) {
	snapshot := newLaneSnapshot()
	ReduceLaneSnapshot(&snapshot, &LaneEvent{Type: "fault"})
	if snapshot.Faulted == nil || !*snapshot.Faulted {
		t.Fatal("fault must set faulted")
	}
}

func TestRetryNotBeforeAddsDelay(t *testing.T) {
	policy := harnesstypes.RetryPolicy{BaseDelayMs: 100, MaxAgentDelayMs: floatPointer(500)}
	got := RetryNotBefore(policy, 1, 1000)
	if got <= 1000 {
		t.Fatalf("retryNotBefore = %v", got)
	}
}

func floatPointer(value float64) *float64 { return &value }

func TestChainEntriesAssignsParents(t *testing.T) {
	first := harnesstypes.MessageEntry{EntryBase: harnesstypes.EntryBase{ID: "a", Type: harnesstypes.EntryTypeMessage}}
	second := harnesstypes.MessageEntry{EntryBase: harnesstypes.EntryBase{ID: "b", Type: harnesstypes.EntryTypeMessage}}
	root := "root"
	chained := ChainEntries(&root, []harnesstypes.MessageEntry{first, second})
	if chained[0].ParentID == nil || *chained[0].ParentID != "root" {
		t.Fatalf("first parent = %v", chained[0].ParentID)
	}
	if chained[1].ParentID == nil || *chained[1].ParentID != "a" {
		t.Fatalf("second parent = %v", chained[1].ParentID)
	}
}

func TestEntryLifecycleEventsOrder(t *testing.T) {
	message := agenttypes.NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("hi", 1)))
	entry := harnesstypes.MessageEntry{EntryBase: harnesstypes.EntryBase{ID: "m1", Type: harnesstypes.EntryTypeMessage}, Message: message}
	events := EntryLifecycleEvents(entry, "main", nil)
	if len(events) != 3 || events[0].Type != "message_start" || events[1].Type != "message_end" || events[2].Type != "entry_added" {
		t.Fatalf("lifecycle events = %+v", events)
	}
}

func TestOperationResultRecordRejectsBadError(t *testing.T) {
	meta := harnesstypes.OperationMeta{OperationID: "r1", Intent: harnesstypes.OperationIntent{Kind: "run"}}
	if _, err := OperationResultRecord(meta, harnesstypes.TerminalStatusFailed, nil, nil); err == nil {
		t.Fatal("failed without an error must be rejected")
	}
	if _, err := OperationResultRecord(meta, harnesstypes.TerminalStatusCompleted, nil, &harnesstypes.OperationError{Code: "x"}); err == nil {
		t.Fatal("completed with an error must be rejected")
	}
}
