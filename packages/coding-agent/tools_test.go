package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	aitypes "github.com/minifish-org/pith/packages/ai/types"
)

func newTestTool(name string, execute func(context.Context, json.RawMessage) (ToolResult, error)) ToolDefinition {
	return ToolDefinition{
		Name:        name,
		Description: "test tool",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}},"required":["n"]}`),
		Execute:     execute,
	}
}

func TestToolRegistryValidationActivationAndCustomTool(t *testing.T) {
	count := 0
	custom := newTestTool("verify_candidate", func(context.Context, json.RawMessage) (ToolResult, error) {
		count++
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("verified")}}, nil
	})
	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{custom}, []string{"verify_candidate"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if names := registry.Names(); len(names) != 1 || names[0] != "verify_candidate" {
		t.Fatalf("allowlist names = %v", names)
	}
	if _, err := registry.Execute(context.Background(), ToolCall{Name: "verify_candidate", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("schema validation bypassed")
	}
	if count != 0 {
		t.Fatal("invalid input executed")
	}
	if _, err := registry.Execute(context.Background(), ToolCall{ID: "1", Name: "verify_candidate", Arguments: json.RawMessage(`{"n":1}`)}); err != nil || count != 1 {
		t.Fatalf("custom tool: err=%v count=%d", err, count)
	}
	if err := registry.SetActive([]string{"missing"}); err == nil {
		t.Fatal("unknown activation accepted")
	}
	if _, err := registry.Execute(context.Background(), ToolCall{Name: "write", Arguments: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("inactive tool executed")
	}
	// An explicit empty active set disables everything.
	if err := registry.SetActive([]string{}); err != nil {
		t.Fatal(err)
	}
	if len(registry.Names()) != 0 {
		t.Fatal("explicit empty activation did not disable tools")
	}
	// nil restores the default policy (custom tools are active by default).
	if err := registry.SetActive(nil); err != nil {
		t.Fatal(err)
	}
	if len(registry.Names()) == 0 {
		t.Fatal("nil activation did not restore defaults")
	}
}

func TestToolRegistryDuplicateAndHooks(t *testing.T) {
	custom := newTestTool("dup", func(context.Context, json.RawMessage) (ToolResult, error) {
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("ran")}}, nil
	})
	if _, err := NewToolRegistry(t.TempDir(), []ToolDefinition{custom, custom}, nil, nil, ToolHooks{}); err == nil {
		t.Fatal("duplicate tool accepted")
	}

	executions := 0
	beforeCalls := 0
	afterCalls := 0
	var registry *ToolRegistry
	hooks := ToolHooks{
		Before: func(context.Context, ToolCall) error {
			beforeCalls++
			// Hooks must run outside the registry lock: these calls would
			// deadlock if a lock were held.
			_ = registry.Names()
			return nil
		},
		After: func(_ context.Context, _ ToolCall, result ToolResult) (ToolResult, error) {
			afterCalls++
			result.Content = append(result.Content, aitypes.TextBlock(" transformed"))
			return result, nil
		},
	}
	tool := newTestTool("hooked", func(context.Context, json.RawMessage) (ToolResult, error) {
		executions++
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("base")}}, nil
	})
	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{tool}, []string{"hooked"}, nil, hooks)
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), ToolCall{Name: "hooked", Arguments: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if beforeCalls != 1 || afterCalls != 1 || executions != 1 {
		t.Fatalf("hook counts before=%d after=%d exec=%d", beforeCalls, afterCalls, executions)
	}
	if !strings.Contains(toolResultText(result), "base") || !strings.Contains(toolResultText(result), "transformed") {
		t.Fatalf("after transform missing: %q", toolResultText(result))
	}

	// Before can deny and the tool never runs.
	denied := 0
	denyHooks := ToolHooks{Before: func(context.Context, ToolCall) error { return errors.New("denied") }}
	denyTool := newTestTool("denied", func(context.Context, json.RawMessage) (ToolResult, error) {
		denied++
		return ToolResult{}, nil
	})
	denyRegistry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{denyTool}, nil, nil, denyHooks)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denyRegistry.Execute(context.Background(), ToolCall{Name: "denied", Arguments: json.RawMessage(`{"n":1}`)}); err == nil {
		t.Fatal("before hook did not deny")
	}
	if denied != 0 {
		t.Fatal("denied tool executed")
	}
}

func TestToolRegistryAllowDenyPrecedence(t *testing.T) {
	custom := newTestTool("extra", func(context.Context, json.RawMessage) (ToolResult, error) {
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("ok")}}, nil
	})
	registry, err := NewToolRegistry(t.TempDir(), []ToolDefinition{custom}, []string{"read", "extra"}, []string{"extra"}, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	names := registry.Names()
	if len(names) != 1 || names[0] != "read" {
		t.Fatalf("deny did not win: %v", names)
	}
	if _, err := registry.Execute(context.Background(), ToolCall{Name: "extra", Arguments: json.RawMessage(`{"n":1}`)}); err == nil {
		t.Fatal("denied tool executed")
	}

	// A custom tool registered after construction is active under the default
	// policy but not under an explicit allow list.
	defaultRegistry, err := NewToolRegistry(t.TempDir(), nil, nil, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	late := newTestTool("late", func(context.Context, json.RawMessage) (ToolResult, error) {
		return ToolResult{Content: []aitypes.ContentBlock{aitypes.TextBlock("late")}}, nil
	})
	if err := defaultRegistry.Register(late); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range defaultRegistry.Names() {
		if name == "late" {
			found = true
		}
	}
	if !found {
		t.Fatal("late custom tool was not activated by the default policy")
	}
}

func TestBuiltinToolsAllSeven(t *testing.T) {
	dir := t.TempDir()
	all := []string{"read", "write", "edit", "bash", "grep", "find", "ls"}
	registry, err := NewToolRegistry(dir, nil, all, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range all {
		found := false
		for _, active := range registry.Names() {
			if active == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing builtin %s", name)
		}
	}

	if _, err := registry.Execute(context.Background(), ToolCall{Name: "write", Arguments: json.RawMessage(`{"path":"nested/hello.txt","content":"hello sdk"}`)}); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "nested", "hello.txt")); err != nil || string(data) != "hello sdk" {
		t.Fatalf("write cwd: data=%q err=%v", data, err)
	}

	read, err := registry.Execute(context.Background(), ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"nested/hello.txt"}`)})
	if err != nil || !strings.Contains(encodeToolResult(t, read), "hello sdk") {
		t.Fatalf("read: err=%v result=%s", err, encodeToolResult(t, read))
	}

	edit, err := registry.Execute(context.Background(), ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"nested/hello.txt","edits":[{"oldText":"sdk","newText":"world"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encodeToolResult(t, edit), "Successfully replaced") {
		t.Fatalf("edit result: %s", encodeToolResult(t, edit))
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "nested", "hello.txt")); string(data) != "hello world" {
		t.Fatalf("edit content=%q", data)
	}

	bash, err := registry.Execute(context.Background(), ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"printf bash-ran"}`)})
	if err != nil || !strings.Contains(encodeToolResult(t, bash), "bash-ran") {
		t.Fatalf("bash: err=%v result=%s", err, encodeToolResult(t, bash))
	}

	grep, err := registry.Execute(context.Background(), ToolCall{Name: "grep", Arguments: json.RawMessage(`{"pattern":"world","path":"."}`)})
	if err != nil || !strings.Contains(encodeToolResult(t, grep), "nested/hello.txt") {
		t.Fatalf("grep: err=%v result=%s", err, encodeToolResult(t, grep))
	}

	find, err := registry.Execute(context.Background(), ToolCall{Name: "find", Arguments: json.RawMessage(`{"pattern":"**/*.txt","path":"."}`)})
	if err != nil || !strings.Contains(encodeToolResult(t, find), "nested/hello.txt") {
		t.Fatalf("find: err=%v result=%s", err, encodeToolResult(t, find))
	}

	ls, err := registry.Execute(context.Background(), ToolCall{Name: "ls", Arguments: json.RawMessage(`{"path":"."}`)})
	if err != nil || !strings.Contains(encodeToolResult(t, ls), "nested/") {
		t.Fatalf("ls: err=%v result=%s", err, encodeToolResult(t, ls))
	}
}

func TestEditExactMismatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewToolRegistry(dir, nil, []string{"edit"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"file.txt","edits":[{"oldText":"does-not-exist","newText":"x"}]}`)}); err == nil {
		t.Fatal("exact edit mismatch accepted")
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "file.txt")); string(data) != "original" {
		t.Fatalf("failed edit mutated file: %q", data)
	}
}

