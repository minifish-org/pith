// Integration conformance for the Pi 1.0 SDK delivery.
//
// These tests prove that the independently delivered packages compose through
// the frozen CreateAgentSession entry point: the V1/mixed model catalog, the
// native MCP client, the embedded CGO-free Codemode sandbox and virtual model
// routing all participate in a single session run. Everything runs offline with
// fake provider streams and in-process transports; no paid provider, no network
// egress and no Node.js runtime are involved.
//
// The sandbox test documents the pure-Go dependency guarantee: the acceptance
// gate compiles and runs with CGO_ENABLED=0, so a passing Codemode execution is
// direct evidence that the embedded quickjs-wasi/wazero chain needs no cgo.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package pi_v1_sdk_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/ai/providers"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/codemode"
	cruntime "github.com/minifish-org/pith/packages/codemode/runtime"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"github.com/minifish-org/pith/packages/mcp"
)

// TestPiV1SDKIntegrationComposition drives one agent run in which the selected
// model is virtual, the tool the model calls is the embedded Codemode tool and
// the script it runs calls a tool backed by the native MCP client. The catalog
// that resolves the models is the same mixed V1 release catalog.
func TestPiV1SDKIntegrationComposition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Mixed V1 catalog: one collection exposes chat and classifier records.
	models := providers.BuiltinModels(nil)
	if flash := models.GetModel("deepseek", "deepseek-flash"); flash == nil || !flash.Reasoning {
		t.Fatalf("V1 chat catalog missing from builtin models: %+v", flash)
	}
	if classifier := models.GetModelOfType(aitypes.ModelTypeClassifier, "typesafe", "jev-latest"); classifier == nil || classifier.Classifier == nil {
		t.Fatal("classifier record missing from the mixed builtin catalog")
	}

	// 2. Native MCP client over an in-process transport.
	mcpClient, closeServer := newIntegrationMCPServer(t)
	defer closeServer()
	if err := mcpClient.Connect(ctx); err != nil {
		t.Fatalf("mcp connect: %v", err)
	}
	defer mcpClient.Close(context.Background())
	tools, err := mcpClient.ListTools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("mcp tools = %+v err = %v", tools, err)
	}

	var mcpCalls atomic.Int32
	mcpBacked := sdk.ToolDefinition{
		Name:        "mcp_echo",
		Description: "Echo text through the native MCP client.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Execute: func(ctx context.Context, arguments json.RawMessage) (sdk.ToolResult, error) {
			mcpCalls.Add(1)
			var input struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(arguments, &input); err != nil {
				return sdk.ToolResult{}, err
			}
			result, err := mcpClient.CallTool(ctx, "echo", map[string]any{"text": input.Text})
			if err != nil {
				return sdk.ToolResult{}, err
			}
			return sdk.ToolResult{
				Content:           mcp.ToAIContent(result),
				StructuredContent: result.StructuredContent,
				IsError:           result.IsError,
			}, nil
		},
	}
	registry, err := sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{mcpBacked}, nil, nil, sdk.ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	defer registry.CloseTools()

	// 3. Embedded Codemode tool over the same registry. Constructed after the
	// MCP-backed tool so the script can call it as tools.mcp_echo.
	codemodeTool, err := sdk.NewCodemodeTool(registry, &codemode.SandboxOptions{Timeout: 2 * time.Second, MemoryLimitBytes: 16 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(codemodeTool); err != nil {
		t.Fatal(err)
	}

	// 4. Virtual routing: the selected entry is virtual and must be replaced by
	// a physical model before the provider stream sees it.
	physical := aitypes.Model{
		Id: "physical", Name: "Physical",
		Api: aitypes.ApiOpenAICompletions, Provider: aitypes.ProviderOpenAI,
		BaseUrl: "http://localhost.invalid/v1", ContextWindow: 8192, MaxTokens: 64,
	}
	var mu sync.Mutex
	var reasons []string
	definition := sdk.VirtualModelDefinition{
		Provider: "integration-router", ID: "choose", Name: "Choose",
		ContextWindow: 8192, MaxTokens: 64,
		Route: func(_ context.Context, request sdk.ModelRouteRequest) (sdk.ModelRoute, error) {
			mu.Lock()
			reasons = append(reasons, request.Reason)
			mu.Unlock()
			return sdk.ModelRoute{
				Model:         physical,
				ThinkingLevel: aitypes.ThinkingOff,
				State:         json.RawMessage(`{"turns":1}`),
			}, nil
		},
	}
	virtual := sdk.CreateVirtualModel(definition)
	if !sdk.IsVirtualModel(&virtual) {
		t.Fatal("virtual entry lost its api")
	}

	turns := 0
	stream := func(model *aitypes.Model, _ *aitypes.TranscriptContext, _ *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		if model.Api == sdk.VirtualModelAPI {
			t.Errorf("virtual model reached the provider stream")
		}
		turns++
		if turns == 1 {
			return integrationDone(aitypes.StopReasonToolUse,
				integrationToolCall("call-1", sdk.CodemodeToolName, `{"code":"const r = await tools.mcp_echo({text: \"composed\"}); return r.echo;"}`))
		}
		return integrationDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
	}

	session, err := sdk.CreateAgentSession(sdk.SessionOptions{
		Cwd:           t.TempDir(),
		Model:         sdk.ModelOptions{Model: &virtual, StreamFn: stream},
		Tools:         registry,
		VirtualModels: []sdk.VirtualModelDefinition{definition},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.Prompt(ctx, "compose every delivered package")
	if err != nil || result.StopReason != aitypes.StopReasonStop {
		t.Fatalf("composed prompt failed: %+v %v", result, err)
	}
	if calls := mcpCalls.Load(); calls != 1 {
		t.Fatalf("MCP-backed tool calls = %d, want 1", calls)
	}
	mu.Lock()
	gotReasons := append([]string(nil), reasons...)
	mu.Unlock()
	if len(gotReasons) == 0 || gotReasons[0] != sdk.ModelRouteReasonUser {
		t.Fatalf("virtual route reasons = %v", gotReasons)
	}
}

// TestPiV1SDKPureGoSandbox confirms the embedded sandbox dependency chain is
// usable in a CGO-disabled build. The acceptance gate sets CGO_ENABLED=0, so a
// successful decode plus execution below is direct evidence that the immutable
// quickjs-wasi asset and the pinned wazero runtime need no cgo.
func TestPiV1SDKPureGoSandbox(t *testing.T) {
	wasm, err := cruntime.QuickJSWasmBytes()
	if err != nil {
		t.Fatalf("decode embedded quickjs-wasi asset: %v", err)
	}
	if !bytes.HasPrefix(wasm, []byte("\x00asm")) {
		t.Fatal("embedded quickjs-wasi asset is not a WebAssembly module")
	}

	sandbox, err := codemode.NewSandbox(codemode.SandboxOptions{Timeout: 2 * time.Second, MemoryLimitBytes: 16 << 20})
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	defer sandbox.Close(context.Background())

	result := sandbox.Execute(context.Background(), `return 40 + 2;`, codemode.ExecuteOptions{})
	if !result.OK || string(result.Value) != "42" {
		t.Fatalf("sandbox execution under CGO-disabled build failed: %+v", result)
	}
}

// newIntegrationMCPServer pairs an MCP client with a minimal in-process server.
func newIntegrationMCPServer(t *testing.T) (*mcp.Client, func()) {
	t.Helper()
	clientTransport, serverTransport := mcp.CreateInMemoryTransportPair()
	if err := serverTransport.Start(); err != nil {
		t.Fatal(err)
	}
	serverTransport.OnMessage(func(message *mcp.Message) {
		if !message.IsRequest() {
			return
		}
		go func() {
			var result any
			switch message.Method {
			case "initialize":
				result = map[string]any{
					"protocolVersion": mcp.LatestProtocolVersion,
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "integration-server", "version": "1"},
				}
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{
					"name":        "echo",
					"description": "Echo the supplied text.",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
				}}}
			case "tools/call":
				var params struct {
					Arguments map[string]any `json:"arguments"`
				}
				_ = json.Unmarshal(message.Params, &params)
				text := fmt.Sprint(params.Arguments["text"])
				result = map[string]any{
					"content":           []any{map[string]any{"type": "text", "text": text}},
					"structuredContent": map[string]any{"echo": text, "length": len(text)},
				}
			default:
				_ = serverTransport.Send(mcp.NewErrorMessage(*message.ID, &mcp.ErrorObject{
					Code:    mcp.JSONRPCCodeMethodNotFound,
					Message: "method not found: " + message.Method,
				}))
				return
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				encoded = []byte("{}")
			}
			_ = serverTransport.Send(mcp.NewResultMessage(*message.ID, encoded))
		}()
	})
	client := mcp.NewClient(clientTransport, &mcp.ClientOptions{Name: "integration", Version: "1", RequestTimeout: 2 * time.Second})
	return client, func() { _ = clientTransport.Close() }
}

// integrationDone builds a terminal assistant message stream.
func integrationDone(reason aitypes.StopReason, blocks ...aitypes.ContentBlock) *aitypes.AssistantMessageEventStream {
	stream := aitypes.NewAssistantMessageEventStream()
	message := aitypes.NewAssistantMessage(aitypes.ApiOpenAICompletions, aitypes.ProviderOpenAI, "physical", 1)
	message.Content = blocks
	message.StopReason = reason
	switch reason {
	case aitypes.StopReasonError:
		text := "integration provider failure"
		message.ErrorMessage = &text
		stream.Push(aitypes.NewErrorEvent(reason, message))
	default:
		stream.Push(aitypes.NewDoneEvent(reason, message))
	}
	return stream
}

// integrationToolCall builds a tool-call content block.
func integrationToolCall(id, name, arguments string) aitypes.ContentBlock {
	call := aitypes.NewToolCall(id, name, json.RawMessage(arguments))
	return aitypes.ContentBlock{Type: aitypes.ContentTypeToolCall, ToolCall: &call}
}
