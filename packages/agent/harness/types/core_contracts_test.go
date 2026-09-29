package harnesstypes

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestResultHelpers(t *testing.T) {
	value, ok := GetOrUndefined(Ok[string, error]("v"))
	if !ok || value != "v" {
		t.Fatalf("GetOrUndefined = %q/%v", value, ok)
	}
	if _, ok := GetOrUndefined(Err[string, error](errors.New("x"))); ok {
		t.Fatal("expected failure to be undefined")
	}
	if got := GetOrThrow(Ok[int, error](7)); got != 7 {
		t.Fatalf("GetOrThrow = %d", got)
	}
	if err := ToError("boom"); err == nil || err.Error() != "boom" {
		t.Fatalf("ToError = %v", err)
	}
}

func TestMessageEntryJSONRoundTrip(t *testing.T) {
	message := aitypes.NewUserMessageVariant(aitypes.NewUserMessage("hi", 1))
	entry := MessageEntry{
		EntryBase: EntryBase{ID: "e1", Seq: 1, Timestamp: 1, Type: EntryTypeMessage},
		Message:   agenttypes.NewAgentMessageFromMessage(message),
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != string(EntryTypeMessage) {
		t.Fatalf("type = %v", decoded["type"])
	}
	if decoded["parentId"] != nil {
		t.Fatalf("parentId = %v", decoded["parentId"])
	}
	var round MessageEntry
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatal(err)
	}
	if round.Message.Message == nil || round.Message.Message.User == nil || round.Message.Message.User.Content.Text != "hi" {
		t.Fatalf("round trip lost the message: %+v", round.Message)
	}
}

func TestLaneConfigurationJSON(t *testing.T) {
	configuration := LaneConfiguration{
		Model:           ModelIdentity{Provider: "fixture", ModelID: "model"},
		ThinkingLevel:   agenttypes.ThinkingLow,
		ActiveToolNames: []string{},
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	model, _ := decoded["model"].(map[string]any)
	if model["provider"] != "fixture" || model["modelId"] != "model" {
		t.Fatalf("model = %#v", decoded["model"])
	}
	if decoded["activeToolNames"] == nil {
		t.Fatal("empty tool names must survive as an empty array")
	}
}

func TestOperationScopeOfCopiesUniformScope(t *testing.T) {
	operation := &StartingOperation{
		OperationScope: OperationScope{
			Cancellable:            Cancellable{Control: Control{Status: "running"}},
			Settings:               RunSettings{Compaction: DefaultCompactionSettings},
			LatestAssistantEntryID: stringPointer("a1"),
		},
		At: OperationAtStarting,
	}
	scope := OperationScopeOf(operation)
	if scope.Control.Status != "running" || scope.LatestAssistantEntryID == nil || *scope.LatestAssistantEntryID != "a1" {
		t.Fatalf("scope = %+v", scope)
	}
	if operation.StateAt() != OperationAtStarting {
		t.Fatalf("state at = %q", operation.StateAt())
	}
}

func TestDriveSettlesOnceAndCloses(t *testing.T) {
	drive := NewDrive(DriveOptions{OperationID: "op"}, harnesscontext.BackgroundContext)
	drive.Settle(DriveOutcome{Kind: "settled"})
	select {
	case completion := <-drive.Completion:
		if completion.Err != nil || completion.Outcome.Kind != "settled" {
			t.Fatalf("completion = %+v", completion)
		}
	case <-time.After(time.Second):
		t.Fatal("drive did not settle")
	}

	failing := NewDrive(DriveOptions{OperationID: "op2"}, harnesscontext.BackgroundContext)
	failing.CloseGate(errors.New("closed"))
	select {
	case completion := <-failing.Completion:
		if completion.Err == nil || completion.Err.Error() != "closed" {
			t.Fatalf("close completion = %+v", completion)
		}
	case <-time.After(time.Second):
		t.Fatal("drive did not close")
	}
	select {
	case <-failing.CloseSignal:
	default:
		t.Fatal("close signal should be closed")
	}
}

func stringPointer(value string) *string { return &value }
