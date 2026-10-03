package durable_tools_test

import (
	"context"
	"encoding/json"
	"github.com/minifish-org/pith/packages/durable/env"
	"github.com/minifish-org/pith/packages/durable/harness"
	"github.com/minifish-org/pith/packages/durable/tools"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// Embedding the real interface retains its full surface. The builtin tools are
// allowed to use only the environment/reporting capabilities overridden here.
type toolAPI struct {
	harness.ToolAPI
	environment env.ExecutionEnv
	output      []byte
	diagnostics []harness.ToolDiagnostic
	details     json.RawMessage
}

func (a *toolAPI) Env() env.ExecutionEnv { return a.environment }
func (a *toolAPI) Output(b []byte) error { a.output = append(a.output, b...); return nil }
func (a *toolAPI) Diagnostic(d harness.ToolDiagnostic) error {
	a.diagnostics = append(a.diagnostics, d)
	return nil
}
func (a *toolAPI) Details(_ context.Context, b json.RawMessage) error {
	a.details = append(json.RawMessage(nil), b...)
	return nil
}
func fixture(t *testing.T) *toolAPI {
	t.Helper()
	e, err := env.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cleanup(context.Background()) })
	return &toolAPI{environment: e}
}
func args(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func text(r harness.ToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Text != nil {
			b.WriteString(c.Text.Text)
		}
	}
	return b.String()
}

func TestPortsmithJudgeDurableCodingTools(t *testing.T) {
	ctx := context.Background()
	a := fixture(t)
	ext := tools.CodingTools()
	if ext.Name != "coding-tools" || len(ext.Tools) != 4 {
		t.Fatalf("explicit extension: %#v", ext)
	}
	seen := map[string]bool{}
	for _, r := range ext.Tools {
		seen[r.Declaration.Name] = true
		if r.Replay == "safe" {
			t.Fatalf("invented replay guarantee for %s", r.Declaration.Name)
		}
	}
	for _, n := range []string{"read", "write", "edit", "bash"} {
		if !seen[n] {
			t.Fatal("missing tool", n)
		}
	}
	r, e := tools.CreateWriteTool().Execute(ctx, args(t, map[string]any{"path": "nested/doc.txt", "content": "alpha\nbeta\ngamma\n"}), a)
	if e != nil || r.IsError {
		t.Fatalf("write: %#v %v", r, e)
	}
	r, e = tools.CreateReadTool().Execute(ctx, args(t, map[string]any{"path": "nested/doc.txt", "offset": 2, "limit": 1}), a)
	if e != nil || r.IsError || text(r) != "beta" {
		t.Fatalf("read selected line: %q %#v %v", text(r), r, e)
	}
	p := filepath.Join(a.environment.CWD(), "crlf.txt")
	original := "\ufeffone\r\ntwo\r\nthree\r\n"
	if e = os.WriteFile(p, []byte(original), 0600); e != nil {
		t.Fatal(e)
	}
	edit := tools.CreateEditTool()
	input := args(t, map[string]any{"path": "crlf.txt", "edits": map[string]any{"oldText": "two", "newText": "second"}})
	if edit.PrepareArguments == nil {
		t.Fatal("argument repair omitted")
	}
	prepared, e := edit.PrepareArguments(input)
	if e != nil {
		t.Fatal(e)
	}
	r, e = edit.Execute(ctx, prepared, a)
	if e != nil || r.IsError {
		t.Fatalf("edit: %#v %v", r, e)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "\ufeffone\r\nsecond\r\nthree\r\n" {
		t.Fatalf("BOM/CRLF lost: %q", b)
	}
	var details map[string]any
	if e = json.Unmarshal(r.Details, &details); e != nil {
		t.Fatal(e)
	}
	if details["diff"] == nil || details["patch"] == nil || details["firstChangedLine"] != float64(2) {
		t.Fatalf("edit details: %#v", details)
	}
	before := string(b)
	bad := args(t, map[string]any{"path": "crlf.txt", "edits": []any{map[string]string{"oldText": "second", "newText": "X"}, map[string]string{"oldText": "cond", "newText": "Y"}}})
	r, e = edit.Execute(ctx, bad, a)
	if e == nil && !r.IsError {
		t.Fatal("overlap accepted")
	}
	b, _ = os.ReadFile(p)
	if string(b) != before {
		t.Fatal("partial overlapping edit committed")
	}
	b = []byte(strings.Repeat("界", 30000))
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	r, e = tools.CreateReadTool().Execute(ctx, args(t, map[string]string{"path": "crlf.txt"}), a)
	if e != nil || r.IsError || !utf8.ValidString(text(r)) || len(text(r)) > 50*1024 {
		t.Fatalf("UTF8 first-line bound: bytes=%d %v", len(text(r)), e)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	r, e = tools.CreateWriteTool().Execute(cancelled, args(t, map[string]string{"path": "crlf.txt", "content": "broken"}), a)
	if e == nil && !r.IsError {
		t.Fatal("cancelled tool succeeded")
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(b) {
		t.Fatal("precancelled tool mutated file")
	}
}

func TestPortsmithJudgeDurableBashTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command; candidate tests cover Windows shell")
	}
	a := fixture(t)
	ctx := context.Background()
	prepared := false
	bash := tools.CreateBashTool(tools.BashToolOptions{CommandPrefix: "export DURABLE_TEST_PREFIX=present", Prepare: func(_ context.Context, e *tools.BashExecution, _ harness.ToolAPI) error {
		prepared = true
		e.Env["DURABLE_TEST_LOCAL"] = "native"
		return nil
	}})
	r, e := bash.Execute(ctx, args(t, map[string]string{"command": "printf '%s:%s' \"$DURABLE_TEST_PREFIX\" \"$DURABLE_TEST_LOCAL\""}), a)
	if e != nil || r.IsError || !prepared || string(a.output) != "present:native" {
		t.Fatalf("prefix/prepare/output: %q %v", a.output, e)
	}
	r, e = bash.Execute(ctx, args(t, map[string]string{"command": "printf failure; exit 8"}), a)
	if e == nil && !r.IsError {
		t.Fatal("nonzero tool exit reported success")
	}
	for _, timeout := range []float64{0, -1, 2147484} {
		r, e = bash.Execute(ctx, args(t, map[string]any{"command": "exit 0", "timeout": timeout}), a)
		if e == nil && !r.IsError {
			t.Fatalf("invalid timeout accepted: %v", timeout)
		}
	}
}
