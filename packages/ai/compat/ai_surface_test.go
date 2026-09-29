package compat

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/providers"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/ai/utils"
)

func TestCompatGetProvidersMatchesBuiltins(t *testing.T) {
	got := GetProviders()
	want := providers.GetBuiltinProviders()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetProviders mismatch\nwant %v\ngot  %v", want, got)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] < got[j] }) {
		t.Fatalf("GetProviders must be deterministic and sorted: %v", got)
	}
	for _, expected := range []string{"anthropic", "openai", "amazon-bedrock", "zai-coding-cn"} {
		found := false
		for _, id := range got {
			if string(id) == expected {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("GetProviders missing %q", expected)
		}
	}
}

func TestCompatGetModelAndGetModels(t *testing.T) {
	model := GetModel("anthropic", "claude-sonnet-4-5")
	if model == nil {
		t.Fatal("expected a catalog model for anthropic/claude-sonnet-4-5")
	}
	if model.Provider != types.ProviderId("anthropic") {
		t.Fatalf("unexpected provider %q", model.Provider)
	}
	if GetModel("anthropic", "does-not-exist") != nil {
		t.Fatal("unknown model id must return nil")
	}
	if models := GetModels("anthropic"); len(models) == 0 {
		t.Fatal("expected anthropic catalog models")
	}
	if models := GetModels("unknown-provider"); len(models) != 0 {
		t.Fatalf("unknown provider must return no models, got %d", len(models))
	}
}

func TestCompatRegisterAndUnregisterApiProviders(t *testing.T) {
	ResetApiProviders()
	defer ResetApiProviders()
	apiID := types.Api("test-surface-registry")
	provider := ApiProvider{API: apiID, Stream: noopStream, StreamSimple: noopSimple}
	RegisterApiProvider(provider, "test-source")
	if got := GetApiProvider(apiID); got == nil || got.API != apiID {
		t.Fatalf("registered provider not found: %#v", got)
	}
	if len(GetApiProviders()) == 0 {
		t.Fatal("expected at least the builtin providers")
	}
	UnregisterApiProviders("test-source")
	if GetApiProvider(apiID) != nil {
		t.Fatal("provider must be removed by source id")
	}
}

func TestCompatStreamDispatchesRegisteredProvider(t *testing.T) {
	ResetApiProviders()
	defer ResetApiProviders()
	apiID := types.Api("test-surface-dispatch")
	RegisterApiProvider(ApiProvider{API: apiID, Stream: doneStream("dispatched"), StreamSimple: doneSimpleStream("dispatched-simple")}, "test-source")
	model := types.Model{Id: "m", Provider: types.ProviderId("test-provider"), Api: apiID}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream := Stream(&model, types.Context{Messages: []types.Message{types.NewUserMessageVariant(types.NewUserMessage("hi", 1))}}, nil)
	result, err := stream.Result(ctx)
	if err != nil {
		t.Fatalf("stream result: %v", err)
	}
	if result.StopReason != types.StopReasonStop {
		t.Fatalf("unexpected stop reason %q", result.StopReason)
	}
	if text := utils.ContentText(result.Content); text != "dispatched" {
		t.Fatalf("unexpected content %q", text)
	}
	simple := StreamSimple(&model, types.Context{Messages: []types.Message{}}, nil)
	simpleResult, err := simple.Result(ctx)
	if err != nil {
		t.Fatalf("simple result: %v", err)
	}
	if text := utils.ContentText(simpleResult.Content); text != "dispatched-simple" {
		t.Fatalf("unexpected simple content %q", text)
	}
}

func TestCompatStreamUnknownApiClassifiesError(t *testing.T) {
	ResetApiProviders()
	defer ResetApiProviders()
	model := types.Model{Id: "m", Provider: types.ProviderId("nobody"), Api: types.Api("no-such-api")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Stream(&model, types.Context{}, nil).Result(ctx)
	if err != nil {
		t.Fatalf("result must resolve to an error message, not fail the stream: %v", err)
	}
	if result.StopReason != types.StopReasonError {
		t.Fatalf("expected error classification, got %q", result.StopReason)
	}
}

func TestCompatRegisterFauxProviderDispatches(t *testing.T) {
	ResetApiProviders()
	defer ResetApiProviders()
	registration := RegisterFauxProvider(nil)
	defer registration.Unregister()
	message := types.AssistantMessage{
		Role:       types.AssistantMessageRole,
		Content:    []types.ContentBlock{types.TextBlock("faux-ok")},
		StopReason: types.StopReasonStop,
		Timestamp:  1,
	}
	registration.SetResponses([]providers.FauxResponseStep{{Message: &message}})
	if len(registration.Models) == 0 {
		t.Fatal("faux registration must expose at least one model")
	}
	model := registration.Models[0]
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Stream(&model, types.Context{}, nil).Result(ctx)
	if err != nil {
		t.Fatalf("faux result: %v", err)
	}
	if text := utils.ContentText(result.Content); text != "faux-ok" {
		t.Fatalf("unexpected faux content %q", text)
	}
	if GetApiProvider(types.Api(registration.API)) == nil {
		t.Fatal("faux api must be registered")
	}
}

func TestCompatMismatchedApiProducesErrorStream(t *testing.T) {
	ResetApiProviders()
	defer ResetApiProviders()
	apiID := types.Api("test-surface-mismatch")
	RegisterApiProvider(ApiProvider{API: apiID, Stream: doneStream("never"), StreamSimple: doneSimpleStream("never")}, "test-source")
	model := types.Model{Id: "m", Provider: types.ProviderId("test-provider"), Api: types.Api("other-api")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := Stream(&model, types.Context{}, nil).Result(ctx)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	// The api has no registration, so the dispatch resolves an error stream.
	if result.StopReason != types.StopReasonError {
		t.Fatalf("expected error classification, got %q", result.StopReason)
	}
}

func noopStream(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	return doneStream("noop")(model, context, options)
}

func noopSimple(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	return doneStream("noop")(model, context, nil)
}

func doneStream(text string) func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
	return func(model *types.Model, context *types.TranscriptContext, options *types.StreamOptions) *types.AssistantMessageEventStream {
		return buildDoneStream(text)
	}
}

func doneSimpleStream(text string) func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
	return func(model *types.Model, context *types.TranscriptContext, options *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
		return buildDoneStream(text)
	}
}

func buildDoneStream(text string) *types.AssistantMessageEventStream {
	stream := types.NewAssistantMessageEventStream()
	message := types.AssistantMessage{
		Role:       types.AssistantMessageRole,
		Content:    []types.ContentBlock{types.TextBlock(text)},
		StopReason: types.StopReasonStop,
		Timestamp:  1,
	}
	stream.Push(types.NewDoneEvent(types.StopReasonStop, message))
	return stream
}
