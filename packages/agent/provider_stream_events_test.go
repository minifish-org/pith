// Candidate self-tests for the agent-side provider stream observer.
//
// They cover the forwarding contract only: the observer is stored per agent
// instance, forwarded on every request (initial, tool-loop and continuation),
// and remains optional. Independent acceptance tests for the provider adapters
// live in packages/ai/api and are not duplicated here.
package agent

import (
	"sync"
	"testing"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func observerModel() *aitypes.Model {
	return &aitypes.Model{
		Id:            "fixture",
		Name:          "Fixture",
		Api:           aitypes.ApiOpenAICompletions,
		Provider:      aitypes.ProviderOpenAI,
		ContextWindow: 8192,
		MaxTokens:     32,
	}
}

func observerTranscript() *aitypes.TranscriptContext {
	return aitypes.NewTranscriptContext([]aitypes.Message{
		aitypes.NewUserMessageVariant(aitypes.NewUserMessage("hello", 1)),
	})
}

func observerOutput() aitypes.AssistantMessage {
	message := aitypes.NewAssistantMessage(aitypes.ApiOpenAICompletions, aitypes.ProviderOpenAI, "fixture", 1)
	message.StopReason = aitypes.StopReasonStop
	message.Content = []aitypes.ContentBlock{aitypes.TextBlock("done")}
	return message
}

// TestProviderObserverPerAgentInstance proves the observer is stored on the
// Agent, not in a process-global registry: two agents keep distinct observers
// and never observe each other's requests.
func TestProviderObserverPerAgentInstance(t *testing.T) {
	model := observerModel()

	type captured struct {
		modelId   string
		provider  aitypes.ProviderId
		hasObject bool
	}
	var mu sync.Mutex
	seen := map[string][]captured{}

	streamFn := func(actual *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		if options == nil || options.OnProviderStreamEvent == nil {
			t.Error("observer was not forwarded to the request")
		} else {
			payload := map[string]any{"tag": "first"}
			if err := options.OnProviderStreamEvent(payload, actual); err != nil {
				t.Errorf("observer returned error: %v", err)
			}
			entry := captured{modelId: actual.Id, provider: actual.Provider, hasObject: payload["tag"] == "first"}
			mu.Lock()
			seen[actual.Id] = append(seen[actual.Id], entry)
			mu.Unlock()
		}
		message := observerOutput()
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}

	first, err := NewAgent(AgentOptions{
		StreamFn:              streamFn,
		InitialState:          &AgentInitialState{Model: model},
		OnProviderStreamEvent: func(data any, actual *aitypes.Model) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewAgent(first): %v", err)
	}

	secondModel := observerModel()
	secondModel.Id = "second"
	secondCalls := 0
	second, err := NewAgent(AgentOptions{
		StreamFn:     streamFn,
		InitialState: &AgentInitialState{Model: secondModel},
		OnProviderStreamEvent: func(data any, actual *aitypes.Model) error {
			secondCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAgent(second): %v", err)
	}

	if err := first.PromptString("one", nil); err != nil {
		t.Fatalf("first.PromptString: %v", err)
	}
	if err := first.PromptString("two", nil); err != nil {
		t.Fatalf("first.PromptString: %v", err)
	}
	if err := second.PromptString("three", nil); err != nil {
		t.Fatalf("second.PromptString: %v", err)
	}

	if secondCalls != 1 {
		t.Fatalf("second observer calls = %d, want 1", secondCalls)
	}
	if got := len(seen["fixture"]); got != 2 {
		t.Fatalf("first agent observed %d requests, want 2", got)
	}
	if got := len(seen["second"]); got != 1 {
		t.Fatalf("second agent observed %d requests, want 1", got)
	}
	for _, entry := range seen["fixture"] {
		if entry.modelId != "fixture" || entry.provider != aitypes.ProviderOpenAI || !entry.hasObject {
			t.Fatalf("first agent captured %+v", entry)
		}
	}
}

// TestProviderObserverForwardedOnToolLoopRequests proves the observer reaches
// every request in a tool loop and in an explicit continuation, not just the
// first request.
func TestProviderObserverForwardedOnToolLoopRequests(t *testing.T) {
	model := observerModel()
	requests := 0
	var observed []string

	tool := agenttypes.AgentTool[any, any]{
		Tool:  aitypes.NewTool("echo", "echo", jsonSchema(`{"type":"object"}`)),
		Label: "echo",
		Execute: func(toolCallId string, params any, signal <-chan struct{}, onUpdate agenttypes.AgentToolUpdateCallback[any]) (agenttypes.AgentToolResult[any], error) {
			return agenttypes.AgentToolResult[any]{
				Content: []aitypes.ContentBlock{aitypes.TextBlock("echoed")},
				Details: map[string]any{},
			}, nil
		},
	}

	streamFn := func(actual *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		requests++
		stream := aitypes.NewAssistantMessageEventStream()
		if options == nil || options.OnProviderStreamEvent == nil {
			t.Error("observer was not forwarded to a tool-loop request")
		} else if err := options.OnProviderStreamEvent(map[string]any{"request": requests}, actual); err != nil {
			t.Errorf("observer returned error: %v", err)
		}
		message := aitypes.NewAssistantMessage(actual.Api, actual.Provider, actual.Id, 1)
		if requests == 1 {
			message.StopReason = aitypes.StopReasonToolUse
			message.Content = []aitypes.ContentBlock{
				aitypes.ToolCallBlock(aitypes.NewToolCall("c1", "echo", []byte(`{}`))),
			}
		} else {
			message.StopReason = aitypes.StopReasonStop
			message.Content = []aitypes.ContentBlock{aitypes.TextBlock("done")}
		}
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}

	runtime, err := NewAgent(AgentOptions{
		StreamFn:     streamFn,
		InitialState: &AgentInitialState{Model: model, Tools: []agenttypes.AgentTool[any, any]{tool}},
		OnProviderStreamEvent: func(data any, actual *aitypes.Model) error {
			observed = append(observed, actual.Id)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}

	if err := runtime.PromptString("go", nil); err != nil {
		t.Fatalf("PromptString: %v", err)
	}
	if requests != 2 {
		t.Fatalf("tool loop requests = %d, want 2", requests)
	}
	if len(observed) != 2 {
		t.Fatalf("observer calls = %v, want 2", observed)
	}

	// An explicit continuation also forwards the observer. Continue() needs a
	// transcript that ends on a user message, so seed one directly.
	observed = nil
	continuation, err := NewAgent(AgentOptions{
		StreamFn:     streamFn,
		InitialState: &AgentInitialState{Model: model, Messages: []agenttypes.AgentMessage{userMessage("again", 2)}},
		OnProviderStreamEvent: func(data any, actual *aitypes.Model) error {
			observed = append(observed, actual.Id)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("NewAgent(continuation): %v", err)
	}
	requests = 1 // next call returns a final text message
	if err := continuation.Continue(); err != nil {
		t.Fatalf("Continue: %v", err)
	}
	if requests != 2 {
		t.Fatalf("continuation requests = %d, want 2", requests)
	}
	if len(observed) != 1 || observed[0] != "fixture" {
		t.Fatalf("continuation observer calls = %v, want [fixture]", observed)
	}
}

// TestProviderObserverNilCompatibility proves the callback stays optional:
// omitting it leaves the forwarded field nil and the run succeeds.
func TestProviderObserverNilCompatibility(t *testing.T) {
	model := observerModel()
	sawNil := false

	streamFn := func(actual *aitypes.Model, transcript *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		if options != nil && options.OnProviderStreamEvent == nil {
			sawNil = true
		} else {
			t.Error("nil observer must not fabricate a callback")
		}
		stream := aitypes.NewAssistantMessageEventStream()
		message := observerOutput()
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}

	runtime, err := NewAgent(AgentOptions{
		StreamFn:     streamFn,
		InitialState: &AgentInitialState{Model: model},
	})
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	if err := runtime.PromptString("hello", nil); err != nil {
		t.Fatalf("PromptString: %v", err)
	}
	if !sawNil {
		t.Fatal("nil observer was not forwarded as nil")
	}
}

// jsonSchema keeps the tool schema literal readable without an extra import.
func jsonSchema(raw string) []byte { return []byte(raw) }
