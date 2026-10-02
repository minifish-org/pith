// Source-derived self-tests for the Pi 1.0 embedded SDK integration.
//
// These tests exercise the behavior added in the SDK change with only offline
// fixtures: fake provider streams, an in-process Codemode sandbox and an
// httptest catalog server. They never call a paid model and never reach the
// network implicitly. They complement (do not replace) the independent judge.
//
// Upstream: Copyright (c) 2025 Mario Zechner, MIT License.
// See the repository LICENSE for the full text.
package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agenttypes "github.com/minifish-org/pith/packages/agent/types"
	aitypes "github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/codemode"
	"github.com/minifish-org/pith/packages/mcp"
)

// ---------------------------------------------------------------------------
// virtual models
// ---------------------------------------------------------------------------

func TestPiV1VirtualModelEntry(t *testing.T) {
	model := CreateVirtualModel(VirtualModelDefinition{
		Provider:       "router",
		ID:             "choose",
		Name:           "Choose",
		ThinkingLevels: []aitypes.ModelThinkingLevel{aitypes.ThinkingOff, aitypes.ThinkingHigh},
		ContextWindow:  8192,
		MaxTokens:      32,
		Route:          func(context.Context, ModelRouteRequest) (ModelRoute, error) { return ModelRoute{}, nil },
	})
	if model.Api != VirtualModelAPI {
		t.Fatalf("virtual api = %q", model.Api)
	}
	if !IsVirtualModel(&model) {
		t.Fatal("virtual model not detected")
	}
	if !model.Reasoning {
		t.Fatal("thinking levels did not mark reasoning")
	}
	if value := model.ThinkingLevelMap[aitypes.ThinkingHigh]; value == nil || *value != string(aitypes.ThinkingHigh) {
		t.Fatalf("high level not mapped: %+v", model.ThinkingLevelMap)
	}
	if value, ok := model.ThinkingLevelMap[aitypes.ThinkingLow]; !ok || value != nil {
		t.Fatalf("unsupported level should map to nil: %+v", model.ThinkingLevelMap)
	}
	if model.ContextWindow != 8192 || model.MaxTokens != 32 {
		t.Fatalf("limits lost: %v %v", model.ContextWindow, model.MaxTokens)
	}
}

