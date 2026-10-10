package codemode_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/codemode"
)

// ---- helpers ----

func newTestSandbox(t *testing.T, options codemode.SandboxOptions) *codemode.Sandbox {
	t.Helper()
	s, err := codemode.NewSandbox(options)
	if err != nil {
		t.Fatalf("NewSandbox: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func decodeValue(t *testing.T, result codemode.Result) any {
	t.Helper()
	if !result.OK || result.Error != nil {
		t.Fatalf("script failed: %+v", result)
	}
	var v any
	if err := json.Unmarshal(result.Value, &v); err != nil {
		t.Fatalf("unmarshal value %q: %v", string(result.Value), err)
	}
	return v
}

func echoTool() codemode.Tool {
	return codemode.Tool{Name: "echo", Execute: func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
		return args, nil
	}}
}

// ---- execution basics ----

func TestPiV1CodemodeReturnsJSONAndAwait(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second})

	r := s.Execute(context.Background(), `return { a: 1, b: [true, 'x'] }`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, map[string]any{"a": float64(1), "b": []any{true, "x"}}) {
		t.Fatalf("value round trip wrong: %#v", got)
	}
	if len(r.Output) != 0 || len(r.Calls) != 0 {
		t.Fatalf("unexpected output/calls: %+v %+v", r.Output, r.Calls)
	}
	if r.StoreWrites == nil || len(r.StoreWrites.Set) != 0 || len(r.StoreWrites.Delete) != 0 {
		t.Fatalf("successful run must report empty writes, got %+v", r.StoreWrites)
	}

	r = s.Execute(context.Background(), `const x = await Promise.resolve(41); return x + 1`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); got != float64(42) {
		t.Fatalf("top-level await wrong: %#v", got)
	}

	r = s.Execute(context.Background(), ``, codemode.ExecuteOptions{})
	if !r.OK || r.Value != nil {
		t.Fatalf("empty script should return undefined: ok=%v value=%q", r.OK, string(r.Value))
	}
}

func TestPiV1CodemodeOutputHelpers(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second})
	const png = "iVBORw0KGgo="
	const jpeg = "/9j/4A=="
	const gif = "R0lGODlh"
	const webp = "UklGRgAAAABXRUJQ"
	r := s.Execute(context.Background(), `
		console.log("hello", 1, { a: 1 });
		text({ json: true });
		text(undefined);
		text(7);
		image("data:image/png;base64,`+png+`");
		image({ image_url: "data:image/jpeg;base64,`+jpeg+`" });
		image({ type: "image", data: "`+gif+`", mimeType: "image/gif" });
		image("data:image/png;base64,`+webp+`");
		image({ type: "image", data: "`+png+`" });
		console.error(new Error("bad"));
		return null;
	`, codemode.ExecuteOptions{})
	if !r.OK {
		t.Fatalf("failed: %+v", r)
	}
	head := r.Output[:len(r.Output)-1]
	if len(head) != 9 {
		t.Fatalf("expected 9 leading output items, got %d: %+v", len(head), r.Output)
	}
	if head[0].Text != `hello 1 {"a":1}` || head[1].Text != `{"json":true}` || head[2].Text != "undefined" || head[3].Text != "7" {
		t.Fatalf("text output wrong: %+v", head[:4])
	}
	if head[4].Data != png || head[4].MimeType != "image/png" {
		t.Fatalf("declared data URI image wrong: %+v", head[4])
	}
	if head[5].Data != jpeg || head[5].MimeType != "image/jpeg" {
		t.Fatalf("image_url image wrong: %+v", head[5])
	}
	if head[6].Data != gif || head[6].MimeType != "image/gif" {
		t.Fatalf("mcp block image wrong: %+v", head[6])
	}
	if head[7].Data != webp || head[7].MimeType != "image/webp" {
		t.Fatalf("webp mime mismatch not corrected: %+v", head[7])
	}
	if head[8].Data != png || head[8].MimeType != "image/png" {
		t.Fatalf("mcp base64 image wrong: %+v", head[8])
	}
	if last := r.Output[len(r.Output)-1]; last.Type != "text" || !strings.HasPrefix(last.Text, "Error: bad") {
		t.Fatalf("console.error wrong: %+v", last)
	}
}

