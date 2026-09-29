package agenttypes

import (
	"encoding/json"
	"testing"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func TestAgentMessageStandardRoundTrip(t *testing.T) {
	message := NewAgentMessageFromMessage(aitypes.NewUserMessageVariant(aitypes.NewUserMessage("hi", 1)))
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["role"] != aitypes.UserMessageRole || decoded["content"] != "hi" {
		t.Fatalf("encoded = %s", encoded)
	}
	var round AgentMessage
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatal(err)
	}
	if round.Message == nil || round.Message.User == nil || round.Message.User.Content.Text != "hi" {
		t.Fatalf("round = %+v", round)
	}
}

func TestAgentMessagePreservesUnknownCustomPayload(t *testing.T) {
	raw := json.RawMessage(`{"role":"notification","level":"info","nested":{"n":1}}`)
	message := NewCustomMessage("notification", raw)
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(raw) {
		t.Fatalf("custom payload changed: %s", encoded)
	}
	var round AgentMessage
	if err := json.Unmarshal(encoded, &round); err != nil {
		t.Fatal(err)
	}
	if round.Custom == nil || round.Custom.Role != "notification" {
		t.Fatalf("round = %+v", round)
	}
	var payload map[string]any
	if err := json.Unmarshal(round.Custom.Raw, &payload); err != nil {
		t.Fatal(err)
	}
	nested, _ := payload["nested"].(map[string]any)
	if payload["level"] != "info" || nested["n"] != float64(1) {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestAgentEventJSON(t *testing.T) {
	event := AgentEvent{Type: AgentEventTurnStart}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != AgentEventTurnStart {
		t.Fatalf("event = %s", encoded)
	}
}

func TestQueueAndExecutionModes(t *testing.T) {
	if QueueModeAll != "all" || QueueModeOneAtATime != "one-at-a-time" {
		t.Fatal("queue mode wire values changed")
	}
	if ToolExecutionSequential != "sequential" || ToolExecutionParallel != "parallel" {
		t.Fatal("tool execution mode wire values changed")
	}
	if ThinkingLevel(ThinkingLow) != aitypes.ThinkingLow {
		t.Fatal("thinking level alias diverged")
	}
}