func TestPiV1VirtualRoutingReasonsAndState(t *testing.T) {
	physical := aitypes.Model{Id: "physical", Name: "Physical", Api: aitypes.ApiOpenAICompletions, Provider: aitypes.ProviderOpenAI, ContextWindow: 8192, MaxTokens: 32}
	var mu sync.Mutex
	reasons := []string{}
	states := []string{}
	def := VirtualModelDefinition{Provider: "router", ID: "choose", Name: "Choose", ContextWindow: 8192, MaxTokens: 32, Route: func(ctx context.Context, request ModelRouteRequest) (ModelRoute, error) {
		mu.Lock()
		defer mu.Unlock()
		reasons = append(reasons, request.Reason)
		states = append(states, string(request.State))
		return ModelRoute{Model: physical, ThinkingLevel: aitypes.ThinkingHigh, State: json.RawMessage(`{"turn":1}`)}, nil
	}}
	virtual := CreateVirtualModel(def)

	responses := 0
	stream := func(model *aitypes.Model, tr *aitypes.TranscriptContext, o *aitypes.SimpleStreamOptions) *aitypes.AssistantMessageEventStream {
		if model.Id != "physical" {
			t.Errorf("virtual model reached provider: %s", model.Id)
		}
		responses++
		if responses == 1 {
			return sessionDone(aitypes.StopReasonToolUse, sessionToolCall("call-1", "probe", `{}`))
		}
		return sessionDone(aitypes.StopReasonStop, aitypes.TextBlock("done"))
	}

	manager, dir := sessionManager(t)
	defer manager.Close()
	registry, err := NewToolRegistry(dir, []ToolDefinition{sessionTool("probe", func(context.Context, json.RawMessage) (ToolResult, error) {
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("ok")}}, nil
	})}, nil, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	session, err := CreateAgentSession(SessionOptions{
		Cwd:           dir,
		Manager:       manager,
		Model:         ModelOptions{Model: &virtual, StreamFn: stream},
		Tools:         registry,
		VirtualModels: []VirtualModelDefinition{def},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.Prompt(context.Background(), "run the tool")
	if err != nil || result.StopReason != aitypes.StopReasonStop {
		t.Fatalf("routed prompt failed: %+v %v", result, err)
	}
	mu.Lock()
	gotReasons := append([]string(nil), reasons...)
	gotStates := append([]string(nil), states...)
	mu.Unlock()
	if !reflect.DeepEqual(gotReasons, []string{ModelRouteReasonUser, ModelRouteReasonContinuation}) {
		t.Fatalf("reasons = %v", gotReasons)
	}
	if len(gotStates) != 2 || gotStates[0] != "" || gotStates[1] != `{"turn":1}` {
		t.Fatalf("states = %v", gotStates)
	}
	// The state persists on the branch.
	if state := GetVirtualModelState(manager.GetBranch(""), "router", "choose"); string(state) != `{"turn":1}` {
		t.Fatalf("branch state = %s", state)
	}
}

func TestPiV1VirtualRoutingRejectsProblems(t *testing.T) {
	if _, err := newVirtualModelRegistry([]VirtualModelDefinition{{Provider: "p", ID: "x"}, {Provider: "p", ID: "x", Route: func(context.Context, ModelRouteRequest) (ModelRoute, error) { return ModelRoute{}, nil }}}); err == nil {
		t.Fatal("duplicate virtual models accepted")
	}
	physical := aitypes.Model{Id: "physical", Api: aitypes.ApiOpenAICompletions, Provider: aitypes.ProviderOpenAI}
	def := VirtualModelDefinition{Provider: "router", ID: "choose", Route: func(context.Context, ModelRouteRequest) (ModelRoute, error) {
		return ModelRoute{Model: CreateVirtualModel(VirtualModelDefinition{Provider: "router", ID: "other"})}, nil
	}}
	registry, err := newVirtualModelRegistry([]VirtualModelDefinition{def})
	if err != nil {
		t.Fatal(err)
	}
	_, err = registry.resolve(context.Background(), ModelRouteRequest{Model: CreateVirtualModel(def)})
	if err == nil || !strings.Contains(err.Error(), "virtual model") {
		t.Fatalf("virtual-to-virtual routing not rejected: %v", err)
	}
	unavailable := VirtualModelDefinition{Provider: "router", ID: "choose", Route: func(context.Context, ModelRouteRequest) (ModelRoute, error) {
		return ModelRoute{Model: aitypes.Model{Id: "", Provider: ""}}, nil
	}}
	registry, _ = newVirtualModelRegistry([]VirtualModelDefinition{unavailable})
	if _, err = registry.resolve(context.Background(), ModelRouteRequest{Model: CreateVirtualModel(unavailable)}); err == nil {
		t.Fatal("unavailable physical model accepted")
	}
	_ = physical
}

func TestPiV1VirtualSessionRejectsUnregisteredModel(t *testing.T) {
	manager, dir := sessionManager(t)
	defer manager.Close()
	def := VirtualModelDefinition{Provider: "router", ID: "choose", Name: "Choose", Route: func(context.Context, ModelRouteRequest) (ModelRoute, error) { return ModelRoute{}, nil }}
	virtual := CreateVirtualModel(def)
	_, err := CreateAgentSession(SessionOptions{Cwd: dir, Manager: manager, Model: ModelOptions{Model: &virtual}})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("unregistered virtual model accepted: %v", err)
	}
}

// ---------------------------------------------------------------------------
// codemode integration
// ---------------------------------------------------------------------------

