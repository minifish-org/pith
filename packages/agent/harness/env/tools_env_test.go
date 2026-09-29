package env

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	outputcapture "github.com/minifish-org/pith/packages/agent/harness/utils/output_capture"
)

func boolPointer(value bool) *bool { return &value }

func intPointer(value int) *int { return &value }

func floatPointer(value float64) *float64 { return &value }

func TestLocalExecutionEnvReadsWritesListsRemoves(t *testing.T) {
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()

	if got := harnesstypes.GetOrThrow(environment.AbsolutePath("nested/child", ctx)); got != filepath.Join(root, "nested", "child") {
		t.Fatalf("AbsolutePath = %q", got)
	}
	if got := harnesstypes.GetOrThrow(environment.JoinPath([]string{root, "nested", "child"}, ctx)); got != filepath.Join(root, "nested", "child") {
		t.Fatalf("JoinPath = %q", got)
	}
	harnesstypes.GetOrThrow(environment.CreateDir("nested/child", nil, ctx))
	harnesstypes.GetOrThrow(environment.WriteFile("nested/child/file.txt", []byte("hel"), ctx))
	harnesstypes.GetOrThrow(environment.AppendFile("nested/child/file.txt", []byte("lo"), ctx))
	if got := harnesstypes.GetOrThrow(environment.ReadTextFile("nested/child/file.txt", ctx)); got != "hello" {
		t.Fatalf("ReadTextFile = %q", got)
	}
	if got := harnesstypes.GetOrThrow(environment.ReadTextLines("nested/child/file.txt", &harnesstypes.ReadTextLinesOptions{MaxLines: intPointer(1)}, ctx)); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("ReadTextLines = %#v", got)
	}
	if got := string(harnesstypes.GetOrThrow(environment.ReadBinaryFile("nested/child/file.txt", ctx))); got != "hello" {
		t.Fatalf("ReadBinaryFile = %q", got)
	}

	entries := harnesstypes.GetOrThrow(environment.ListDir("nested/child", ctx))
	if len(entries) != 1 || entries[0].Name != "file.txt" || entries[0].Kind != harnesstypes.FileKindFile || entries[0].Size != 5 {
		t.Fatalf("ListDir = %#v", entries)
	}
	if !harnesstypes.GetOrThrow(environment.Exists("nested/child/file.txt", ctx)) {
		t.Fatalf("Exists should be true")
	}
	harnesstypes.GetOrThrow(environment.Remove("nested/child/file.txt", nil, ctx))
	if harnesstypes.GetOrThrow(environment.Exists("nested/child/file.txt", ctx)) {
		t.Fatalf("Exists should be false after removal")
	}
}

func TestLocalExecutionEnvExpandsHomeAndFileURLs(t *testing.T) {
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if got := harnesstypes.GetOrThrow(environment.AbsolutePath("~/pi-node-env-test", ctx)); got != filepath.Join(home, "pi-node-env-test") {
		t.Fatalf("home expansion = %q", got)
	}
	filePath := filepath.Join(root, "file with spaces.txt")
	fileURL := "file://" + strings.ReplaceAll(filePath, " ", "%20")
	got := harnesstypes.GetOrThrow(environment.AbsolutePath(fileURL, ctx))
	if got != filePath {
		t.Fatalf("file URL expansion = %q, want %q", got, filePath)
	}
}

