// Command minimal is the smallest embedded-SDK example: create an agent
// session, send one prompt and print the answer.
//
// By default it uses an offline fake stream so `go run` never calls a paid
// model. Set PITH_SDK_LIVE=1 (plus OPENAI_API_KEY and optionally
// PITH_SDK_MODEL, PITH_SDK_BASE_URL) to talk to a live provider through the
// native Pith AI provider resolved from the model API.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	codingagent "github.com/minifish-org/pith/packages/coding-agent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, err := codingagent.CreateAgentSession(sessionOptions())
	if err != nil {
		fmt.Fprintln(os.Stderr, "create session:", err)
		os.Exit(1)
	}
	defer session.Close()

	result, err := session.Prompt(ctx, "Summarize what this SDK does in one sentence.")
	if err != nil {
		fmt.Fprintln(os.Stderr, "prompt:", err)
		os.Exit(1)
	}
	fmt.Printf("stop=%s turns=%d tokens=%g\n", result.StopReason, result.Turns, result.Usage.TotalTokens)
	for _, message := range result.Messages {
		if message.Message != nil && message.Message.Assistant != nil {
			for _, block := range message.Message.Assistant.Content {
				if block.IsText() && block.Text != nil {
					fmt.Println(block.Text.Text)
				}
			}
		}
	}
}

// sessionOptions selects a live provider when explicitly configured and falls
// back to an offline fake stream by default.
func sessionOptions() codingagent.SessionOptions {
	cwd, _ := os.Getwd()
	if os.Getenv("PITH_SDK_LIVE") == "1" {
		return codingagent.SessionOptions{
			Cwd: cwd,
			Model: codingagent.ModelOptions{
				Model: liveModel(),
				APIKey: func(context.Context, string) (string, error) {
					return os.Getenv("OPENAI_API_KEY"), nil
				},
			},
		}
	}
	return codingagent.SessionOptions{
		Cwd:   cwd,
		Model: codingagent.ModelOptions{Model: fakeModel(), StreamFn: fakeStream("The embedded SDK runs an agent in-process from Go.")},
	}
}

func fakeModel() *aitypes.Model {
	return &aitypes.Model{
		Id:            "fake-model",
		Name:          "fake-model",
		Api:           aitypes.ApiOpenAICompletions,
		Provider:      aitypes.ProviderOpenAI,
		BaseUrl:       "http://localhost.invalid/v1",
		ContextWindow: 128000,
		MaxTokens:     4096,
	}
}

func liveModel() *aitypes.Model {
	id := os.Getenv("PITH_SDK_MODEL")
	if id == "" {
		id = "gpt-4o-mini"
	}
	baseURL := os.Getenv("PITH_SDK_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := fakeModel()
	model.Id = id
	model.Name = id
	model.BaseUrl = baseURL
	return model
}

// fakeStream returns a scripted offline stream that emits one assistant text.
func fakeStream(text string) agenttypes.StreamFn {
	return func(model *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		stream := aitypes.NewAssistantMessageEventStream()
		message := aitypes.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
		message.Content = []aitypes.ContentBlock{aitypes.TextBlock(text)}
		message.StopReason = aitypes.StopReasonStop
		message.Usage = aitypes.Usage{Input: 8, Output: 4, TotalTokens: 12}
		stream.Push(aitypes.NewDoneEvent(aitypes.StopReasonStop, message))
		return stream
	}
}