func TestSymlinkHandling(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("linked content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}
	registry, err := NewToolRegistry(dir, nil, []string{"read", "edit", "ls"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	read, err := registry.Execute(context.Background(), ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"link.txt"}`)})
	if err != nil || !strings.Contains(encodeToolResult(t, read), "linked content") {
		t.Fatalf("read symlink: err=%v result=%s", err, encodeToolResult(t, read))
	}
	// Editing through the symlink updates the target and leaves the link intact.
	if _, err := registry.Execute(context.Background(), ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"link.txt","edits":[{"oldText":"linked","newText":"edited"}]}`)}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "edited content" {
		t.Fatalf("symlink target not updated: %q", data)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink was replaced by a regular file")
	}
}

func TestBashCancellationDuringExecution(t *testing.T) {
	registry, err := NewToolRegistry(t.TempDir(), nil, []string{"bash"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = registry.Execute(ctx, ToolCall{Name: "bash", Arguments: json.RawMessage(`{"command":"sleep 10"}`)})
	if err == nil {
		t.Fatal("cancelled bash reported success")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cancellation did not interrupt the process group: %v", elapsed)
	}
}

func TestToolErrorsAndDetailPreservation(t *testing.T) {
	registry, err := NewToolRegistry(t.TempDir(), nil, []string{"read", "edit"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Execute(context.Background(), ToolCall{Name: "read", Arguments: json.RawMessage(`{"path":"missing.txt"}`)}); err == nil {
		t.Fatal("missing file read accepted")
	}
	if _, err := registry.Execute(context.Background(), ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"missing.txt","edits":[{"oldText":"a","newText":"b"}]}`)}); err == nil {
		t.Fatal("missing file edit accepted")
	}
	// Details are preserved for a successful edit.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err = NewToolRegistry(dir, nil, []string{"edit"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"f.txt","edits":[{"oldText":"abc","newText":"xyz"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Details) == 0 || !strings.Contains(string(result.Details), "firstChangedLine") {
		t.Fatalf("edit details lost: %s", result.Details)
	}
}

func TestFileMutationQueueSerializesSamePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.txt")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewToolRegistry(dir, nil, []string{"edit"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := registry.Execute(context.Background(), ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"shared.txt","edits":[{"oldText":"alpha","newText":"ALPHA"}]}`)})
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := registry.Execute(context.Background(), ToolCall{Name: "edit", Arguments: json.RawMessage(`{"path":"shared.txt","edits":[{"oldText":"beta","newText":"BETA"}]}`)})
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent edit failed: %v", err)
		}
	}
	// At least one edit must have survived without corruption. Because each
	// call re-reads under the serialized queue, both markers should survive
	// when the edits do not overlap.
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "beta") && !strings.Contains(string(data), "BETA") {
		t.Fatalf("serialized edits lost an update: %q", data)
	}
}