func TestLocalExecutionEnvFileInfoAndSymlinks(t *testing.T) {
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()
	harnesstypes.GetOrThrow(environment.CreateDir("dir", nil, ctx))
	harnesstypes.GetOrThrow(environment.WriteFile("dir/file.txt", []byte("hello"), ctx))
	if err := os.Symlink(filepath.Join(root, "dir", "file.txt"), filepath.Join(root, "file-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	info := harnesstypes.GetOrThrow(environment.FileInfo("file-link", ctx))
	if info.Kind != harnesstypes.FileKindSymlink {
		t.Fatalf("symlink kind = %q", info.Kind)
	}
	canonical := harnesstypes.GetOrThrow(environment.CanonicalPath("file-link", ctx))
	expected, err := filepath.EvalSymlinks(filepath.Join(root, "dir", "file.txt"))
	if err != nil {
		expected = filepath.Join(root, "dir", "file.txt")
	}
	if canonical != expected {
		t.Fatalf("canonical = %q, want %q", canonical, expected)
	}
}

func TestLocalExecutionEnvMissingPathsAndAbort(t *testing.T) {
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()

	info := environment.FileInfo("missing.txt", ctx)
	if info.OK || info.Error.Code != harnesstypes.FileErrorNotFound {
		t.Fatalf("missing FileInfo = %#v", info)
	}
	if harnesstypes.GetOrThrow(environment.Exists("missing.txt", ctx)) {
		t.Fatalf("missing path should not exist")
	}

	harnesstypes.GetOrThrow(environment.WriteFile("file.txt", []byte("hello"), ctx))
	aborted, cancel := context.WithCancel(context.Background())
	cancel()
	results := []bool{
		environment.ReadTextFile("file.txt", aborted).OK,
		environment.WriteFile("other.txt", []byte("hello"), aborted).OK,
		environment.RenameFile("file.txt", "renamed.txt", aborted).OK,
		environment.ListDir(".", aborted).OK,
	}
	for index, ok := range results {
		if ok {
			t.Fatalf("operation %d should have been aborted", index)
		}
	}
}

func TestLocalExecutionEnvExecCapturesOutputAndEnv(t *testing.T) {
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()

	var output *harnesstypes.ShellOutputView
	collect := func(update harnesstypes.ShellOutputUpdate, _ harnesstypes.Context) {
		view := outputcapture.ApplyShellOutputUpdate(output, update)
		output = &view
	}

	result := harnesstypes.GetOrThrow(environment.Exec("printf '%s:%s' \"$PWD\" \"$PI_TOOLS_ENV_TEST\"", &harnesstypes.ShellExecOptions{Env: map[string]string{"PI_TOOLS_ENV_TEST": "ok"}, OnUpdate: collect}, ctx))
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	if output == nil || output.Text != realRoot+":ok" {
		t.Fatalf("env output = %#v, want %q", output, realRoot+":ok")
	}
	if result.ExitCode != 0 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}

	updates := []string{}
	output = nil
	_ = harnesstypes.GetOrThrow(environment.Exec("printf out; printf err >&2", &harnesstypes.ShellExecOptions{OnUpdate: func(update harnesstypes.ShellOutputUpdate, _ harnesstypes.Context) {
		updates = append(updates, update.Kind)
		view := outputcapture.ApplyShellOutputUpdate(output, update)
		output = &view
	}}, ctx))
	if len(updates) == 0 || updates[0] != harnesstypes.ShellOutputUpdateReplace {
		t.Fatalf("first update kind = %#v", updates)
	}
	if output == nil || !strings.Contains(output.Text, "out") || !strings.Contains(output.Text, "err") {
		t.Fatalf("combined output = %#v", output)
	}
}

func TestLocalExecutionEnvExecExitCodesAndErrors(t *testing.T) {
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()

	nonzero := harnesstypes.GetOrThrow(environment.Exec("exit 7", nil, ctx))
	if nonzero.ExitCode != 7 || nonzero.Truncation.TotalBytes != 0 {
		t.Fatalf("non-zero exit = %#v", nonzero)
	}

	timedOut := environment.Exec("sleep 5", &harnesstypes.ShellExecOptions{Timeout: floatPointer(0.01)}, ctx)
	if timedOut.OK || timedOut.Error.Code != harnesstypes.ExecutionErrorTimeout {
		t.Fatalf("timeout result = %#v", timedOut)
	}

	missing := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: filepath.Join(root, "missing")})
	missingResult := missing.Exec("printf ok", nil, ctx)
	if missingResult.OK || missingResult.Error.Code != harnesstypes.ExecutionErrorSpawn || !strings.Contains(missingResult.Error.Message, "Working directory does not exist") {
		t.Fatalf("missing cwd result = %#v", missingResult)
	}

	missingShell := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root, ShellPath: stringPointer(filepath.Join(root, "missing-shell"))})
	missingShellResult := missingShell.Exec("printf ok", nil, ctx)
	if missingShellResult.OK || missingShellResult.Error.Code != harnesstypes.ExecutionErrorShellUnavailable {
		t.Fatalf("missing shell result = %#v", missingShellResult)
	}

	notExecutable := filepath.Join(root, "not-executable-shell")
	harnesstypes.GetOrThrow(environment.WriteFile(notExecutable, []byte("not executable"), ctx))
	spawnEnv := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root, ShellPath: &notExecutable})
	spawnResult := spawnEnv.Exec("printf ok", nil, ctx)
	if spawnResult.OK || spawnResult.Error.Code != harnesstypes.ExecutionErrorSpawn {
		t.Fatalf("spawn error result = %#v", spawnResult)
	}
}

