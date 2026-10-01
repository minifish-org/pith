package agent_test

import (
	"encoding/json"
	"github.com/minifish-org/pith/packages/agent"
	"github.com/minifish-org/pith/packages/ai/types"
	"reflect"
	"strings"
	"testing"
)

func TestPortsmithJudgeAgentProviderObserver(t *testing.T) {
	model := &types.Model{Id: "fixture", Name: "Fixture", Api: types.ApiOpenAICompletions, Provider: types.ProviderOpenAI, ContextWindow: 8192, MaxTokens: 32}
	seen := []string{}
	options := agent.AgentOptions{InitialState: &agent.AgentInitialState{Model: model}}
	field := reflect.ValueOf(&options).Elem().FieldByName("OnProviderStreamEvent")
	callback := func(data any, actual *types.Model) error {
		if actual.Id != model.Id || actual.Provider != model.Provider {
			t.Error("model identity changed")
		}
		raw, _ := json.Marshal(data)
		seen = append(seen, string(raw))
		return nil
	}
	if !field.IsValid() || !field.CanSet() || !reflect.TypeOf(callback).AssignableTo(field.Type()) {
		t.Fatal("AgentOptions must expose OnProviderStreamEvent func(any,*types.Model)error")
	}
	field.Set(reflect.ValueOf(callback))
	requests := 0
	options.StreamFn = func(actual *types.Model, transcript *types.TranscriptContext, streamOptions *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
		requests++
		stream := types.NewAssistantMessageEventStream()
		message := types.NewAssistantMessage(actual.Api, actual.Provider, actual.Id, 1)
		message.StopReason = types.StopReasonStop
		message.Content = []types.ContentBlock{types.TextBlock("done")}
		f := reflect.ValueOf(streamOptions).Elem().FieldByName("OnProviderStreamEvent")
		if !f.IsValid() || f.Kind() != reflect.Func || f.IsNil() {
			t.Error("agent did not forward observer")
		} else {
			observe, ok := f.Interface().(func(any, *types.Model) error)
			if !ok {
				t.Error("wrong stream callback signature")
			} else if err := observe(map[string]any{"request_cost": 0.01, "request": requests}, actual); err != nil {
				t.Error(err)
			}
		}
		stream.Push(types.NewDoneEvent(message.StopReason, message))
		return stream
	}
	a, err := agent.NewAgent(options)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.PromptString("one", nil); err != nil {
		t.Fatal(err)
	}
	if err = a.PromptString("two", nil); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(seen) != 2 || !strings.Contains(seen[0], `"request_cost":0.01`) || !strings.Contains(seen[1], `"request":2`) {
		t.Fatalf("observer forwarding: requests=%d data=%v", requests, seen)
	}
}
