package codingagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/minifish-org/pith/packages/ai/types"
	codingagent "github.com/minifish-org/pith/packages/coding-agent"
	"reflect"
	"strings"
	"testing"
	"time"
)

func psSDKObserver(t *testing.T, options *codingagent.SessionOptions, callback func(any, *types.Model) error) {
	t.Helper()
	field := reflect.ValueOf(options).Elem().FieldByName("OnProviderStreamEvent")
	if !field.IsValid() || !field.CanSet() || !reflect.TypeOf(callback).AssignableTo(field.Type()) {
		t.Fatal("SessionOptions must expose OnProviderStreamEvent func(any,*types.Model)error")
	}
	field.Set(reflect.ValueOf(callback))
}

func TestPortsmithJudgeSDKProviderObserver(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			model := &types.Model{Id: "fixture", Name: "Fixture", Api: types.ApiOpenAICompletions, Provider: types.ProviderOpenAI, ContextWindow: 8192, MaxTokens: 32}
			options := codingagent.SessionOptions{Cwd: t.TempDir(), Model: codingagent.ModelOptions{Model: model}}
			observed := 0
			psSDKObserver(t, &options, func(data any, actual *types.Model) error {
				observed++
				raw, _ := json.Marshal(data)
				if !strings.Contains(string(raw), `"strategy":"direct"`) || actual.Id != model.Id {
					t.Error("SDK callback lost metadata/model")
				}
				if fail {
					return errors.New("observer sentinel")
				}
				return nil
			})
			requests := 0
			options.Model.StreamFn = func(actual *types.Model, transcript *types.TranscriptContext, streamOptions *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
				requests++
				stream := types.NewAssistantMessageEventStream()
				message := types.NewAssistantMessage(actual.Api, actual.Provider, actual.Id, 1)
				message.StopReason = types.StopReasonStop
				message.Content = []types.ContentBlock{types.TextBlock("done")}
				field := reflect.ValueOf(streamOptions).Elem().FieldByName("OnProviderStreamEvent")
				if !field.IsValid() || field.Kind() != reflect.Func || field.IsNil() {
					t.Error("SDK did not forward observer")
				} else if callback, ok := field.Interface().(func(any, *types.Model) error); !ok {
					t.Error("incorrect callback signature")
				} else {
					if err := callback(map[string]any{"openrouter_metadata": map[string]any{"strategy": "direct"}}, actual); err != nil {
						message.StopReason = types.StopReasonError
						text := err.Error()
						message.ErrorMessage = &text
						stream.Push(types.NewErrorEvent(message.StopReason, message))
						return stream
					}
				}
				stream.Push(types.NewDoneEvent(message.StopReason, message))
				return stream
			}
			session, err := codingagent.CreateAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			var events []map[string]any
			unsubscribe := session.Subscribe(func(event codingagent.SessionEvent) {
				if event.Type == "provider_stream_event" {
					raw, _ := json.Marshal(event)
					var object map[string]any
					_ = json.Unmarshal(raw, &object)
					events = append(events, object)
				}
			})
			defer unsubscribe()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := session.Prompt(ctx, "hello")
			if fail {
				if err == nil || !strings.Contains(err.Error(), "observer sentinel") {
					t.Fatalf("SDK swallowed observer failure: result=%+v err=%v", result, err)
				}
			} else {
				if err != nil || result.StopReason != types.StopReasonStop {
					t.Fatalf("SDK prompt failed: result=%+v err=%v", result, err)
				}
				if len(events) != 1 || events[0]["provider"] != string(model.Provider) || events[0]["api"] != string(model.Api) || events[0]["model"] != model.Id {
					t.Fatalf("headless event missing provider identity: %v", events)
				}
				raw, _ := json.Marshal(events[0]["data"])
				if !strings.Contains(string(raw), `"strategy":"direct"`) {
					t.Fatalf("headless event lost data: %v", events)
				}
			}
			if observed != 1 || requests != 1 {
				t.Fatalf("callback lost/replayed: observed=%d requests=%d", observed, requests)
			}
			raw, _ := json.Marshal(result.Messages)
			if strings.Contains(string(raw), "openrouter_metadata") {
				t.Fatal("provider event leaked into durable transcript")
			}
		})
	}
}

func TestPortsmithJudgeSDKProviderEventsWithoutCallback(t *testing.T) {
	options := codingagent.SessionOptions{Cwd: t.TempDir(), Model: codingagent.ModelOptions{Model: &types.Model{Id: "fixture", Name: "Fixture", Api: types.ApiOpenAICompletions, Provider: types.ProviderOpenAI, ContextWindow: 8192, MaxTokens: 32}}}
	options.Model.StreamFn = func(model *types.Model, transcript *types.TranscriptContext, streamOptions *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
		stream := types.NewAssistantMessageEventStream()
		message := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
		message.StopReason = types.StopReasonStop
		field := reflect.ValueOf(streamOptions).Elem().FieldByName("OnProviderStreamEvent")
		if !field.IsValid() || field.Kind() != reflect.Func || field.IsNil() {
			t.Error("session subscribers must receive provider events without an explicit callback")
		} else if callback, ok := field.Interface().(func(any, *types.Model) error); !ok {
			t.Error("wrong callback type")
		} else if err := callback(map[string]any{"routing": "direct"}, model); err != nil {
			t.Error(err)
		}
		stream.Push(types.NewDoneEvent(message.StopReason, message))
		return stream
	}
	session, err := codingagent.CreateAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	seen := 0
	unsubscribe := session.Subscribe(func(event codingagent.SessionEvent) {
		if event.Type == "provider_stream_event" {
			seen++
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = session.Prompt(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	unsubscribe()
	if _, err = session.Prompt(ctx, "two"); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("subscribe/unsubscribe delivery=%d", seen)
	}
}
