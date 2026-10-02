package pi_v1_sdk_test

import (
	"context"
	"github.com/minifish-org/pith/packages/ai/providers"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/codemode"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"github.com/minifish-org/pith/packages/mcp"
	"testing"
	"time"
)

func TestPortsmithJudgePiV1SDKComposition(t *testing.T) {
	models := providers.BuiltinModels(nil)
	if models.GetModel("deepseek", "deepseek-flash") == nil {
		t.Fatal("new catalog missing")
	}
	sandbox, err := codemode.NewSandbox(codemode.SandboxOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer sandbox.Close(context.Background())
	if result := sandbox.Execute(context.Background(), `return 42;`, codemode.ExecuteOptions{}); !result.OK || string(result.Value) != "42" {
		t.Fatal("embedded sandbox unavailable", result)
	}
	var _ *mcp.Client
	model := types.Model{Id: "fixture", Api: types.ApiOpenAICompletions, Provider: types.ProviderOpenAI, ContextWindow: 8192, MaxTokens: 32}
	options := sdk.SessionOptions{Cwd: t.TempDir(), Model: sdk.ModelOptions{Model: &model, StreamFn: func(m *types.Model, tr *types.TranscriptContext, o *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
		s := types.NewAssistantMessageEventStream()
		msg := types.NewAssistantMessage(m.Api, m.Provider, m.Id, 1)
		msg.StopReason = types.StopReasonStop
		s.Push(types.NewDoneEvent(msg.StopReason, msg))
		return s
	}}}
	session, err := sdk.CreateAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err = session.Prompt(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
}
