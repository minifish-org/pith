// Self-tests for the bash tool.
//
// The upstream implementation ships no dedicated bash-tool test file in the
// frozen reference ledger, so these tests translate the tools-bash acceptance
// scope into Go tests against the real LocalExecutionEnv and CreateBashTool:
// prepare/execution ordering, cwd/env handling, nonzero exits, signals,
// timeout and abort, bounded head/tail output, spill files, sanitization and
// progress updates. The behavior contract is
// packages/agent/src/harness/tools/bash.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
package tools

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	env "github.com/minifish-org/pith/packages/agent/harness/env"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	truncate "github.com/minifish-org/pith/packages/agent/harness/utils/truncate"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

type bashTool = harnesstypes.AgentHarnessTool[ExecutionToolContext, BashToolInput, *BashToolDetails]

func noopBashUpdateForTest(agenttypes.AgentToolResult[*BashToolDetails], *harnesstypes.AgentHarnessToolUpdateOptions) {
}

func bashTestEnvironment(t *testing.T) *env.LocalExecutionEnv {
	t.Helper()
	return env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: t.TempDir()})
}

func executeBashForTest(
	t *testing.T,
	tool bashTool,
	environment harnesstypes.ExecutionEnv,
	ctx harnesscontext.Context,
	input BashToolInput,
) (agenttypes.AgentToolResult[*BashToolDetails], error) {
	t.Helper()
	return tool.Execute("bash-test", input, noopBashUpdateForTest, ExecutionToolContext{Env: environment}, nil, ctx)
}

func bashTextOutput(result agenttypes.AgentToolResult[*BashToolDetails]) string {
	var parts []string
	for _, block := range result.Content {
		if block.IsText() && block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func bashFloatPointer(value float64) *float64 { return &value }

func TestCreateBashToolBasicOutput(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "printf 'hello\\n'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "hello\n" {
		t.Fatalf("text = %q", got)
	}
	if result.Details != nil {
		t.Fatalf("unexpected details: %#v", result.Details)
	}
}

func TestCreateBashToolNonZeroExitKeepsStderr(t *testing.T) {
	environment := bashTestEnvironment(t)
	_, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "printf 'err\\n' >&2; exit 3",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "err\n\n\nCommand exited with code 3"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCreateBashToolSilentNonZeroExit(t *testing.T) {
	environment := bashTestEnvironment(t)
	_, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "exit 7",
	})
	if err == nil || err.Error() != "Command exited with code 7" {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateBashToolSignalExitCode(t *testing.T) {
	environment := bashTestEnvironment(t)
	_, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "kill -TERM $$",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "Command exited with code 143"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCreateBashToolNoOutput(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: ":",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "(no output)" {
		t.Fatalf("text = %q", got)
	}
}

func TestCreateBashToolPreservesUnicodeOutput(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "printf '中文'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "中文" {
		t.Fatalf("text = %q", got)
	}
}

func TestCreateBashToolSanitizesControlOutput(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "printf 'a\\001b'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "ab" {
		t.Fatalf("text = %q", got)
	}
}

func TestCreateBashToolCombinesStdoutAndStderr(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "printf 'out\\n'; printf 'err\\n' >&2",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := bashTextOutput(result)
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Fatalf("combined output = %q", got)
	}
}