func TestPiV1CodemodeImageValidation(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second})
	r := s.Execute(context.Background(), `
		const errors = [];
		const circular = {};
		circular.self = circular;
		for (const run of [
			() => text(circular),
			() => image(""),
			() => image("https://example.com/a.png"),
			() => image("data:image/png,raw"),
			() => image({ type: "text", text: "x" }),
			() => image({ type: "image", data: "" }),
			() => image(42),
			() => image("data:image/png;base64,AAAA!"),
			() => image("data:image/png;base64,AAAAA"),
			() => image("data:image/png;base64,AA=A"),
			() => image("data:image/png;base64,"),
			() => image("data:image/png;base64,AAAA\n[Output truncated]"),
			() => image({ type: "image", data: "AAAA!", mimeType: "image/png" }),
			() => image("data:image/png;base64,AAAA"),
			() => image("data:image/png;base64,QUJD"),
			() => image("data:image/jpeg;base64,/9j/9w=="),
		]) {
			try { run(); errors.push("no error"); } catch (error) { errors.push(error.name + ": " + error.message); }
		}
		return errors;
	`, codemode.ExecuteOptions{})
	if !r.OK {
		t.Fatalf("failed: %+v", r)
	}
	if len(r.Output) != 0 {
		t.Fatalf("invalid images must not produce output: %+v", r.Output)
	}
	var got []string
	if err := json.Unmarshal(r.Value, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 16 {
		t.Fatalf("expected 16 errors, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "circular") {
		t.Fatalf("circular text error wrong: %q", got[0])
	}
	const expects = "TypeError: image expects a non-empty image URL string, an object with image_url, or a raw MCP image block"
	const invalidBase64 = "TypeError: invalid image output. The image data is not valid base64 (truncated or corrupted?)"
	const invalidSig = "TypeError: invalid image output. The image data is not a PNG, JPEG, GIF, or WebP image"
	want := []string{
		expects,
		"TypeError: remote image URLs are not supported in tool outputs. Pass a base64 data URI instead",
		"TypeError: invalid image output. Pass a base64 data URI instead",
		`TypeError: image only accepts MCP image blocks, got "text"`,
		"TypeError: image expected MCP image data",
		expects,
	}
	for i := 0; i < 6; i++ {
		want = append(want, invalidBase64)
	}
	for i := 0; i < 3; i++ {
		want = append(want, invalidSig)
	}
	if !reflect.DeepEqual(got[1:], want) {
		t.Fatalf("image errors wrong:\n got=%v\nwant=%v", got[1:], want)
	}
}

func TestPiV1CodemodeImageWrappedAndLarge(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 10 * time.Second})
	large := "iVBORw0KGgoA" + strings.Repeat("QUJD", 256*1024)
	r := s.Execute(context.Background(), `
		image("data:image/png;base64,iVBORw0K\r\nGgo=\n");
		image("data:image/png;base64,`+large+`");
	`, codemode.ExecuteOptions{})
	if !r.OK {
		t.Fatalf("failed: %+v", r)
	}
	if len(r.Output) != 2 || r.Output[0].Data != "iVBORw0KGgo=" || r.Output[1].Data != large {
		t.Fatalf("wrapped/large base64 wrong: %+v", r.Output)
	}
}

func TestPiV1CodemodeExit(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Tools: []codemode.Tool{echoTool()}})
	r := s.Execute(context.Background(), `
		text("before");
		store("k", 1);
		await tools.echo(1);
		try { exit(); } catch {}
		text("after");
		return "unreachable";
	`, codemode.ExecuteOptions{})
	if !r.OK || r.Value != nil {
		t.Fatalf("exit should succeed with undefined: %+v", r)
	}
	if len(r.Output) != 1 || r.Output[0].Text != "before" {
		t.Fatalf("exit lost output: %+v", r.Output)
	}
	if r.StoreWrites == nil || string(r.StoreWrites.Set["k"]) != "1" {
		t.Fatalf("exit lost store writes: %+v", r.StoreWrites)
	}
}