func TestLocalExecutionEnvExecCancellationAndCleanup(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})

	abortContext, cancel := context.WithCancel(context.Background())
	pending := make(chan harnesstypes.Result[harnesstypes.ShellExecResult, harnesstypes.ExecutionError], 1)
	go func() { pending <- environment.Exec("sleep 30", nil, abortContext) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case result := <-pending:
		if result.OK || result.Error.Code != harnesstypes.ExecutionErrorAborted {
			t.Fatalf("cancelled exec = %#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("cancelled exec did not settle")
	}

	cleanupContext := context.Background()
	execution := make(chan harnesstypes.Result[harnesstypes.ShellExecResult, harnesstypes.ExecutionError], 1)
	go func() { execution <- environment.Exec("touch started; sleep 30", nil, cleanupContext) }()
	for attempt := 0; attempt < 200; attempt++ {
		if exists := harnesstypes.GetOrThrow(environment.Exists("started", cleanupContext)); exists {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !harnesstypes.GetOrThrow(environment.Exists("started", cleanupContext)) {
		t.Fatalf("shell command did not start")
	}
	environment.Cleanup(cleanupContext)
	select {
	case result := <-execution:
		if !result.OK {
			t.Fatalf("cleanup should let the command settle: %#v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("cleanup did not terminate the shell")
	}
}

func TestLocalExecutionEnvSpillPreservesCompleteOutput(t *testing.T) {
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	ctx := context.Background()

	capture := &harnesstypes.ShellOutputCaptureOptions{
		Limits: harnesstypes.ShellOutputLimits{MaxBytes: 10, MaxLines: 10, Retain: retentionPointer(harnesstypes.ShellOutputTail)},
		Spill:  boolPointer(true),
	}
	result := harnesstypes.GetOrThrow(environment.Exec("yes x | head -n 100", &harnesstypes.ShellExecOptions{Capture: capture}, ctx))
	if result.SpillPath == nil {
		t.Fatalf("expected a spill path: %#v", result)
	}
	content := harnesstypes.GetOrThrow(environment.ReadTextFile(*result.SpillPath, ctx))
	if len(content) < len(strings.Repeat("x\n", 100)) {
		t.Fatalf("spill lost output: %d bytes", len(content))
	}
}

func TestLocalExecutionEnvSpillFailureIsReported(t *testing.T) {
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	environment.createTempFileHook = func(*harnesstypes.CreateTempFileOptions, harnesstypes.Context) harnesstypes.Result[string, harnesstypes.FileError] {
		return fileErr[string](harnesstypes.NewFileError(harnesstypes.FileErrorUnknown, "cannot create spill", nil, nil))
	}
	ctx := context.Background()
	capture := &harnesstypes.ShellOutputCaptureOptions{
		Limits: harnesstypes.ShellOutputLimits{MaxBytes: 10, MaxLines: 10, Retain: retentionPointer(harnesstypes.ShellOutputTail)},
		Spill:  boolPointer(true),
	}
	result := environment.Exec("printf 12345678901234567890", &harnesstypes.ShellExecOptions{Capture: capture}, ctx)
	if result.OK || result.Error.Code != harnesstypes.ExecutionErrorUnknown || !strings.Contains(result.Error.Message, "Failed to preserve complete shell output") {
		t.Fatalf("spill failure result = %#v", result)
	}
}

func TestLocalExecutionEnvSignalExitCode(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root := t.TempDir()
	environment := NewLocalExecutionEnv(LocalExecutionEnvOptions{Cwd: root})
	result := harnesstypes.GetOrThrow(environment.Exec("kill -9 $$", nil, context.Background()))
	if result.ExitCode != 128+9 {
		t.Fatalf("signal exit code = %d", result.ExitCode)
	}
}

func retentionPointer(value harnesstypes.ShellOutputRetention) *harnesstypes.ShellOutputRetention {
	return &value
}