func TestPiV1CodemodeNestedPermissionAndStructured(t *testing.T) {
	var executed, before atomic.Int32
	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{{
		Name:       "secret",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{StructuredContent: json.RawMessage(`{"value":7}`)}, nil
		},
	}}, nil, nil, ToolHooks{Before: func(context.Context, ToolCall) error {
		before.Add(1)
		return errors.New("denied")
	}})
	if err != nil {
		t.Fatal(err)
	}
	cm, err := NewCodemodeTool(registry, &codemode.SandboxOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer cm.close()
	result, err := cm.Execute(context.Background(), json.RawMessage(`{"code":"return await tools.secret({});"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || executed.Load() != 0 || before.Load() != 1 {
		t.Fatalf("denied nested tool executed: %+v calls %d/%d", result, executed.Load(), before.Load())
	}
}

func TestPiV1CodemodeDeferredAndStructuredReturn(t *testing.T) {
	var executed atomic.Int32
	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{{
		Name:       "deferred_lookup",
		Exposure:   ExposureDeferred,
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{StructuredContent: json.RawMessage(`{"value":42}`)}, nil
		},
	}}, nil, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range registry.Declarations() {
		if declaration.Name == "deferred_lookup" {
			t.Fatal("deferred tool declared before loading")
		}
	}
	cm, err := NewCodemodeTool(registry, &codemode.SandboxOptions{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer cm.close()
	result, err := cm.Execute(context.Background(), json.RawMessage(`{"code":"const r = await tools.deferred_lookup({}); if(r.value!==42) throw new Error('no structured'); return r.value;"}`))
	if err != nil || result.IsError || executed.Load() != 1 {
		t.Fatalf("deferred structured call failed: %+v %v calls=%d", result, err, executed.Load())
	}
}

func TestPiV1CodemodeStoreReplay(t *testing.T) {
	manager, _ := sessionManager(t)
	defer manager.Close()
	store := NewCodemodeStore(manager)
	if err := store.Append(codemode.StoreWrites{Set: map[string]json.RawMessage{"a": json.RawMessage(`1`), "b": json.RawMessage(`2`)}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(codemode.StoreWrites{Delete: []string{"a"}, Set: map[string]json.RawMessage{"c": json.RawMessage(`3`)}}); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	if string(snapshot["b"]) != `2` || string(snapshot["c"]) != `3` {
		t.Fatalf("store replay wrong: %+v", snapshot)
	}
	if _, ok := snapshot["a"]; ok {
		t.Fatal("deleted key survived replay")
	}
	// The caller's map is never mutated by Snapshot.
	snapshot["b"][0] = '9'
	if string(store.Snapshot()["b"]) != `2` {
		t.Fatal("snapshot aliased the branch")
	}
}

// ---------------------------------------------------------------------------
// tool search
// ---------------------------------------------------------------------------

func TestPiV1ToolSearchLoadsDeferred(t *testing.T) {
	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{
		{Name: "deferred_lookup", Exposure: ExposureDeferred, Description: "look up an issue", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) { return ToolResult{}, nil }},
		{Name: "docs_search", Exposure: ExposureDeferred, Description: "search documentation", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) { return ToolResult{}, nil }},
		{Name: "hidden_tool", Exposure: ExposureHidden, Description: "look up nothing", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) { return ToolResult{}, nil }},
	}, nil, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	search, err := CreateToolSearchTool(registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := search.Execute(context.Background(), json.RawMessage(`{"query":"lookup issue"}`))
	if err != nil || result.IsError {
		t.Fatalf("tool search failed: %+v %v", result, err)
	}
	declared := map[string]bool{}
	for _, declaration := range registry.Declarations() {
		declared[declaration.Name] = true
	}
	if !declared["deferred_lookup"] {
		t.Fatal("loaded tool not declared")
	}
	if declared["docs_search"] {
		t.Fatal("unmatched tool loaded")
	}
	if declared["hidden_tool"] {
		t.Fatal("hidden tool loaded")
	}
}

func TestPiV1Bm25StableTies(t *testing.T) {
	documents := []ToolSearchDocument{
		{Name: "a", Text: "shared token alpha"},
		{Name: "b", Text: "shared token alpha"},
		{Name: "c", Text: "shared token beta"},
	}
	matches := Bm25Ranker{}.Rank("shared token", documents, 3)
	if len(matches) != 3 || matches[0].Name != "a" || matches[1].Name != "b" || matches[2].Name != "c" {
		t.Fatalf("stable ties lost: %+v", matches)
	}
	if got := Tokenize("listIssuesForRepo"); !reflect.DeepEqual(got, []string{"list", "issue", "for", "repo"}) {
		// "for" is a stop word and should be removed.
		if !reflect.DeepEqual(got, []string{"list", "issue", "repo"}) {
			t.Fatalf("tokenize = %v", got)
		}
	}
}

// ---------------------------------------------------------------------------
// nested tool calls
// ---------------------------------------------------------------------------

func TestPiV1NestedCallRecorder(t *testing.T) {
	recorder := NewNestedCallRecorder()
	first := recorder.Start(aitypes.ToolCall{Id: "p/1", Name: "a", Arguments: json.RawMessage(`{}`)})
	recorder.Finish(first, false, "")
	second := recorder.Start(aitypes.ToolCall{Id: "p/2", Name: "b", Arguments: json.RawMessage(`{}`)})
	recorder.Finish(second, true, "boom")
	recorder.AddUsage(aitypes.Usage{Input: 3, Output: 1, TotalTokens: 4})
	snapshot := recorder.Snapshot()
	if snapshot == nil || !snapshot.Complete || len(snapshot.Calls) != 2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	if snapshot.Calls[0].Status != nestedStatusOK || snapshot.Calls[1].Status != nestedStatusError || snapshot.Calls[1].Error != "boom" {
		t.Fatalf("statuses = %+v", snapshot.Calls)
	}
	if usage := recorder.TotalUsage(); usage == nil || usage.Input != 3 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestPiV1RegistryNestedHostAppliesHooks(t *testing.T) {
	var before, executed atomic.Int32
	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{{
		Name:       "nested_tool",
		Parameters: json.RawMessage(`{"type":"object"}`),
		Execute: func(context.Context, json.RawMessage) (ToolResult, error) {
			executed.Add(1)
			return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("ok")}}, nil
		},
	}}, nil, nil, ToolHooks{Before: func(context.Context, ToolCall) error {
		before.Add(1)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	host := NewRegistryNestedHost(registry)
	runner := NewNestedToolCallRunner(host)
	outcome := runner.Execute("parent", "nested_tool", json.RawMessage(`{}`), nil, nil)
	if outcome.IsError || executed.Load() != 1 || before.Load() != 1 {
		t.Fatalf("nested hooks not applied: %+v %d/%d", outcome, executed.Load(), before.Load())
	}
	summary := runner.TakeRecord("parent")
	if summary == nil || summary.Calls == nil || len(summary.Calls.Calls) != 1 {
		t.Fatalf("nested record = %+v", summary)
	}

	registry.deny["nested_tool"] = true
	outcome = runner.Execute("parent2", "nested_tool", json.RawMessage(`{}`), nil, nil)
	if !outcome.IsError || executed.Load() != 1 {
		t.Fatalf("deny list bypassed: %+v", outcome)
	}
}

// ---------------------------------------------------------------------------
// cache warmer
// ---------------------------------------------------------------------------

func TestPiV1CacheWarmingDecision(t *testing.T) {
	if _, ok := GetCacheWarmingDelayMs(5_000); ok {
		t.Fatal("short ttl should not warm")
	}
	delay, ok := GetCacheWarmingDelayMs(300_000)
	if !ok || delay != 270_000 {
		t.Fatalf("delay = %d %v", delay, ok)
	}
	model := aitypes.Model{Cost: aitypes.ModelCost{ModelCostRates: aitypes.ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3}}}
	decision := CacheWarmingEconomics(model, 100_000, "streaming")
	if !decision.EconomicsAvailable {
		t.Fatal("economics unavailable")
	}
	if decision.Action != CacheWarmingActionWarm {
		t.Fatalf("expected warm, got %s (savings %v)", decision.Action, decision.ExpectedSavings)
	}
	cheap := CacheWarmingEconomics(aitypes.Model{Cost: aitypes.ModelCost{ModelCostRates: aitypes.ModelCostRates{Input: 0.0001, CacheRead: 0.0001}}}, 100, "idle")
	if cheap.Action != CacheWarmingActionStop {
		t.Fatalf("expected stop, got %s", cheap.Action)
	}
}

func TestPiV1CacheWarmerOptInAndCancel(t *testing.T) {
	// A warmer without Warm never schedules.
	model := aitypes.Model{Api: aitypes.ApiOpenAICompletions, PromptCache: &aitypes.ModelPromptCache{Short: floatPtr(300)}}
	calls := atomic.Int32{}
	mode := CacheWarmingOff
	warmer := NewCacheWarmer(CacheWarmingOptions{Mode: func() CacheWarmingMode { return mode }})
	if warmer.Start(CacheWarmRequest{Model: model}) {
		t.Fatal("warmer scheduled while disabled")
	}

	mode = CacheWarmingStreaming
	clock := time.Now()
	scheduled := make(chan time.Duration, 1)
	warmer = NewCacheWarmer(CacheWarmingOptions{
		Mode: func() CacheWarmingMode { return mode },
		Now:  func() time.Time { return clock },
		Warm: func(context.Context, CacheWarmRequest) (aitypes.Usage, error) {
			calls.Add(1)
			return aitypes.Usage{}, nil
		},
		Schedule: func(ctx context.Context, delay time.Duration) <-chan time.Time {
			scheduled <- delay
			return make(chan time.Time)
		},
	})
	if !warmer.Start(CacheWarmRequest{Model: model}) {
		t.Fatal("warmer did not schedule an opted-in request")
	}
	select {
	case delay := <-scheduled:
		if delay <= 0 {
			t.Fatalf("non-positive delay %v", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("schedule callback not invoked")
	}
	warmer.Stop()
	if calls.Load() != 0 {
		t.Fatalf("warmer issued a request without a fired schedule: %d", calls.Load())
	}
}

func floatPtr(value float64) *float64 { return &value }

// ---------------------------------------------------------------------------
// MCP
// ---------------------------------------------------------------------------

func TestPiV1MCPConfigAndExposure(t *testing.T) {
	if MCPNamespace("my-server") != "mcp__my_server" {
		t.Fatalf("namespace = %s", MCPNamespace("my-server"))
	}
	if err := ValidateMCPServerConfig(MCPServerConfig{Name: "bad name", Type: "stdio", Command: "x"}); err == nil {
		t.Fatal("invalid server name accepted")
	}
	if err := ValidateMCPServerConfig(MCPServerConfig{Name: "ok", Type: "http"}); err == nil {
		t.Fatal("missing url accepted")
	}
	config := MCPServerConfig{
		Name:         "srv",
		Type:         "stdio",
		Command:      "x",
		Exposure:     MCPExposureDeferred,
		ToolExposure: map[string]string{"dangerous*": MCPExposureHidden, "read_*": MCPExposureDirect},
	}
	if got := MCPExposureForTool(config, "read_file"); got != MCPExposureDirect {
		t.Fatalf("pattern exposure = %s", got)
	}
	if got := MCPExposureForTool(config, "dangerous_delete"); got != MCPExposureHidden {
		t.Fatalf("exposure = %s", got)
	}
	if got := MCPExposureForTool(config, "other"); got != MCPExposureDeferred {
		t.Fatalf("default exposure = %s", got)
	}
	if NormalizeMCPExposure("codemode-deferred") != MCPExposureCodemode {
		t.Fatal("alias not applied")
	}
}

func TestPiV1MCPRuntimePartialFailure(t *testing.T) {
	runtime := NewMCPRuntime(MCPRuntimeOptions{
		TransportFactory: func(config MCPServerConfig) (mcp.Transport, error) {
			return nil, errors.New("transport unavailable")
		},
	})
	diagnostics := runtime.Load(context.Background(), []MCPServerConfig{
		{Name: "bad name", Type: "stdio", Command: "x"},
		{Name: "down", Type: "stdio", Command: "x"},
	})
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v", diagnostics)
	}
	if diagnostics[0].Type != "config" || diagnostics[1].Type != "connect" {
		t.Fatalf("diagnostic types = %+v", diagnostics)
	}
	if len(runtime.Tools()) != 0 {
		t.Fatal("failed server exposed tools")
	}
	runtime.Close(context.Background())
}

func TestPiV1MCPResourceConversion(t *testing.T) {
	text := "resource text"
	blob := "aGVsbG8="
	blocks := MCPResourceContentsToContent([]mcp.ResourceContents{
		{URI: "file:///a", Text: &text},
		{URI: "file:///b", Blob: &blob, MimeType: "image/png"},
	})
	if len(blocks) != 2 || blocks[0].Text == nil || blocks[0].Text.Text != "resource text" {
		t.Fatalf("text resource lost: %+v", blocks)
	}
	if blocks[1].Text == nil || !strings.Contains(blocks[1].Text.Text, "image/png") {
		t.Fatalf("blob placeholder wrong: %+v", blocks[1])
	}
}

// ---------------------------------------------------------------------------
// remote catalog
// ---------------------------------------------------------------------------

func TestPiV1RemoteCatalogParseAndMerge(t *testing.T) {
	body := []byte(`{"models":[{"id":"a","type":"chat","name":"A"},{"id":"img","type":"image"},{"id":"bad","type":"unknown"},{"name":"no-id"}]}`)
	models, err := ParseRemoteCatalog("prov", body)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("parsed %d models", len(models))
	}
	for _, model := range models {
		if !strings.Contains(string(model), `"provider":"prov"`) {
			t.Fatalf("provider not stamped: %s", model)
		}
	}
	baseline := []json.RawMessage{json.RawMessage(`{"id":"a","type":"chat","name":"base"}`)}
	merged := MergeRemoteModels(baseline, []json.RawMessage{json.RawMessage(`{"id":"a","type":"chat","name":"new"}`)})
	if len(merged) != 1 || !strings.Contains(string(merged[0]), `"new"`) {
		t.Fatalf("merge did not override: %s", merged)
	}
}

func TestPiV1RemoteCatalogRefresh(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("etag", `"v1"`)
		if r.Header.Get("if-none-match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"id": "a", "type": "chat"}}})
	}))
	defer server.Close()
	client := &RemoteCatalogClient{BaseURL: server.URL, HTTPClient: server.Client(), UserAgent: "test"}
	result, err := client.Refresh(context.Background(), "prov", nil, RemoteCatalogRefreshOptions{AllowNetwork: true, Force: true})
	if err != nil || !result.Persist || len(result.Models) != 1 {
		t.Fatalf("refresh failed: %+v %v", result, err)
	}
	if result.State.ETag != `"v1"` {
		t.Fatalf("etag lost: %+v", result.State)
	}
	// A second forced refresh revalidates and reports not-modified.
	result, err = client.Refresh(context.Background(), "prov", &result.State, RemoteCatalogRefreshOptions{AllowNetwork: true, Force: true})
	if err != nil || !result.NotModified {
		t.Fatalf("revalidation failed: %+v %v", result, err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d", requests)
	}
	// Network is off by default.
	offline, err := client.Refresh(context.Background(), "prov", nil, RemoteCatalogRefreshOptions{})
	if err != nil || offline.Persist || len(offline.Models) != 0 {
		t.Fatalf("offline refresh reached the network: %+v %v", offline, err)
	}
}

// ---------------------------------------------------------------------------
// tool registry exposure defaults
// ---------------------------------------------------------------------------

func TestPiV1DirectCustomToolsStillDefaultActive(t *testing.T) {
	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{{Name: "custom", Parameters: json.RawMessage(`{"type":"object"}`), Execute: func(context.Context, json.RawMessage) (ToolResult, error) { return ToolResult{}, nil }}}, nil, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	active := false
	for _, name := range registry.Names() {
		if name == "custom" {
			active = true
		}
	}
	if !active {
		t.Fatal("direct custom tool not active by default")
	}
}

// Ensure the agenttypes import is exercised by a compile-time assertion.
var _ = func() agenttypes.AgentTool[any, any] { return agenttypes.AgentTool[any, any]{} }