func TestPiV1CodemodeErrors(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second})

	r := s.Execute(context.Background(), "text(\"partial\");\nthrow new Error(\"boom\")", codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Kind != "script" || r.Error.Message != "boom" {
		t.Fatalf("thrown error wrong: %+v", r)
	}
	if len(r.Output) != 1 || r.Output[0].Text != "partial" {
		t.Fatalf("output before failure lost: %+v", r.Output)
	}

	r = s.Execute(context.Background(), "const a = 1;\nconst b = ;\nreturn a", codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Kind != "script" || r.Error.Name != "SyntaxError" {
		t.Fatalf("syntax error wrong: %+v", r.Error)
	}
	if !strings.Contains(r.Error.Stack, "codemode.js:2") {
		t.Fatalf("syntax stack missing line: %q", r.Error.Stack)
	}

	r = s.Execute(context.Background(), "throw { code: 7 }", codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Message != `{"code":7}` {
		t.Fatalf("non-error throw wrong: %+v", r.Error)
	}

	r = s.Execute(context.Background(), "return 10n", codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Name != "TypeError" {
		t.Fatalf("non-serializable return wrong: %+v", r.Error)
	}
}

func TestPiV1CodemodeStackFormat(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second})
	r := s.Execute(context.Background(), "console.log(new Error('inner'));\nthrow new RangeError('outer')", codemode.ExecuteOptions{})
	if r.OK || r.Error == nil {
		t.Fatalf("expected failure: %+v", r)
	}
	if r.Error.Name != "RangeError" || r.Error.Message != "outer" {
		t.Fatalf("error fields wrong: %+v", r.Error)
	}
	if !strings.HasPrefix(r.Error.Stack, "RangeError: outer\n    at ") || !strings.Contains(r.Error.Stack, "codemode.js:2") {
		t.Fatalf("stack format wrong: %q", r.Error.Stack)
	}
	if strings.Contains(r.Error.Stack, "codemode-prelude.js") {
		t.Fatalf("prelude frames leaked into stack: %q", r.Error.Stack)
	}
	if len(r.Output) != 1 || !strings.HasPrefix(r.Output[0].Text, "Error: inner") {
		t.Fatalf("console error output wrong: %+v", r.Output)
	}
	if strings.Contains(r.Output[0].Text, "codemode-prelude.js") {
		t.Fatalf("prelude frames leaked into output: %q", r.Output[0].Text)
	}
}

// ---- tools ----

func TestPiV1CodemodeToolsAsync(t *testing.T) {
	var called atomic.Int32
	seen := []string{}
	tools := []codemode.Tool{
		{Name: "double-value", InputSchema: json.RawMessage(`{"type":"object","properties":{"n":{"type":"number"}}}`), Execute: func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
			called.Add(1)
			seen = append(seen, string(args))
			var a struct {
				N int `json:"n"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"value": 2 * a.N})
		}},
		{Name: "fail", Execute: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("fixture tool failure")
		}},
	}
	s := newTestSandbox(t, codemode.SandboxOptions{Tools: tools, Timeout: 5 * time.Second})
	r := s.Execute(context.Background(), `
		const a = await tools.double_value({n:21});
		text("start");
		console.log(a.value);
		const all = await Promise.allSettled([tools["double-value"]({n:1}), tools.fail({})]);
		return {answer:a.value, status:all.map(x=>x.status)};
	`, codemode.ExecuteOptions{})
	got := decodeValue(t, r)
	want := map[string]any{"answer": float64(42), "status": []any{"fulfilled", "rejected"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("async/JSON bridge wrong: %#v", got)
	}
	if called.Load() != 2 || len(r.Calls) != 3 {
		t.Fatalf("calls: called=%d records=%+v", called.Load(), r.Calls)
	}
	if len(r.Output) != 2 || r.Output[0].Text != "start" || r.Output[1].Text != "42" {
		t.Fatalf("output order lost: %+v", r.Output)
	}
	statuses := []string{}
	for _, c := range r.Calls {
		statuses = append(statuses, c.Status)
	}
	if !reflect.DeepEqual(statuses, []string{"ok", "ok", "error"}) {
		t.Fatalf("call statuses wrong: %v", statuses)
	}
}

func TestPiV1CodemodeToolNormalization(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Tools: []codemode.Tool{
		{Name: "my-tool", Description: "Dashes", Execute: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"dash"`), nil
		}},
		{Name: "my_tool", Description: "Shadowed", Execute: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"underscore"`), nil
		}},
		{Name: "mcp__docs__search", Execute: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return json.RawMessage(`"mcp"`), nil
		}},
	}})
	r := s.Execute(context.Background(), `
		try { ALL_TOOLS.push({}); } catch {}
		return {
			all: ALL_TOOLS,
			calls: [await tools.my_tool(), await tools["my-tool"](), await tools.mcp__docs__search()],
		};
	`, codemode.ExecuteOptions{})
	got := decodeValue(t, r)
	want := map[string]any{
		"all": []any{
			map[string]any{"name": "my_tool", "description": "Dashes"},
			map[string]any{"name": "mcp__docs__search", "description": ""},
		},
		"calls": []any{"dash", "dash", "mcp"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalization wrong: %#v", got)
	}
}

func TestPiV1CodemodeToolErrors(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Tools: []codemode.Tool{
		{Name: "fail", Execute: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) {
			return nil, errors.New("tool exploded")
		}},
	}})
	r := s.Execute(context.Background(), `
		try { await tools.fail(); return "no error"; }
		catch (error) { return { isError: error instanceof Error, message: error.message }; }
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, map[string]any{"isError": true, "message": "tool exploded"}) {
		t.Fatalf("tool error bridge wrong: %#v", got)
	}
	if len(r.Calls) != 1 || r.Calls[0].Status != "error" {
		t.Fatalf("tool error status wrong: %+v", r.Calls)
	}
}

func TestPiV1CodemodeUnknownToolHints(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Tools: []codemode.Tool{
		echoTool(),
		{Name: "web-search", Execute: func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`""`), nil }},
	}})
	attempt := func(expr string) string {
		r := s.Execute(context.Background(), "return "+expr+";", codemode.ExecuteOptions{})
		if r.OK {
			var v any
			json.Unmarshal(r.Value, &v)
			b, _ := json.Marshal(v)
			return string(b)
		}
		if r.Error == nil {
			return "<no error>"
		}
		return r.Error.Message
	}
	if got := attempt("tools.Echo"); got != `tools.Echo does not exist. Did you mean tools.echo? ALL_TOOLS lists every tool; searchTools(query) finds tools by topic. Check for a member with "Echo" in tools.` {
		t.Fatalf("echo hint wrong: %q", got)
	}
	if got := attempt("tools.websearch"); !strings.Contains(got, "Did you mean tools.web_search?") {
		t.Fatalf("close-match hint wrong: %q", got)
	}
	if got := attempt("tools.nothing"); !strings.Contains(got, "Available: echo, web_search.") {
		t.Fatalf("available hint wrong: %q", got)
	}
	r := s.Execute(context.Background(), `return ['echo' in tools, 'nothing' in tools, String(tools.toString), JSON.stringify(tools)]`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, []any{true, false, "undefined", "{}"}) {
		t.Fatalf("tool proxy introspection wrong: %#v", got)
	}
}

