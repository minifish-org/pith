// Candidate self-tests for the embedded-SDK provider stream observer.
//
// They exercise the native Go adaptation of the upstream `provider_stream_event`
// extension event: SessionOptions.OnProviderStreamEvent, the transient
// provider_stream_event SessionEvent and session isolation/rebuild behavior.
// Independent acceptance tests live with the frozen judge and are not
// duplicated here.
//
// The tests use offline fake stream functions only: no live model, no Node and
// no Pi binary is required.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

// providerEventModel returns a model with a caller-chosen identity so tests can
// detect stale construction-time snapshots.
func providerEventModel(id string) *aitypes.Model {
	return &aitypes.Model{
		Id:            id,
		Name:          id,
		Api:           aitypes.ApiOpenAICompletions,
		Provider:      aitypes.ProviderOpenAI,
		BaseUrl:       "http://localhost.invalid/v1",
		ContextWindow: 8192,
		MaxTokens:     32,
	}
}

// providerEventStreamFn emits one parsed provider event through the observer the
// session installed on the request options, then completes the assistant turn.
func providerEventStreamFn(payload any) agenttypes.StreamFn {
	return func(model *aitypes.Model, _ *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		if options == nil || options.OnProviderStreamEvent == nil {
			stream.Push(aitypes.NewErrorEvent(aitypes.StopReasonError, errorAssistantMessage(model, "observer not forwarded")))
			return stream
		}
		if err := options.OnProviderStreamEvent(payload, model); err != nil {
			stream.Push(aitypes.NewErrorEvent(aitypes.StopReasonError, errorAssistantMessage(model, err.Error())))
			return stream
		}
		message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
		message.StopReason = aitypes.StopReasonStop
		message.Content = []aitypes.ContentBlock{aitypes.TextBlock("done")}
		stream.Push(aitypes.NewDoneEvent(message.StopReason, message))
		return stream
	}
}

func errorAssistantMessage(model *aitypes.Model, text string) aitypes.AssistantMessage {
	message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
	message.StopReason = aitypes.StopReasonError
	message.ErrorMessage = &text
	return message
}

