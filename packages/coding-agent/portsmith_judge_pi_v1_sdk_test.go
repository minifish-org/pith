package codingagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/codemode"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPortsmithJudgePiV1VirtualRouting(t *testing.T) {
	physical := types.Model{Id: "physical", Name: "Physical", Api: types.ApiOpenAICompletions, Provider: types.ProviderOpenAI, ContextWindow: 8192, MaxTokens: 32}
	var mu sync.Mutex
	reasons := []string{}
	states := []string{}
	requests := []string{}
	def := sdk.VirtualModelDefinition{Provider: "router", ID: "choose", Name: "Choose", ContextWindow: 8192, MaxTokens: 32, Route: func(ctx context.Context, request sdk.ModelRouteRequest) (sdk.ModelRoute, error) {
		mu.Lock()
		defer mu.Unlock()
		reasons = append(reasons, request.Reason)
		states = append(states, string(request.State))
		return sdk.ModelRoute{Model: physical, ThinkingLevel: types.ThinkingHigh, State: json.RawMessage(`{"turn":1}`)}, nil
	}}
	virtual := sdk.CreateVirtualModel(def)
	if virtual.Api != "pi-virtual" {
		t.Fatal("virtual entry has physical api")
	}
	options := sdk.SessionOptions{Cwd: t.TempDir(), Model: sdk.ModelOptions{Model: &virtual}, VirtualModels: []sdk.VirtualModelDefinition{def}}
	options.Model.StreamFn = func(model *types.Model, tr *types.TranscriptContext, o *types.SimpleStreamOptions) *types.AssistantMessageEventStream {
		mu.Lock()
		requests = append(requests, model.Id)
		mu.Unlock()
		s := types.NewAssistantMessageEventStream()
		m := types.NewAssistantMessage(model.Api, model.Provider, model.Id, 1)
		m.StopReason = types.StopReasonStop
		m.Content = []types.ContentBlock{types.TextBlock("done")}
		s.Push(types.NewDoneEvent(m.StopReason, m))
		return s
	}
	session, err := sdk.CreateAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		result, err := session.Prompt(ctx, "hello")
		if err != nil || result.StopReason != types.StopReasonStop {
			t.Fatalf("routed prompt failed %+v %v", result, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(requests, []string{"physical", "physical"}) || !reflect.DeepEqual(reasons, []string{"user", "user"}) {
		t.Fatal("virtual reached provider or route reason lost", requests, reasons)
	}
	if len(states) != 2 || states[0] != "" || states[1] != `{"turn":1}` {
		t.Fatal("session router state not retained", states)
	}
}

func TestPortsmithJudgePiV1CodemodePermission(t *testing.T) {
	var executed, before atomic.Int32
	custom := sdk.ToolDefinition{Name: "private", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(ctx context.Context, args json.RawMessage) (sdk.ToolResult, error) {
		executed.Add(1)
		return sdk.ToolResult{Content: []types.ContentBlock{types.TextBlock("secret")}, StructuredContent: json.RawMessage(`{"secret":true}`)}, nil
	}}
	registry, err := sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{custom}, []string{"private"}, nil, sdk.ToolHooks{Before: func(ctx context.Context, call sdk.ToolCall) error {
		before.Add(1)
		return errors.New("denied by judge")
	}})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := sdk.NewCodemodeTool(registry, &codemode.SandboxOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := cm.Execute(ctx, json.RawMessage(`{"code":"return await tools.private({});"}`))
	if executed.Load() != 0 || before.Load() != 1 {
		t.Fatal("nested Codemode bypassed permission hooks", executed.Load(), before.Load())
	}
	if err == nil && !result.IsError {
		t.Fatal("denied nested tool reported success")
	}
}

func TestPortsmithJudgePiV1DeferredTools(t *testing.T) {
	var executed atomic.Int32
	tool := sdk.ToolDefinition{Name: "deferred_lookup", Exposure: "deferred", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(ctx context.Context, args json.RawMessage) (sdk.ToolResult, error) {
		executed.Add(1)
		return sdk.ToolResult{StructuredContent: json.RawMessage(`{"value":42}`)}, nil
	}}
	registry, err := sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{tool}, nil, nil, sdk.ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range registry.Declarations() {
		if declaration.Name == tool.Name {
			t.Fatal("deferred tool declared before loading")
		}
	}
	cm, err := sdk.NewCodemodeTool(registry, &codemode.SandboxOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := cm.Execute(context.Background(), json.RawMessage(`{"code":"const result = await tools.deferred_lookup({}); if(result.value !== 42) throw new Error('structured content missing'); return result.value;"}`))
	if err != nil || result.IsError || executed.Load() != 1 {
		t.Fatalf("deferred structured nested call failed: %+v %v; calls %d", result, err, executed.Load())
	}
	denied, err := sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{tool}, nil, []string{tool.Name}, sdk.ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	cm, err = sdk.NewCodemodeTool(denied, &codemode.SandboxOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = cm.Execute(context.Background(), json.RawMessage(`{"code":"return await tools.deferred_lookup({});"}`))
	if executed.Load() != 1 {
		t.Fatal("deny list bypassed by deferred tool exposure")
	}
}
