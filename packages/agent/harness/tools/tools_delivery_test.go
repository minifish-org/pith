// Self-tests for the tools-delivery batch: built-in tool registration.
//
// This file translates the executable tool scenarios of the upstream
// packages/agent/test/harness/types.test.ts into Go runtime checks. The
// upstream file is a type-level fixture; the Go port keeps the runtime-visible
// facts it fixes (tool names, labels, declarations, prepare coercion and the
// concrete read/write/edit behavior) as executable assertions.
package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	harnessenv "github.com/minifish-org/pith/packages/agent/harness/env"
)

func TestBuiltinToolsRegistration(t *testing.T) {
	builtins := CreateBuiltinTools()
	wantOrder := []string{"read", "write", "edit", "bash"}
	if len(builtins) != len(wantOrder) {
		t.Fatalf("expected %d built-in tools, got %d", len(wantOrder), len(builtins))
	}
	seen := map[string]bool{}
	for index, builtin := range builtins {
		if builtin.Tool.Name != wantOrder[index] {
			t.Fatalf("tool %d: want %q, got %q", index, wantOrder[index], builtin.Tool.Name)
		}
		if builtin.Label == "" {
			t.Fatalf("tool %q has no label", builtin.Tool.Name)
		}
		if builtin.Tool.Description == "" {
			t.Fatalf("tool %q has no description", builtin.Tool.Name)
		}
		if len(builtin.Tool.Input.Schema) == 0 {
			t.Fatalf("tool %q has no JSON schema", builtin.Tool.Name)
		}
		var schema map[string]any
		if err := json.Unmarshal(builtin.Tool.Input.Schema, &schema); err != nil {
			t.Fatalf("tool %q schema is not JSON: %v", builtin.Tool.Name, err)
		}
		if schema["type"] != "object" {
			t.Fatalf("tool %q schema is not an object", builtin.Tool.Name)
		}
		if builtin.PrepareArguments == nil || builtin.Execute == nil {
			t.Fatalf("tool %q is missing prepare/execute", builtin.Tool.Name)
		}
		seen[builtin.Tool.Name] = true
	}
	for _, name := range wantOrder {
		if !seen[name] {
			t.Fatalf("missing built-in tool %q", name)
		}
	}
}

func TestBuiltinToolPrepareCoercion(t *testing.T) {
	builtins := map[string]BuiltinTool{}
	for _, builtin := range CreateBuiltinTools() {
		builtins[builtin.Tool.Name] = builtin
	}

	readParams, err := builtins["read"].PrepareArguments(json.RawMessage(`{"path":"file.txt","offset":2}`))
	if err != nil {
		t.Fatalf("read prepare: %v", err)
	}
	readInput, ok := readParams.(ReadToolInput)
	if !ok || readInput.Path != "file.txt" || readInput.Offset == nil || *readInput.Offset != 2 {
		t.Fatalf("unexpected read params %#v", readParams)
	}

	writeParams, err := builtins["write"].PrepareArguments(json.RawMessage(`{"path":"out.txt","content":"x"}`))
	if err != nil {
		t.Fatalf("write prepare: %v", err)
	}
	if writeInput, ok := writeParams.(WriteToolInput); !ok || writeInput.Path != "out.txt" || writeInput.Content != "x" {
		t.Fatalf("unexpected write params %#v", writeParams)
	}

	editParams, err := builtins["edit"].PrepareArguments(json.RawMessage(`{"path":"out.txt","oldText":"x","newText":"y"}`))
	if err != nil {
		t.Fatalf("edit prepare: %v", err)
	}
	editInput, ok := editParams.(EditToolInput)
	if !ok || editInput.Path != "out.txt" || len(editInput.Edits) != 1 || editInput.Edits[0].NewText != "y" {
		t.Fatalf("unexpected edit params %#v", editParams)
	}

	bashParams, err := builtins["bash"].PrepareArguments(json.RawMessage(`{"command":"true"}`))
	if err != nil {
		t.Fatalf("bash prepare: %v", err)
	}
	if bashInput, ok := bashParams.(BashToolInput); !ok || bashInput.Command != "true" {
		t.Fatalf("unexpected bash params %#v", bashParams)
	}
}

func TestBuiltinToolExecutionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	env := harnessenv.NewLocalExecutionEnv(harnessenv.LocalExecutionEnvOptions{Cwd: dir})
	ctx := context.Background()

	builtins := map[string]BuiltinTool{}
	for _, builtin := range CreateBuiltinTools() {
		builtins[builtin.Tool.Name] = builtin
	}

	writeParams, err := builtins["write"].PrepareArguments(json.RawMessage(`{"path":"nested/out.txt","content":"beta\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builtins["write"].Execute("call-1", writeParams, env, ctx); err != nil {
		t.Fatalf("write execute: %v", err)
	}

	editParams, err := builtins["edit"].PrepareArguments(json.RawMessage(`{"path":"nested/out.txt","oldText":"beta","newText":"gamma"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := builtins["edit"].Execute("call-2", editParams, env, ctx); err != nil {
		t.Fatalf("edit execute: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "nested", "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "gamma\n" {
		t.Fatalf("unexpected file content %q", content)
	}

	readParams, err := builtins["read"].PrepareArguments(json.RawMessage(`{"path":"nested/out.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, err := builtins["read"].Execute("call-3", readParams, env, ctx)
	if err != nil {
		t.Fatalf("read execute: %v", err)
	}
	if len(result.Content) == 0 || result.Content[0].Text == nil || result.Content[0].Text.Text == "" {
		t.Fatalf("read returned no text: %#v", result)
	}
}