func TestPiV1CodemodeUnawaitedCancels(t *testing.T) {
	aborted := make(chan struct{}, 1)
	var once sync.Once
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Tools: []codemode.Tool{
		{Name: "slow", Execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			<-ctx.Done()
			once.Do(func() { aborted <- struct{}{} })
			return nil, ctx.Err()
		}},
	}})
	r := s.Execute(context.Background(), `tools.slow(); return 'early'`, codemode.ExecuteOptions{})
	if !r.OK || string(r.Value) != `"early"` {
		t.Fatalf("early return wrong: %+v", r)
	}
	if len(r.Calls) != 1 || r.Calls[0].Status != "cancelled" {
		t.Fatalf("unawaited call should be cancelled: %+v", r.Calls)
	}
	select {
	case <-aborted:
	case <-time.After(2 * time.Second):
		t.Fatal("unawaited tool was not aborted")
	}
}

func TestPiV1CodemodeRegisterUnregister(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second})
	if err := s.RegisterTool(echoTool()); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterTool(echoTool()); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate register: %v", err)
	}
	if got := s.Tools(); len(got) != 1 || got[0].Name != "echo" {
		t.Fatalf("tools wrong: %+v", got)
	}
	r := s.Execute(context.Background(), `return await tools.echo("a")`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); got != "a" {
		t.Fatalf("registered tool call wrong: %#v", got)
	}
	if !s.UnregisterTool("echo") {
		t.Fatal("unregister returned false")
	}
	r = s.Execute(context.Background(), `return 'echo' in tools`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); got != false {
		t.Fatalf("unregister did not take effect: %#v", got)
	}
}

// ---- store ----

func TestPiV1CodemodeStore(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second})
	input := map[string]json.RawMessage{"a": json.RawMessage(`{"value":1}`), "delete": json.RawMessage(`7`)}
	r := s.Execute(context.Background(), `
		const a = load("a"); a.value = 99;
		store("new", a);
		store("delete", undefined);
		return load("a").value;
	`, codemode.ExecuteOptions{Store: input})
	if got := decodeValue(t, r); got != float64(1) {
		t.Fatalf("store copy semantics wrong: %#v", got)
	}
	if string(input["a"]) != `{"value":1}` {
		t.Fatalf("store input mutated: %s", string(input["a"]))
	}
	if r.StoreWrites == nil || string(r.StoreWrites.Set["new"]) != `{"value":99}` || !reflect.DeepEqual(r.StoreWrites.Delete, []string{"delete"}) {
		t.Fatalf("store write delta wrong: %+v", r.StoreWrites)
	}

	r = s.Execute(context.Background(), `store("bad", 1); text("before failure"); throw new Error("fixture");`, codemode.ExecuteOptions{Store: input})
	if r.OK || r.Error == nil || r.Error.Kind != "script" || r.StoreWrites != nil {
		t.Fatalf("failure committed writes: %+v", r)
	}
	if len(r.Output) != 1 || r.Output[0].Text != "before failure" {
		t.Fatalf("output before failure lost: %+v", r.Output)
	}
}

func TestPiV1CodemodeStoreLimits(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 15 * time.Second})
	r := s.Execute(context.Background(), `
		const attempt = (fn) => { try { fn(); return "ok"; } catch (error) { return error.name; } };
		return [
			attempt(() => store(1, "x")),
			attempt(() => load({})),
			attempt(() => store("fn", () => 1)),
			attempt(() => store("big", "x".repeat(5 * 1024 * 1024))),
			attempt(() => { for (let i = 0; i < 8; i++) store("k" + i, "x".repeat(2 * 1024 * 1024)); }),
		];
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, []any{"TypeError", "TypeError", "TypeError", "RangeError", "RangeError"}) {
		t.Fatalf("store limit errors wrong: %#v", got)
	}
	r = s.Execute(context.Background(), `store("img", "x".repeat(5 * 1024 * 1024));`, codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || !strings.Contains(r.Error.Message, "Show images with image()") {
		t.Fatalf("oversized store message wrong: %+v", r.Error)
	}
	r = s.Execute(context.Background(), `
		store("exact", "中".repeat(4194304 - 2));
		let over = "accepted";
		try { store("over", "x".repeat(4194304 - 1)); } catch (e) { over = e.name; }
		return [load("exact").length, over];
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, []any{float64(4194304 - 2), "RangeError"}) {
		t.Fatalf("new store boundary/UTF-16 unit contract: %#v", got)
	}
}

// ---- globals ----