func TestSearchToolsTruncationMetadata(t *testing.T) {
	dir := t.TempDir()
	var builder strings.Builder
	for i := 0; i < 150; i++ {
		builder.WriteString("match line ")
		builder.WriteString(strings.Repeat("x", 600))
		builder.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewToolRegistry(dir, nil, []string{"grep"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := registry.Execute(context.Background(), ToolCall{Name: "grep", Arguments: json.RawMessage(`{"pattern":"match line","path":".","limit":10}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Details) == 0 {
		t.Fatal("grep truncation metadata missing")
	}
	details := string(result.Details)
	if !strings.Contains(details, "matchLimitReached") && !strings.Contains(details, "linesTruncated") && !strings.Contains(details, "truncation") {
		t.Fatalf("grep metadata incomplete: %s", details)
	}
}

func TestRegistryDeclarationsReflectActiveSet(t *testing.T) {
	registry, err := NewToolRegistry(t.TempDir(), nil, []string{"read", "write"}, nil, ToolHooks{})
	if err != nil {
		t.Fatal(err)
	}
	declarations := registry.Declarations()
	if len(declarations) != 2 {
		t.Fatalf("declarations = %d", len(declarations))
	}
	for _, declaration := range declarations {
		if declaration.Name != "read" && declaration.Name != "write" {
			t.Fatalf("unexpected declaration %q", declaration.Name)
		}
		if len(declaration.Input.Schema) == 0 {
			t.Fatalf("declaration %q has no schema", declaration.Name)
		}
	}
	if err := registry.SetActive([]string{"bash"}); err != nil {
		t.Fatal(err)
	}
	declarations = registry.Declarations()
	if len(declarations) != 1 || declarations[0].Name != "bash" {
		t.Fatalf("declarations after SetActive = %v", declarations)
	}
}

func TestWrapToolDefinitionRoundTrip(t *testing.T) {
	definition := ToolDefinition{
		Name:        "echo",
		Description: "echo",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Execute: func(_ context.Context, arguments json.RawMessage) (ToolResult, error) {
			return ToolResult{
				Content: []aitypes.ContentBlock{aitypes.TextBlock("image+text")},
				Details: json.RawMessage(`{"kept":true}`),
			}, nil
		},
	}
	agentTool := WrapToolDefinition(definition)
	result, err := agentTool.Execute("1", json.RawMessage(`{"text":"hi"}`), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("content lost: %v", result.Content)
	}
	if _, ok := result.Details.(map[string]any); !ok {
		t.Fatalf("details lost: %#v", result.Details)
	}
	roundTrip := CreateToolDefinitionFromAgentTool(agentTool)
	again, err := roundTrip.Execute(context.Background(), json.RawMessage(`{"text":"hi"}`))
	if err != nil || len(again.Content) != 1 {
		t.Fatalf("round trip failed: %v %v", err, again)
	}
	if !strings.Contains(string(again.Details), "kept") {
		t.Fatalf("round trip details lost: %s", again.Details)
	}
}

func encodeToolResult(t *testing.T, result ToolResult) string {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