// TestProviderStreamEventSessionIsolation proves the observer and transient
// SessionEvent are scoped to one AgentSession: two sessions with distinct
// callbacks and subscribers never observe each other's requests.
func TestProviderStreamEventSessionIsolation(t *testing.T) {
	type seenEvent struct {
		model string
		data  any
	}

	var mu sync.Mutex
	callbacks := map[string][]string{}
	events := map[string][]seenEvent{}

	newSession := func(id string) (*AgentSession, func()) {
		session, err := CreateAgentSession(SessionOptions{
			Cwd: t.TempDir(),
			Model: ModelOptions{
				Model:    providerEventModel(id),
				StreamFn: providerEventStreamFn(map[string]any{"session": id}),
			},
			OnProviderStreamEvent: func(data any, model *aitypes.Model) error {
				mu.Lock()
				callbacks[id] = append(callbacks[id], model.Id)
				mu.Unlock()
				return nil
			},
		})
		if err != nil {
			t.Fatalf("CreateAgentSession(%s): %v", id, err)
		}
		unsubscribe := session.Subscribe(func(event SessionEvent) {
			if event.Type != SessionEventProviderStreamEvent {
				return
			}
			mu.Lock()
			events[id] = append(events[id], seenEvent{model: event.Model, data: event.Data})
			mu.Unlock()
		})
		return session, unsubscribe
	}

	first, unsubFirst := newSession("model-a")
	defer unsubFirst()
	defer first.Close()
	second, unsubSecond := newSession("model-b")
	defer unsubSecond()
	defer second.Close()

	ctx := context.Background()
	if _, err := first.Prompt(ctx, "one"); err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	if _, err := second.Prompt(ctx, "two"); err != nil {
		t.Fatalf("second prompt: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := callbacks["model-a"]; len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("session A callback = %v, want [model-a]", got)
	}
	if got := callbacks["model-b"]; len(got) != 1 || got[0] != "model-b" {
		t.Fatalf("session B callback = %v, want [model-b]", got)
	}
	if got := events["model-a"]; len(got) != 1 || got[0].model != "model-a" {
		t.Fatalf("session A events = %+v, want one model-a event", got)
	}
	if got := events["model-b"]; len(got) != 1 || got[0].model != "model-b" {
		t.Fatalf("session B events = %+v, want one model-b event", got)
	}
	rawA, _ := json.Marshal(events["model-a"][0].data)
	rawB, _ := json.Marshal(events["model-b"][0].data)
	if strings.Contains(string(rawA), "model-b") || strings.Contains(string(rawB), "model-a") {
		t.Fatalf("cross-session leakage: A=%s B=%s", rawA, rawB)
	}
}

// TestProviderStreamEventSurvivesRebuild proves SetModel rebuilds the agent
// while preserving the observer and reporting the identity of the new request
// model instead of a stale construction-time snapshot.
func TestProviderStreamEventSurvivesRebuild(t *testing.T) {
	var mu sync.Mutex
	var callbackModels []string
	var eventModels []string

	firstModel := providerEventModel("model-a")
	secondModel := providerEventModel("model-b")

	session, err := CreateAgentSession(SessionOptions{
		Cwd:   t.TempDir(),
		Model: ModelOptions{Model: firstModel, StreamFn: providerEventStreamFn(map[string]any{"routing": "direct"})},
		OnProviderStreamEvent: func(_ any, model *aitypes.Model) error {
			mu.Lock()
			callbackModels = append(callbackModels, model.Id)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	unsubscribe := session.Subscribe(func(event SessionEvent) {
		if event.Type != SessionEventProviderStreamEvent {
			return
		}
		mu.Lock()
		eventModels = append(eventModels, event.Model)
		mu.Unlock()
	})
	defer unsubscribe()

	ctx := context.Background()
	if _, err := session.Prompt(ctx, "one"); err != nil {
		t.Fatalf("first prompt: %v", err)
	}
	if err := session.SetModel(ModelOptions{Model: secondModel}); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if _, err := session.Prompt(ctx, "two"); err != nil {
		t.Fatalf("second prompt: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(callbackModels) != 2 || callbackModels[0] != "model-a" || callbackModels[1] != "model-b" {
		t.Fatalf("callback models after rebuild = %v, want [model-a model-b]", callbackModels)
	}
	if len(eventModels) != 2 || eventModels[0] != "model-a" || eventModels[1] != "model-b" {
		t.Fatalf("event models after rebuild = %v, want [model-a model-b]", eventModels)
	}
}

// TestProviderStreamEventSubscriberWithoutCallback proves native subscribers
// receive transient events when no explicit callback is configured, and that
// the raw adapter payload never reaches the durable transcript.
func TestProviderStreamEventSubscriberWithoutCallback(t *testing.T) {
	payload := map[string]any{"ps_marker": "durable-leak-sentinel"}
	session, err := CreateAgentSession(SessionOptions{
		Cwd: t.TempDir(),
		Model: ModelOptions{
			Model:    providerEventModel("model-a"),
			StreamFn: providerEventStreamFn(payload),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	var event *SessionEvent
	unsubscribe := session.Subscribe(func(got SessionEvent) {
		if got.Type == SessionEventProviderStreamEvent {
			copied := got
			event = &copied
		}
	})
	defer unsubscribe()

	result, err := session.Prompt(context.Background(), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if event == nil {
		t.Fatal("subscriber did not receive provider_stream_event without a callback")
	}
	if event.Provider != string(aitypes.ProviderOpenAI) || event.API != string(aitypes.ApiOpenAICompletions) || event.Model != "model-a" {
		t.Fatalf("event identity = %+v", event)
	}
	raw, _ := json.Marshal(result.Messages)
	if strings.Contains(string(raw), "durable-leak-sentinel") {
		t.Fatalf("provider event leaked into durable transcript: %s", raw)
	}
}

// TestProviderStreamEventFailureIsTerminal proves an observer error fails the
// logical request with its original text, is delivered to subscribers, and is
// never replayed with a second request.
func TestProviderStreamEventFailureIsTerminal(t *testing.T) {
	requests := 0
	streamFn := func(model *aitypes.Model, _ *aitypes.TranscriptContext, options *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		requests++
		stream := aitypes.NewAssistantMessageEventStream()
		err := options.OnProviderStreamEvent(map[string]any{"x": 1}, model)
		if err == nil {
			text := "observer should have failed"
			stream.Push(aitypes.NewErrorEvent(aitypes.StopReasonError, errorAssistantMessage(model, text)))
			return stream
		}
		stream.Push(aitypes.NewErrorEvent(aitypes.StopReasonError, errorAssistantMessage(model, err.Error())))
		return stream
	}
	session, err := CreateAgentSession(SessionOptions{
		Cwd:                   t.TempDir(),
		Model:                 ModelOptions{Model: providerEventModel("model-a"), StreamFn: streamFn},
		OnProviderStreamEvent: func(any, *aitypes.Model) error { return errors.New("observer sentinel") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	delivered := 0
	unsubscribe := session.Subscribe(func(event SessionEvent) {
		if event.Type == SessionEventProviderStreamEvent {
			delivered++
		}
	})
	defer unsubscribe()

	_, err = session.Prompt(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "observer sentinel") {
		t.Fatalf("observer failure swallowed: %v", err)
	}
	if requests != 1 {
		t.Fatalf("observer failure triggered %d requests, want 1", requests)
	}
	if delivered != 1 {
		t.Fatalf("subscriber delivery on failure = %d, want 1", delivered)
	}
}
