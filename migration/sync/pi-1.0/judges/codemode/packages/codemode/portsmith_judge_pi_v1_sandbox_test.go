package codemode_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/minifish-org/pith/packages/codemode"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func psV1Sandbox(t *testing.T, options codemode.SandboxOptions) *codemode.Sandbox {
	t.Helper()
	s, err := codemode.NewSandbox(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}
func psV1Value(t *testing.T, result codemode.Result) any {
	t.Helper()
	if !result.OK || result.Error != nil {
		t.Fatalf("script failed: %+v", result)
	}
	var v any
	if err := json.Unmarshal(result.Value, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestPortsmithJudgePiV1CodemodeAsync(t *testing.T) {
	var called atomic.Int32
	tools := []codemode.Tool{{Name: "double-value", InputSchema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`), Execute: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		called.Add(1)
		var a struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"value": 2 * a.N})
	}}, {Name: "fail", Execute: func(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("fixture tool failure")
	}}}
	s := psV1Sandbox(t, codemode.SandboxOptions{Tools: tools, Timeout: 2 * time.Second, MemoryLimitBytes: 32 << 20})
	result := s.Execute(context.Background(), `const a = await tools.double_value({n:21}); text("start"); console.log(a.value); const all = await Promise.allSettled([tools["double-value"]({n:1}),tools.fail({})]); return {answer:a.value,status:all.map(x=>x.status)};`, codemode.ExecuteOptions{})
	want := map[string]any{"answer": float64(42), "status": []any{"fulfilled", "rejected"}}
	if !reflect.DeepEqual(psV1Value(t, result), want) {
		t.Fatalf("async/JSON bridge wrong: %s", result.Value)
	}
	if called.Load() != 2 || len(result.Calls) != 3 {
		t.Fatal("tool calls not recorded exactly once", called.Load(), result.Calls)
	}
	if len(result.Output) != 2 || result.Output[0].Text != "start" || result.Output[1].Text != "42" {
		t.Fatal("output order lost", result.Output)
	}
}

func TestPortsmithJudgePiV1CodemodeIsolation(t *testing.T) {
	s := psV1Sandbox(t, codemode.SandboxOptions{Timeout: time.Second})
	result := s.Execute(context.Background(), `globalThis.testMarker = "secret"; return [typeof process,typeof require,typeof fetch,typeof setTimeout,typeof WebAssembly];`, codemode.ExecuteOptions{})
	if !reflect.DeepEqual(psV1Value(t, result), []any{"undefined", "undefined", "undefined", "undefined", "undefined"}) {
		t.Fatal("privileged globals exposed", string(result.Value))
	}
	result = s.Execute(context.Background(), `return typeof testMarker;`, codemode.ExecuteOptions{})
	if psV1Value(t, result) != "undefined" {
		t.Fatal("VM state leaked across executions")
	}
}

func TestPortsmithJudgePiV1CodemodeStore(t *testing.T) {
	s := psV1Sandbox(t, codemode.SandboxOptions{Timeout: time.Second})
	input := map[string]json.RawMessage{"a": json.RawMessage(`{"value":1}`), "delete": json.RawMessage(`7`)}
	result := s.Execute(context.Background(), `const a = load("a"); a.value=99; store("new",a); store("delete",undefined); return load("a").value;`, codemode.ExecuteOptions{Store: input})
	if psV1Value(t, result) != float64(1) || string(input["a"]) != `{"value":1}` {
		t.Fatal("store input aliasing")
	}
	if result.StoreWrites == nil || string(result.StoreWrites.Set["new"]) != `{"value":99}` || !reflect.DeepEqual(result.StoreWrites.Delete, []string{"delete"}) {
		t.Fatal("store write delta wrong", result.StoreWrites)
	}
	result = s.Execute(context.Background(), `store("bad",1);text("before failure");throw new Error("fixture");`, codemode.ExecuteOptions{Store: input})
	if result.OK || result.Error == nil || result.Error.Kind != "script" || result.StoreWrites != nil || len(result.Output) != 1 {
		t.Fatal("failure committed writes or lost preceding output", result)
	}
}

func TestPortsmithJudgePiV1CodemodeCancellation(t *testing.T) {
	s := psV1Sandbox(t, codemode.SandboxOptions{Timeout: 40 * time.Millisecond, MemoryLimitBytes: 8 << 20})
	start := time.Now()
	r := s.Execute(context.Background(), `while(true) {}`, codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Kind != "timeout" || time.Since(start) > 5*time.Second {
		t.Fatal("CPU loop escaped deadline", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = s.Execute(ctx, `return 1;`, codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Kind != "aborted" {
		t.Fatal("cancelled caller ignored", r)
	}
	r = s.Execute(context.Background(), `await new Promise(()=>{});`, codemode.ExecuteOptions{Timeout: time.Second})
	if r.OK || r.Error == nil || !strings.Contains(strings.ToLower(r.Error.Message), "can never settle") {
		t.Fatal("unresolvable promise not diagnosed", r)
	}
	r = s.Execute(context.Background(), `return 42;`, codemode.ExecuteOptions{Timeout: time.Second})
	if psV1Value(t, r) != float64(42) {
		t.Fatal("deadline damaged sandbox reuse")
	}
}

func TestPortsmithJudgePiV1CodemodeDeclarations(t *testing.T) {
	if codemode.ToIdentifier("mcp.search-docs") != "mcp_search_docs" {
		t.Fatal("tool identifier normalization")
	}
	declarations, err := codemode.RenderDeclarations([]codemode.Tool{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`), OutputSchema: json.RawMessage(`{"type":"array","items":{"type":"number"}}`)}})
	if err != nil || !strings.Contains(declarations, "lookup") || !strings.Contains(declarations, "query") || !strings.Contains(declarations, "Promise") || !strings.Contains(declarations, "number") {
		t.Fatal("schema declarations unusable", declarations, err)
	}
}