func TestCreateBashToolHandlesBinaryOutput(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "printf '\\377\\376abc'",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := bashTextOutput(result)
	if !strings.HasSuffix(got, "abc") {
		t.Fatalf("text = %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("output is not valid UTF-8: %q", got)
	}
}

func TestCreateBashToolInheritsEnvironment(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: `test -n "$PATH" && printf 'has-path'`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "has-path" {
		t.Fatalf("text = %q", got)
	}
}

func TestCreateBashToolPrepareInjectsEnv(t *testing.T) {
	environment := bashTestEnvironment(t)
	tool := CreateBashTool(&BashToolOptions{
		Prepare: func(execution *BashExecution, _ ExecutionToolContext, _ harnesscontext.Context) error {
			execution.Env["BASH_GREETING"] = "hi"
			return nil
		},
	})
	result, err := executeBashForTest(t, tool, environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: `printf '%s' "$BASH_GREETING"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "hi" {
		t.Fatalf("text = %q", got)
	}
}

func TestCreateBashToolPrepareControlsInheritEnv(t *testing.T) {
	environment := bashTestEnvironment(t)
	tool := CreateBashTool(&BashToolOptions{
		Prepare: func(execution *BashExecution, _ ExecutionToolContext, _ harnesscontext.Context) error {
			execution.InheritEnv = false
			execution.Env["ONLY"] = "yes"
			return nil
		},
	})
	result, err := executeBashForTest(t, tool, environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: `printf '%s' "${HOME-none}:$ONLY"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "none:yes" {
		t.Fatalf("text = %q", got)
	}
}

func TestCreateBashToolPrepareControlsCwd(t *testing.T) {
	environment := bashTestEnvironment(t)
	sub := filepath.Join(environment.Cwd(), "sub")
	if created := environment.CreateDir("sub", nil, harnesscontext.BackgroundContext); !created.OK {
		t.Fatalf("create dir: %v", created.Error.Message)
	}
	if written := environment.WriteFile("sub/marker.txt", []byte("marker\n"), harnesscontext.BackgroundContext); !written.OK {
		t.Fatalf("write marker: %v", written.Error.Message)
	}
	tool := CreateBashTool(&BashToolOptions{
		Prepare: func(execution *BashExecution, _ ExecutionToolContext, _ harnesscontext.Context) error {
			execution.Cwd = "sub"
			execution.Command = "cat marker.txt"
			return nil
		},
	})
	result, err := executeBashForTest(t, tool, environment, harnesscontext.BackgroundContext, BashToolInput{Command: "ignored"})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "marker\n" {
		t.Fatalf("text = %q (sub=%s)", got, sub)
	}
}

func TestCreateBashToolPrepareErrorAbortsBeforeSpawn(t *testing.T) {
	environment := bashTestEnvironment(t)
	sentinel := "prepare failed"
	tool := CreateBashTool(&BashToolOptions{
		Prepare: func(_ *BashExecution, _ ExecutionToolContext, _ harnesscontext.Context) error {
			return &bashSentinelError{message: sentinel}
		},
	})
	_, err := executeBashForTest(t, tool, environment, harnesscontext.BackgroundContext, BashToolInput{Command: "printf 'should-not-run'"})
	if err == nil || err.Error() != sentinel {
		t.Fatalf("error = %v", err)
	}
}

type bashSentinelError struct{ message string }

func (e *bashSentinelError) Error() string { return e.message }

func TestCreateBashToolCommandPrefix(t *testing.T) {
	environment := bashTestEnvironment(t)
	tool := CreateBashTool(&BashToolOptions{CommandPrefix: "echo prefix"})
	result, err := executeBashForTest(t, tool, environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "printf 'body'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := bashTextOutput(result); got != "prefix\nbody" {
		t.Fatalf("text = %q", got)
	}
}

func TestCreateBashToolTimeout(t *testing.T) {
	environment := bashTestEnvironment(t)
	_, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: "sleep 10",
		Timeout: bashFloatPointer(0.2),
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if got, want := err.Error(), "Command timed out after 0.2 seconds"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCreateBashToolAbort(t *testing.T) {
	environment := bashTestEnvironment(t)
	ctx, cancel := context.WithCancel(harnesscontext.BackgroundContext)
	defer cancel()
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()
	_, err := executeBashForTest(t, CreateBashTool(nil), environment, ctx, BashToolInput{Command: "sleep 10"})
	if err == nil {
		t.Fatal("expected abort error")
	}
	if got, want := err.Error(), "Command aborted"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCreateBashToolRejectsInvalidTimeout(t *testing.T) {
	environment := bashTestEnvironment(t)
	cases := []struct {
		name    string
		timeout float64
		want    string
	}{
		{"zero", 0, "Invalid timeout: must be a finite number of seconds"},
		{"negative", -1, "Invalid timeout: must be a finite number of seconds"},
		{"infinity", math.Inf(1), "Invalid timeout: must be a finite number of seconds"},
		{"nan", math.NaN(), "Invalid timeout: must be a finite number of seconds"},
		{"too-large", maxBashTimeoutSeconds + 1, "Invalid timeout: maximum is 2147483.647 seconds"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			timeout := testCase.timeout
			_, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
				Command: "printf 'never'",
				Timeout: &timeout,
			})
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestCreateBashToolTruncatesTailAndSpills(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: `i=1; while [ "$i" -le 3000 ]; do printf 'line %s\n' "$i"; i=$((i+1)); done`,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := bashTextOutput(result)
	if !strings.Contains(text, "[Showing lines 1001-3000 of 3000. Full output: ") {
		t.Fatalf("missing line-truncation suffix: %q", tailOf(text, 400))
	}
	if result.Details == nil || result.Details.Truncation == nil {
		t.Fatalf("missing truncation details: %#v", result.Details)
	}
	truncation := result.Details.Truncation
	if !truncation.Truncated || truncation.TruncatedBy == nil || *truncation.TruncatedBy != "lines" {
		t.Fatalf("truncation = %#v", truncation)
	}
	if truncation.TotalLines != 3000 || truncation.OutputLines != 2000 {
		t.Fatalf("truncation counts = %#v", truncation)
	}
	if truncation.MaxLines != truncate.DefaultMaxLines || truncation.MaxBytes != truncate.DefaultMaxBytes {
		t.Fatalf("truncation limits = %#v", truncation)
	}
	if result.Details.FullOutputPath == nil {
		t.Fatal("missing full output path")
	}
	spillPath := *result.Details.FullOutputPath
	defer func() { _ = os.Remove(spillPath) }()
	full, err := os.ReadFile(spillPath)
	if err != nil {
		t.Fatalf("read spill: %v", err)
	}
	if got := strings.Count(string(full), "\n"); got != 3000 {
		t.Fatalf("spill line count = %d", got)
	}
	if !strings.HasPrefix(string(full), "line 1\n") || !strings.HasSuffix(string(full), "line 3000\n") {
		t.Fatalf("spill boundaries = %q", full[:minInt(len(full), 32)])
	}
}

func TestCreateBashToolTruncatesLongPartialLine(t *testing.T) {
	environment := bashTestEnvironment(t)
	result, err := executeBashForTest(t, CreateBashTool(nil), environment, harnesscontext.BackgroundContext, BashToolInput{
		Command: `printf 'a%.0s' {1..60000}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	text := bashTextOutput(result)
	if !strings.Contains(text, "[Showing last 50.0KB of line 1 (line is 58.6KB). Full output: ") {
		t.Fatalf("missing partial-line suffix: %q", tailOf(text, 400))
	}
	if result.Details == nil || result.Details.Truncation == nil {
		t.Fatalf("missing truncation details: %#v", result.Details)
	}
	truncation := result.Details.Truncation
	if !truncation.LastLinePartial {
		t.Fatalf("expected lastLinePartial, got %#v", truncation)
	}
	if truncation.TruncatedBy == nil || *truncation.TruncatedBy != "bytes" {
		t.Fatalf("truncatedBy = %#v", truncation.TruncatedBy)
	}
	if truncation.TotalLines != 1 || truncation.OutputLines != 1 {
		t.Fatalf("counts = %#v", truncation)
	}
	if truncation.OutputBytes != truncate.DefaultMaxBytes {
		t.Fatalf("outputBytes = %d", truncation.OutputBytes)
	}
	if result.Details.FullOutputPath != nil {
		defer func() { _ = os.Remove(*result.Details.FullOutputPath) }()
	}
}

func TestCreateBashToolProgressUpdates(t *testing.T) {
	environment := bashTestEnvironment(t)
	var mu sync.Mutex
	var snapshots []agenttypes.AgentToolResult[*BashToolDetails]
	tool := CreateBashTool(nil)
	_, err := tool.Execute(
		"bash-test",
		BashToolInput{Command: "printf 'streamed\\n'"},
		func(partial agenttypes.AgentToolResult[*BashToolDetails], _ *harnesstypes.AgentHarnessToolUpdateOptions) {
			mu.Lock()
			snapshots = append(snapshots, partial)
			mu.Unlock()
		},
		ExecutionToolContext{Env: environment},
		nil,
		harnesscontext.BackgroundContext,
	)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(snapshots) < 2 {
		t.Fatalf("expected at least an initial and a content update, got %d", len(snapshots))
	}
	if len(snapshots[0].Content) != 0 || snapshots[0].Details != nil {
		t.Fatalf("first update = %#v", snapshots[0])
	}
	if len(snapshots[len(snapshots)-1].Content) == 0 || snapshots[len(snapshots)-1].Content[0].Text == nil {
		t.Fatalf("last update = %#v", snapshots[len(snapshots)-1])
	}
	if !strings.Contains(snapshots[len(snapshots)-1].Content[0].Text.Text, "streamed") {
		t.Fatalf("last update text = %q", snapshots[len(snapshots)-1].Content[0].Text.Text)
	}
}

func TestCreateBashToolPrepareArguments(t *testing.T) {
	tool := CreateBashTool(nil)
	params, err := tool.PrepareArguments(json.RawMessage(`{"command":"echo hi","timeout":2.5}`))
	if err != nil {
		t.Fatal(err)
	}
	if params.Command != "echo hi" || params.Timeout == nil || *params.Timeout != 2.5 {
		t.Fatalf("params = %#v", params)
	}
	empty, err := tool.PrepareArguments(nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Command != "" || empty.Timeout != nil {
		t.Fatalf("nil params = %#v", empty)
	}
}

func TestCreateBashToolDescription(t *testing.T) {
	tool := CreateBashTool(nil)
	if tool.Tool.Name != "bash" || tool.Label != "bash" {
		t.Fatalf("identity = %q/%q", tool.Tool.Name, tool.Label)
	}
	if !strings.Contains(tool.Tool.Description, "last 2000 lines or 50KB") {
		t.Fatalf("description = %q", tool.Tool.Description)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(tool.Tool.Input.Schema, &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "command" {
		t.Fatalf("required = %#v", schema.Required)
	}
}

func tailOf(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[len(text)-limit:]
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}
