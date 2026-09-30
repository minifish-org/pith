package sdk_incremental_test

import (
	"context"
	"encoding/json"
	"errors"
	ai "github.com/minifish-org/pith/packages/ai/types"
	sdk "github.com/minifish-org/pith/packages/coding-agent"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPortsmithJudgeSDKTools(t *testing.T) {
	count := 0
	custom := sdk.ToolDefinition{Name: "verify_candidate", Description: "Check candidate", Parameters: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`), Execute: func(ctx context.Context, args json.RawMessage) (sdk.ToolResult, error) {
		count++
		return sdk.ToolResult{Content: []ai.ContentBlock{ai.TextBlock("verified")}}, nil
	}}
	r, err := sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{custom}, []string{"verify_candidate"}, nil, sdk.ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(r.Names(), []string{"verify_candidate"}) {
		t.Fatal("allowlist")
	}
	if _, err = r.Execute(context.Background(), sdk.ToolCall{Name: "verify_candidate", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("schema validation bypass")
	}
	if count != 0 {
		t.Fatal("invalid input executed")
	}
	result, err := r.Execute(context.Background(), sdk.ToolCall{ID: "1", Name: "verify_candidate", Arguments: json.RawMessage(`{"n":1}`)})
	if err != nil || count != 1 || len(result.Content) != 1 {
		t.Fatal("custom tool")
	}
	if err = r.SetActive([]string{"missing"}); err == nil {
		t.Fatal("unknown activation accepted")
	}
	if _, err = r.Execute(context.Background(), sdk.ToolCall{Name: "write", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("inactive tool executed")
	}
	denied, err := sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{custom}, nil, nil, sdk.ToolHooks{Before: func(context.Context, sdk.ToolCall) error { return errors.New("denied") }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = denied.Execute(context.Background(), sdk.ToolCall{Name: "verify_candidate", Arguments: json.RawMessage(`{"n":1}`)}); err == nil || count != 1 {
		t.Fatal("before hook bypass")
	}
	if _, err = sdk.NewToolRegistry(t.TempDir(), []sdk.ToolDefinition{custom, custom}, nil, nil, sdk.ToolHooks{}); err == nil {
		t.Fatal("duplicate tool accepted")
	}
	d := t.TempDir()
	r, err = sdk.NewToolRegistry(d, nil, []string{"read", "write", "edit", "bash", "grep", "find", "ls"}, nil, sdk.ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"read", "write", "edit", "bash", "grep", "find", "ls"} {
		if !slices.Contains(r.Names(), name) {
			t.Fatal("missing", name)
		}
	}
	_, err = r.Execute(context.Background(), sdk.ToolCall{Name: "write", Arguments: json.RawMessage(`{"path":"hello.txt","content":"hello sdk"}`)})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(d, "hello.txt"))
	if err != nil || string(data) != "hello sdk" {
		t.Fatal("builtin cwd")
	}
	result, err = r.Execute(context.Background(), sdk.ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"hello.txt"}`)})
	b, _ := json.Marshal(result)
	if err != nil || !strings.Contains(string(b), "hello sdk") {
		t.Fatal("builtin read")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.Execute(ctx, sdk.ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"echo should-not-run"}`)}); err == nil {
		t.Fatal("cancelled tool ran")
	}
}
