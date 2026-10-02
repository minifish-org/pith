// Command virtual-model demonstrates per-session virtual model routing in the
// embedded SDK.
//
// A virtual model is a catalog entry with the `pi-virtual` API. It never
// reaches a provider: before each request the session calls the router, which
// selects a physical model and thinking level. Router state is persisted on the
// session branch, so a resumed session keeps its routing memory.
//
// The example runs fully offline with a scripted stream and an in-memory
// session, so it builds and runs with CGO_ENABLED=0.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
	codingagent "github.com/minifish-org/pith/packages/coding-agent"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "virtual-model example:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The physical model a provider can actually stream.
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	var routedModel string
	var lastState string
	definition := codingagent.VirtualModelDefinition{
		Provider:       "example-router",
		ID:             "smart-cheap",
		Name:           "Smart Cheap",
		ContextWindow:  8192,
		MaxTokens:      512,
		ThinkingLevels: []aitypes.ModelThinkingLevel{aitypes.ThinkingOff, aitypes.ThinkingHigh},
		Route: func(_ context.Context, request codingagent.ModelRouteRequest) (codingagent.ModelRoute, error) {
			// The router sees why the request happened and, on later requests,
			// the router state it stored on this session branch.
			lastState = string(request.State)
			fmt.Printf("route reason=%s state=%s\n", request.Reason, string(request.State))
			physical := physicalModel()
			routedModel = physical.Id
			// Store routing memory; it is persisted on the active branch.
			return codingagent.ModelRoute{
				Model:         physical,
				ThinkingLevel: aitypes.ThinkingHigh,
				State:         json.RawMessage(`{"turn":1}`),
			}, nil
		},
	}

	virtual := codingagent.CreateVirtualModel(definition)
	if !codingagent.IsVirtualModel(&virtual) {
		return fmt.Errorf("create virtual model returned a physical entry")
	}

	options := codingagent.SessionOptions{
		Cwd:           cwd,
		Model:         codingagent.ModelOptions{Model: &virtual},
		VirtualModels: []codingagent.VirtualModelDefinition{definition},
	}
	options.Model.StreamFn = func(model *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		// The stream only ever sees the routed physical model.
		if model.Api == codingagent.VirtualModelAPI {
			return errorStream(model, "virtual model reached the provider unimplemented")
		}
		return textStream(model, "routed to "+model.Id)
	}

	session, err := codingagent.CreateAgentSession(options)
	if err != nil {
		return err
	}
	defer session.Close()

	for i := 0; i < 2; i++ {
		result, err := session.Prompt(ctx, "route this request")
		if err != nil {
			return err
		}
		if result.StopReason != aitypes.StopReasonStop {
			return fmt.Errorf("unexpected stop reason %q", result.StopReason)
		}
	}
	fmt.Printf("provider saw physical model: %s\n", routedModel)
	// The second request saw the state the first request stored, proving the
	// router memory round-trips through the session branch.
	if lastState == `{"turn":1}` {
		fmt.Println("virtual model state persisted on branch")
	}
	return nil
}

func physicalModel() aitypes.Model {
	return aitypes.Model{
		Id:            "physical-model",
		Name:          "Physical Model",
		Api:           aitypes.ApiOpenAICompletions,
		Provider:      aitypes.ProviderOpenAI,
		BaseUrl:       "http://localhost.invalid/v1",
		ContextWindow: 8192,
		MaxTokens:     512,
	}
}

func textStream(model *aitypes.Model, text string) *aitypes.AssistantMessageEventStream {
	stream := aitypes.NewAssistantMessageEventStream()
	message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
	message.Content = []aitypes.ContentBlock{aitypes.TextBlock(text)}
	message.StopReason = aitypes.StopReasonStop
	stream.Push(aitypes.NewDoneEvent(aitypes.StopReasonStop, message))
	return stream
}

func errorStream(model *aitypes.Model, text string) *aitypes.AssistantMessageEventStream {
	stream := aitypes.NewAssistantMessageEventStream()
	message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 0)
	message.StopReason = aitypes.StopReasonError
	message.ErrorMessage = &text
	stream.Push(aitypes.NewErrorEvent(aitypes.StopReasonError, message))
	return stream
}