func TestPiV1CodemodeGlobals(t *testing.T) {
	// Go callbacks run concurrently. A callback's complete blocking body runs in
	// its own goroutine, so independent callbacks may start and finish in either
	// order; only a first explicitly awaited call is ordered before later calls.
	// Collect observations under a mutex and assert membership, not the relative
	// order of overlapping calls.
	var mu sync.Mutex
	seen := []string{}
	record := func(_ context.Context, args json.RawMessage) (json.RawMessage, error) {
		mu.Lock()
		seen = append(seen, string(args))
		mu.Unlock()
		return nil, nil
	}
	observed := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
	s := newTestSandbox(t, codemode.SandboxOptions{Tools: []codemode.Tool{echoTool()}, Globals: []codemode.Tool{
		{Name: "attach", Execute: record},
		{Name: "models.list", Spread: true, Execute: record},
	}, Timeout: 5 * time.Second})
	r := s.Execute(context.Background(), `
		await attach({ ref: 1 });
		const saved = attach("not awaited");
		const spread = models.list("classifier", undefined, 3);
		await saved;
		await spread;
		return [typeof attach, Object.keys(models), await tools.echo(2)];
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, []any{"function", []any{"list"}, float64(2)}) {
		t.Fatalf("globals wrong: %#v", got)
	}
	got := observed()
	if len(got) != 3 || got[0] != `{"ref":1}` {
		t.Fatalf("explicit await must complete before overlapping calls: %q", got)
	}
	counts := map[string]int{}
	for _, payload := range got[1:] {
		counts[payload]++
	}
	if counts[`"not awaited"`] != 1 || counts[`["classifier",null,3]`] != 1 || len(counts) != 2 {
		t.Fatalf("overlapping global payloads wrong: %q", got)
	}
	if len(r.Calls) != 1 || r.Calls[0].Name != "echo" {
		t.Fatalf("globals must not be recorded as calls: %+v", r.Calls)
	}
}

func TestPiV1CodemodeGlobalsNamespaceHints(t *testing.T) {
	noop := func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil }
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Globals: []codemode.Tool{
		{Name: "models.classify", Execute: noop},
		{Name: "models.generateImages", Execute: noop},
	}})
	r := s.Execute(context.Background(), `await models.generateImage();`, codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Message != `models.generateImage does not exist. Did you mean models.generateImages? Check for a member with "generateImage" in models.` {
		t.Fatalf("namespace hint wrong: %+v", r.Error)
	}
}

func TestPiV1CodemodeInvalidOptions(t *testing.T) {
	noop := func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil }
	for _, name := range []string{"a.b.c", "a.", ".a", "tools.x", "store.x", "a.not-valid", "not-valid", "tools", "console", "load"} {
		if _, err := codemode.NewSandbox(codemode.SandboxOptions{Globals: []codemode.Tool{{Name: name, Execute: noop}}}); err == nil || !strings.Contains(err.Error(), "Invalid global") {
			t.Fatalf("global %q not rejected: %v", name, err)
		}
	}
	if _, err := codemode.NewSandbox(codemode.SandboxOptions{Globals: []codemode.Tool{
		{Name: "models", Execute: noop},
		{Name: "models.list", Execute: noop},
	}}); err == nil || !strings.Contains(err.Error(), "conflicts with the namespace") {
		t.Fatalf("namespace conflict not rejected: %v", err)
	}
	if _, err := codemode.NewSandbox(codemode.SandboxOptions{Tools: []codemode.Tool{echoTool(), echoTool()}}); err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("duplicate tool not rejected: %v", err)
	}
}

// ---- limits and lifetime ----

func TestPiV1CodemodeTimeoutAndAbort(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 40 * time.Millisecond, MemoryLimitBytes: 8 << 20})
	start := time.Now()
	r := s.Execute(context.Background(), `while(true) {}`, codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Kind != "timeout" {
		t.Fatalf("CPU loop escaped deadline: %+v", r)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("timeout took too long")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = s.Execute(ctx, `return 1;`, codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Kind != "aborted" {
		t.Fatalf("cancelled caller ignored: %+v", r)
	}

	// The sandbox stays usable after a timeout.
	r = s.Execute(context.Background(), `return 42;`, codemode.ExecuteOptions{Timeout: time.Second})
	if got := decodeValue(t, r); got != float64(42) {
		t.Fatalf("deadline damaged sandbox reuse: %#v", got)
	}
}

func TestPiV1CodemodeStalledPromise(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Tools: []codemode.Tool{echoTool()}})
	r := s.Execute(context.Background(), `await new Promise(()=>{});`, codemode.ExecuteOptions{Timeout: time.Second})
	if r.OK || r.Error == nil || !strings.Contains(strings.ToLower(r.Error.Message), "can never settle") {
		t.Fatalf("unresolvable promise not diagnosed: %+v", r)
	}
	// Returning while a call is still pending is not a stall.
	r = s.Execute(context.Background(), `tools.echo(2); return 'early'`, codemode.ExecuteOptions{Timeout: time.Second})
	if !r.OK || string(r.Value) != `"early"` {
		t.Fatalf("pending call at return should not stall: %+v", r)
	}
}

func TestPiV1CodemodeCloseAborts(t *testing.T) {
	s, err := codemode.NewSandbox(codemode.SandboxOptions{Timeout: 10 * time.Second, Tools: []codemode.Tool{
		{Name: "hang", Execute: func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	done := make(chan codemode.Result, 1)
	go func() {
		done <- s.Execute(context.Background(), `await tools.hang(); return "never"`, codemode.ExecuteOptions{})
	}()
	time.Sleep(50 * time.Millisecond)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.OK || r.Error == nil || r.Error.Kind != "aborted" || r.Error.Message != "Sandbox closed" {
			t.Fatalf("close did not abort: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close hung")
	}
	// Execute after close reports aborted rather than hanging.
	r := s.Execute(context.Background(), `return 1`, codemode.ExecuteOptions{})
	if r.OK || r.Error == nil || r.Error.Kind != "aborted" {
		t.Fatalf("execute after close: %+v", r)
	}
}

func TestPiV1CodemodeParallelIsolation(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 10 * time.Second})
	type out struct {
		ok  bool
		val string
		err string
	}
	results := make([]out, 4)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := s.Execute(context.Background(), `globalThis.shared = '`+string(rune('a'+i))+`'; await null; return globalThis.shared`, codemode.ExecuteOptions{})
			results[i] = out{r.OK, string(r.Value), ""}
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for i, r := range results {
		if !r.ok {
			t.Fatalf("parallel run %d failed: %+v", i, r)
		}
		seen[r.val] = true
	}
	for _, want := range []string{`"a"`, `"b"`, `"c"`, `"d"`} {
		if !seen[want] {
			t.Fatalf("parallel isolation lost %s: %v", want, seen)
		}
	}
}

func TestPiV1CodemodeVMLifetime(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second})
	r := s.Execute(context.Background(), `globalThis.testMarker = "secret"; return [typeof process, typeof require, typeof fetch, typeof setTimeout, typeof WebAssembly];`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, []any{"undefined", "undefined", "undefined", "undefined", "undefined"}) {
		t.Fatalf("privileged globals exposed: %#v", got)
	}
	r = s.Execute(context.Background(), `return typeof testMarker;`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); got != "undefined" {
		t.Fatalf("VM state leaked across executions: %#v", got)
	}
}

func TestPiV1CodemodeDeepRecursion(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, MaxStackBytes: 512 * 1024})
	r := s.Execute(context.Background(), `
		let depth = 0;
		function dive() { depth++; dive(); }
		try { dive(); } catch (error) { return [error.name, depth > 1000]; }
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, []any{"RangeError", true}) {
		t.Fatalf("deep recursion did not become a catchable RangeError: %#v", got)
	}
}

func TestPiV1CodemodeMemoryPressure(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, MemoryLimitBytes: 8 << 20})
	r := s.Execute(context.Background(), `
		const chunks = [];
		try {
			for (let i = 0; i < 1000; i++) chunks.push("x".repeat(1024 * 1024));
			return "no oom";
		} catch (error) { return error.name; }
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); got != "InternalError" {
		t.Fatalf("memory limit not enforced: %#v", got)
	}
	r = s.Execute(context.Background(), `return 1 + 1`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); got != float64(2) {
		t.Fatalf("sandbox unusable after OOM: %#v", got)
	}
}

func TestPiV1CodemodeEvalAndImport(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Tools: []codemode.Tool{echoTool()}})
	r := s.Execute(context.Background(), `
		return [
			eval("typeof process"),
			new Function("return typeof process")(),
			tools.echo.constructor("return typeof require")(),
			(async () => {}).constructor("return typeof setTimeout")() instanceof Promise,
		];
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, []any{"undefined", "undefined", "undefined", true}) {
		t.Fatalf("eval/Function escaped the VM: %#v", got)
	}
	r = s.Execute(context.Background(), `
		try { await import("node:fs"); return "imported"; }
		catch (error) { return error.constructor.name; }
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); got == "imported" {
		t.Fatal("dynamic import was allowed")
	}
}

func TestPiV1CodemodeFrozenHelpers(t *testing.T) {
	s := newTestSandbox(t, codemode.SandboxOptions{Timeout: 5 * time.Second, Tools: []codemode.Tool{echoTool()}})
	r := s.Execute(context.Background(), `
		try { tools.echo = () => 'nope'; } catch {}
		try { tools.extra = () => 'nope'; } catch {}
		try { globalThis.tools = null; } catch {}
		return ["extra" in tools, await tools.echo('still')];
	`, codemode.ExecuteOptions{})
	if got := decodeValue(t, r); !reflect.DeepEqual(got, []any{false, "still"}) {
		t.Fatalf("frozen helpers violated: %#v", got)
	}
}

// ---- source parsing ----

func TestPiV1CodemodeParseSource(t *testing.T) {
	parsed, err := codemode.ParseCodemodeSource("text('hi')")
	if err != nil || parsed.Code != "text('hi')" || parsed.Options.TimeoutMs != nil || parsed.Options.MaxOutputTokens != nil {
		t.Fatalf("plain source wrong: %+v %v", parsed, err)
	}
	parsed, err = codemode.ParseCodemodeSource("// just a comment\nreturn 1")
	if err != nil || parsed.Code != "// just a comment\nreturn 1" || parsed.Options.TimeoutMs != nil {
		t.Fatalf("comment source wrong: %+v %v", parsed, err)
	}
	parsed, err = codemode.ParseCodemodeSource("// @options: {\"timeout_ms\": 10}\nconst a = 1;\ntext(a)")
	if err != nil || parsed.Code != "\nconst a = 1;\ntext(a)" || parsed.Options.TimeoutMs == nil || *parsed.Options.TimeoutMs != 10 {
		t.Fatalf("options source wrong: %+v %v", parsed, err)
	}
	parsed, err = codemode.ParseCodemodeSource("  // @options:{\"max_output_tokens\":0,\"timeout_ms\":1500}\r\ntext(1)")
	if err != nil || parsed.Options.MaxOutputTokens == nil || *parsed.Options.MaxOutputTokens != 0 || parsed.Options.TimeoutMs == nil || *parsed.Options.TimeoutMs != 1500 {
		t.Fatalf("options fields wrong: %+v %v", parsed, err)
	}
	parsed, err = codemode.ParseCodemodeSource("// @options: {}\ntext(1)")
	if err != nil || parsed.Code != "\ntext(1)" {
		t.Fatalf("empty options wrong: %+v %v", parsed, err)
	}
	input := "text(1)\n// @options: {\"timeout_ms\": 1}"
	parsed, err = codemode.ParseCodemodeSource(input)
	if err != nil || parsed.Code != input {
		t.Fatalf("only first line is options: %+v %v", parsed, err)
	}
	parsed, err = codemode.ParseCodemodeSource("// @optionsx {}\ntext(1)")
	if err != nil || parsed.Options.TimeoutMs != nil {
		t.Fatalf("options-like line must not parse: %+v %v", parsed, err)
	}

	bad := []string{
		"",
		"  \n",
		"// @options:\ntext(1)",
		"// @options: {timeout_ms: 1}\ntext(1)",
		"// @options: [1]\ntext(1)",
		"// @options: {\"yield\": 1}\ntext(1)",
		"// @options: {\"max_output_tokens\": 1.5}\ntext(1)",
		"// @options: {\"timeout_ms\": 0}\ntext(1)",
		"// @options: {\"timeout_ms\": 1}",
		"// @options: {\"timeout_ms\": 1}\n  \n",
	}
	for _, source := range bad {
		if _, err := codemode.ParseCodemodeSource(source); err == nil {
			t.Fatalf("expected error for %q", source)
		} else {
			var se *codemode.CodemodeSourceError
			if !errors.As(err, &se) {
				t.Fatalf("expected CodemodeSourceError for %q, got %T", source, err)
			}
		}
	}
}

// ---- declarations ----

func TestPiV1CodemodeToIdentifier(t *testing.T) {
	cases := map[string]string{
		"mcp.search-docs":   "mcp_search_docs",
		"mcp__docs__search": "mcp__docs__search",
		"my-tool":           "my_tool",
		"":                  "_",
		"1abc":              "_abc",
	}
	for in, want := range cases {
		if got := codemode.ToIdentifier(in); got != want {
			t.Fatalf("ToIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPiV1CodemodeRenderDeclarations(t *testing.T) {
	noop := func(_ context.Context, _ json.RawMessage) (json.RawMessage, error) { return nil, nil }
	got, err := codemode.RenderDeclarationsWithGlobals(
		[]codemode.Tool{
			{
				Name:         "read",
				Description:  "Read a file.\nSecond line.",
				InputSchema:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
				OutputSchema: json.RawMessage(`{"type":"string"}`),
				Execute:      noop,
			},
			{Name: "remote-api", Execute: noop},
		},
		[]codemode.Tool{{Name: "attach", Description: "Attach it.", InputSchema: json.RawMessage(`{"type":"string"}`), Execute: noop}},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"declare const tools: {",
		"  /**",
		"   * Read a file.",
		"   * Second line.",
		"   */",
		"  read(args: { path: string; }): Promise<string>;",
		"  remote_api(args: unknown): Promise<unknown>;",
		"};",
		"",
		"/** Attach it. */",
		"declare function attach(args: string): Promise<unknown>;",
	}, "\n")
	if got != want {
		t.Fatalf("declarations wrong:\n got=%q\nwant=%q", got, want)
	}

	got, err = codemode.RenderDeclarationsWithGlobals(nil, []codemode.Tool{
		{Name: "models.list", Description: "List models.", Signature: "(type: string): Promise<string[]>", Execute: noop},
		{Name: "models.get", InputSchema: json.RawMessage(`{"type":"string"}`), Execute: noop},
		{Name: "plain", Signature: "(): void", Execute: noop},
	})
	if err != nil {
		t.Fatal(err)
	}
	want = strings.Join([]string{
		"declare function plain(): void;",
		"",
		"declare const models: {",
		"  /** List models. */",
		"  list(type: string): Promise<string[]>;",
		"  get(args: string): Promise<unknown>;",
		"};",
	}, "\n")
	if got != want {
		t.Fatalf("namespaced declarations wrong:\n got=%q\nwant=%q", got, want)
	}

	got, err = codemode.RenderDeclarations([]codemode.Tool{{Name: "x", Description: "a */ b", Execute: noop}})
	if err != nil || !strings.Contains(got, `/** a *\/ b */`) {
		t.Fatalf("comment escaping wrong: %q %v", got, err)
	}
}

func TestPiV1CodemodeSchemaToType(t *testing.T) {
	cases := []struct {
		schema string
		want   string
	}{
		{`{"type":"string"}`, "string"},
		{`{"type":"integer"}`, "number"},
		{`{"type":["string","null"]}`, "string | null"},
		{`{"const":"a"}`, `"a"`},
		{`{"enum":["a",1,null]}`, `"a" | 1 | null`},
		{`{"anyOf":[{"type":"string"},{"type":"number"}]}`, "string | number"},
		{`{"anyOf":[{"type":"string"},{}]}`, "unknown"},
		{`{"allOf":[{"anyOf":[{"type":"string"},{"type":"number"}]},{"const":1}]}`, "(string | number) & 1"},
		{`{"$ref":"#/defs/x"}`, "unknown"},
		{`true`, "unknown"},
		{`false`, "never"},
		{`{"type":"object","properties":{"city":{"type":"string"},"max-lines":{"type":"number"}},"required":["city"],"additionalProperties":false}`, `{ city: string; "max-lines"?: number; }`},
		{`{"type":"object","additionalProperties":{"type":"number"}}`, "{ [key: string]: number; }"},
		{`{"type":"object"}`, "{ [key: string]: unknown; }"},
		{`{"type":"object","properties":{},"additionalProperties":false}`, "{}"},
		{`{"type":"array","items":{"type":"string"}}`, "Array<string>"},
		{`{"type":"array","prefixItems":[{"type":"string"},{"type":"number"}]}`, "[string, number]"},
		{`{"type":"array"}`, "unknown[]"},
		{`{"type":"object","properties":{"weather":{"type":"array","description":"look up weather for a given list of locations","items":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}},"required":["weather"]}`, "{\n  // look up weather for a given list of locations\n  weather: Array<{ location: string; }>;\n}"},
		{`{"type":"object","properties":{"outer":{"type":"object","description":"Outer","properties":{"inner":{"type":"string","description":"Inner"}}}}}`, "{\n  // Outer\n  outer?: {\n    // Inner\n    inner?: string;\n  };\n}"},
		{`{"type":"object","properties":{"item":{"$ref":"#/$defs/Item"},"legacy":{"$ref":"#/definitions/Legacy"},"remote":{"$ref":"https://example.com/schema.json"}},"required":["item"],"$defs":{"Item":{"type":"object","properties":{"id":{"type":"string"},"parent":{"$ref":"#/$defs/Item"}},"required":["id"]}},"definitions":{"Legacy":{"enum":["a","b"]}}}`, `{ item: { id: string; parent?: unknown; }; legacy?: "a" | "b"; remote?: unknown; }`},
	}
	for _, tc := range cases {
		got := codemode.SchemaToType(json.RawMessage(tc.schema), 0)
		if got != tc.want {
			t.Fatalf("SchemaToType(%s):\n got=%q\nwant=%q", tc.schema, got, tc.want)
		}
	}

	props := map[string]any{}
	for i := 0; i < 50; i++ {
		props["field"+itoa(i)] = map[string]any{"type": "string"}
	}
	schema, _ := json.Marshal(map[string]any{"type": "object", "properties": props})
	if got := codemode.SchemaToType(schema, 100); got != "unknown" {
		t.Fatalf("budgeted type should be unknown: %q", got)
	}
	if got := codemode.SchemaToType(schema, 0); !strings.Contains(got, "field49?: string;") {
		t.Fatalf("unbudgeted type truncated: %q", got)
	}
}

func TestPiV1CodemodeToolSignatures(t *testing.T) {
	got := codemode.RenderToolSignature(codemode.Tool{
		Name:         "hidden-dynamic-tool",
		InputSchema:  json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"]}`),
	})
	if got != "hidden_dynamic_tool(args: { city: string; }): Promise<{ ok: boolean; }>;" {
		t.Fatalf("signature wrong: %q", got)
	}
	if got := codemode.RenderToolSignature(codemode.Tool{Name: "free"}); got != "free(args: unknown): Promise<unknown>;" {
		t.Fatalf("free signature wrong: %q", got)
	}

	mcpSchema := json.RawMessage(`{"type":"object","properties":{"content":{"type":"array","items":{"type":"object"}},"structuredContent":{"type":"object","properties":{"results":{"type":"array","items":{"$ref":"#/definitions/Result~1item~0v1"}}},"required":["results"],"additionalProperties":false,"definitions":{"Result/item~v1":{"type":"object","properties":{"id":{"type":"string"},"score":{"type":"number"}},"required":["id","score"],"additionalProperties":false}}},"isError":{"type":"boolean"},"_meta":{"type":"object"}},"required":["content"]}`)
	got = codemode.RenderToolSignature(codemode.Tool{Name: "mcp__sample__search", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), OutputSchema: mcpSchema})
	want := "mcp__sample__search(args: {}): Promise<CallToolResult<{ results: Array<{ id: string; score: number; }>; }>>;"
	if got != want {
		t.Fatalf("MCP signature wrong:\n got=%q\nwant=%q", got, want)
	}

	plainMCP := json.RawMessage(`{"type":"object","properties":{"content":{"type":"array","items":{"type":"object"}},"isError":{"type":"boolean"},"_meta":{"type":"object"}},"required":["content"]}`)
	if got := codemode.RenderToolSignature(codemode.Tool{Name: "plain", InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`), OutputSchema: plainMCP}); got != "plain(args: {}): Promise<CallToolResult>;" {
		t.Fatalf("plain MCP signature wrong: %q", got)
	}
	if _, ok := codemode.MCPStructuredContentSchema(json.RawMessage(`{"type":"object","properties":{"content":{"type":"array"}}}`)); ok {
		t.Fatal("non-MCP schema detected as CallToolResult")
	}

	sample := codemode.RenderToolSample(codemode.Tool{Name: "foo", Description: "bar", InputSchema: json.RawMessage(`{"type":"string"}`)})
	if sample != "bar\n\ncodemode tool declaration:\n```ts\ndeclare const tools: { foo(args: string): Promise<unknown>; };\n```" {
		t.Fatalf("sample wrong: %q", sample)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
