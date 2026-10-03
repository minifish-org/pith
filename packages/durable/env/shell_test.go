package env

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newLocalWith(t *testing.T, options LocalOptions) *Local {
	t.Helper()
	local, err := NewLocalWithOptions(options)
	if err != nil {
		t.Fatalf("NewLocalWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = local.Cleanup(context.Background()) })
	return local
}

func shellCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func collect(t *testing.T, local *Local, ctx context.Context, command string, options ExecOptions) (ExecResult, []byte, error) {
	t.Helper()
	var output []byte
	if options.OnOutput == nil {
		options.OnOutput = func(_ context.Context, chunk []byte) error {
			output = append(output, chunk...)
			return nil
		}
	}
	result, err := local.Exec(ctx, command, options)
	return result, output, err
}

func writeLines(t *testing.T, path string, count int) {
	t.Helper()
	var builder strings.Builder
	for index := 1; index <= count; index++ {
		builder.WriteString("line-")
		builder.WriteString(itoa(index))
		builder.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func TestExecStreamsCombinedOutputAndEnvironment(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	result, output, err := collect(t, local, ctx, `printf 'out'; printf 'err' >&2`, ExecOptions{})
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("exec: %#v %v", result, err)
	}
	if !strings.Contains(string(output), "out") || !strings.Contains(string(output), "err") {
		t.Fatalf("combined output: %q", output)
	}
}

func TestExecAppliesEnvironmentOverrides(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	_, output, err := collect(t, local, ctx, `printf '%s' "$PI_ENV_OVERRIDE"`, ExecOptions{Env: map[string]string{"PI_ENV_OVERRIDE": "ok"}})
	if err != nil || string(output) != "ok" {
		t.Fatalf("env override: %q %v", output, err)
	}
}

func TestExecConfiguredShellEnvironmentAndInheritFalse(t *testing.T) {
	ctx := shellCtx(t)
	const inherited = "PI_ENV_INHERITED_UNIQUE"
	t.Setenv(inherited, "host")
	local := newLocalWith(t, LocalOptions{CWD: t.TempDir(), ShellEnv: map[string]string{"PI_ENV_CONFIGURED": "configured"}})
	_, output, err := collect(t, local, ctx,
		`printf '%s:%s:%s' "${PI_ENV_INHERITED_UNIQUE-}" "${PI_ENV_CONFIGURED-}" "${PI_ENV_EXPLICIT-}"`,
		ExecOptions{InheritEnv: boolPtr(false), Env: map[string]string{"PI_ENV_EXPLICIT": "explicit"}})
	if err != nil || string(output) != "::explicit" {
		t.Fatalf("inherit false: %q %v", output, err)
	}
	_, output, err = collect(t, local, ctx,
		`printf '%s:%s' "${PI_ENV_INHERITED_UNIQUE-}" "${PI_ENV_CONFIGURED-}"`,
		ExecOptions{})
	if err != nil || string(output) != "host:configured" {
		t.Fatalf("inherit true: %q %v", output, err)
	}
}

func boolPtr(value bool) *bool { return &value }

func TestExecNonZeroExitIsOrdinary(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	result, err := local.Exec(ctx, "exit 7", ExecOptions{})
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("nonzero exit: %#v %v", result, err)
	}
}

func TestExecMapsSignalExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX signal semantics")
	}
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	result, err := local.Exec(ctx, "kill -9 $$", ExecOptions{})
	if err != nil || result.ExitCode != 128+9 {
		t.Fatalf("signal exit: %#v %v", result, err)
	}
}

func TestExecTimeout(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	_, err := local.Exec(ctx, "sleep 5", ExecOptions{Timeout: 100 * time.Millisecond})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorTimeout {
		t.Fatalf("timeout error: %v", err)
	}
}

func TestExecRejectsInvalidTimeoutBeforeSpawning(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	for _, timeout := range []time.Duration{-time.Second, time.Duration(maxTimeoutMs+1000) * time.Millisecond} {
		_, err := local.Exec(ctx, `touch spawned`, ExecOptions{Timeout: timeout})
		var executionErr *ExecutionError
		if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorTimeout {
			t.Fatalf("invalid timeout %v: %v", timeout, err)
		}
	}
	if present, _ := local.Exists(ctx, "spawned"); present {
		t.Fatal("invalid timeout must not run the command")
	}
}

func TestExecReportsCallbackError(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	_, err := local.Exec(ctx, "printf out", ExecOptions{OnOutput: func(context.Context, []byte) error {
		return errors.New("callback failed")
	}})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorCallback {
		t.Fatalf("callback error: %v", err)
	}
	if !strings.Contains(err.Error(), "callback failed") {
		t.Fatalf("callback error message: %v", err)
	}
}

func TestExecRecoversCallbackPanic(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	_, err := local.Exec(ctx, "printf out", ExecOptions{OnOutput: func(context.Context, []byte) error {
		panic("observer exploded")
	}})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorCallback {
		t.Fatalf("panic callback error: %v", err)
	}
}

func TestExecShellUnavailable(t *testing.T) {
	ctx := shellCtx(t)
	root := t.TempDir()
	local := newLocalWith(t, LocalOptions{CWD: root, ShellPath: filepath.Join(root, "missing-shell")})
	_, err := local.Exec(ctx, "printf ok", ExecOptions{})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorShellUnavailable {
		t.Fatalf("shell unavailable: %v", err)
	}
}

func TestExecSpawnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable-bit semantics")
	}
	ctx := shellCtx(t)
	root := t.TempDir()
	notExecutable := filepath.Join(root, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := newLocalWith(t, LocalOptions{CWD: root, ShellPath: notExecutable})
	_, err := local.Exec(ctx, "printf ok", ExecOptions{})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorSpawn {
		t.Fatalf("spawn error: %v", err)
	}
}

func TestExecMissingWorkingDirectory(t *testing.T) {
	ctx := shellCtx(t)
	root := t.TempDir()
	local := newLocal(t, filepath.Join(root, "missing"))
	_, err := local.Exec(ctx, "printf ok", ExecOptions{})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorSpawn {
		t.Fatalf("missing cwd: %v", err)
	}
	if !strings.Contains(err.Error(), "Working directory does not exist") {
		t.Fatalf("missing cwd message: %v", err)
	}
}

func TestExecPreCancelledDoesNotSpawn(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := local.Exec(cancelled, "touch spawned", ExecOptions{})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorAborted {
		t.Fatalf("pre-cancel: %v", err)
	}
	if present, _ := local.Exists(ctx, "spawned"); present {
		t.Fatal("pre-cancelled command ran")
	}
}

func TestExecInFlightCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell loop")
	}
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := local.Exec(runCtx, "sleep 30", ExecOptions{})
		done <- err
	}()
	cancel()
	err := <-done
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorAborted {
		t.Fatalf("in-flight cancel: %v", err)
	}
}

func TestExecDecodesUTF8AcrossChunks(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	_, output, err := collect(t, local, ctx, `printf '\303'; printf '\251'`, ExecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "é" {
		t.Fatalf("split utf8: %q", output)
	}
}

func TestExecNoSpillBelowThreshold(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	result, _, err := collect(t, local, ctx, "printf short", ExecOptions{Spill: &SpillOptions{AfterBytes: 100, AfterLines: 10}})
	if err != nil || result.SpillPath != "" {
		t.Fatalf("no spill: %#v %v", result, err)
	}
}

func TestExecExactlyAtByteThresholdIsNotOverflow(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	result, _, err := collect(t, local, ctx, "printf abc", ExecOptions{Spill: &SpillOptions{AfterBytes: 3, AfterLines: 100}})
	if err != nil || result.SpillPath != "" {
		t.Fatalf("exact threshold: %#v %v", result, err)
	}
}

func TestExecSpillsCompleteStreamOnOverflow(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	result, output, err := collect(t, local, ctx, "printf abcdef; printf err >&2; exit 7",
		ExecOptions{Spill: &SpillOptions{AfterBytes: 3, AfterLines: 100}})
	if err != nil || result.ExitCode != 7 || result.SpillPath == "" {
		t.Fatalf("spill overflow: %#v %v", result, err)
	}
	data, err := os.ReadFile(result.SpillPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(output) || !strings.Contains(string(data), "abcdef") || !strings.Contains(string(data), "err") {
		t.Fatalf("spill lost raw stream: %q vs %q", data, output)
	}
}

func TestExecSpillPreservesRawBytes(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	raw := []byte{0x66, 0x80, 0x00, 0x6f}
	source := filepath.Join(local.CWD(), "raw.bin")
	if err := os.WriteFile(source, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := local.Exec(ctx, "cat '"+source+"'", ExecOptions{Spill: &SpillOptions{AfterBytes: 1, AfterLines: 10}})
	if err != nil || result.SpillPath == "" {
		t.Fatalf("spill raw: %#v %v", result, err)
	}
	data, err := os.ReadFile(result.SpillPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(raw) {
		t.Fatalf("raw spill bytes: %v", data)
	}
}

func TestExecSpillPreservesCompleteLargeOutput(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	source := filepath.Join(local.CWD(), "large.bin")
	payload := strings.Repeat("x", 500_000)
	if err := os.WriteFile(source, []byte(payload), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := local.Exec(ctx, "cat '"+source+"'", ExecOptions{Spill: &SpillOptions{AfterBytes: 10, AfterLines: 10}})
	if err != nil || result.SpillPath == "" {
		t.Fatalf("large spill: %#v %v", result, err)
	}
	data, err := os.ReadFile(result.SpillPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != len(payload) {
		t.Fatalf("large spill length: %d", len(data))
	}
}

func TestExecSpillsOnLineThreshold(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	source := filepath.Join(local.CWD(), "lines.txt")
	writeLines(t, source, 15000)
	result, err := local.Exec(ctx, "cat '"+source+"'", ExecOptions{Spill: &SpillOptions{AfterBytes: 1024 * 1024, AfterLines: 100}})
	if err != nil || result.SpillPath == "" {
		t.Fatalf("line spill: %#v %v", result, err)
	}
	lines, err := local.ReadTextLines(ctx, result.SpillPath, ReadTextLinesOptions{})
	if err != nil || len(lines) != 15000 || lines[14999] != "line-15000" {
		t.Fatalf("spilled lines: %d %v", len(lines), err)
	}
}

func TestExecSpillFailsRatherThanSilentlyLosingOutput(t *testing.T) {
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	local.createTempFileHook = func(context.Context, TempFileOptions) (string, error) {
		return filepath.Join(local.CWD(), "missing", "spill.log"), nil
	}
	_, err := local.Exec(ctx, "printf 12345678901234567890", ExecOptions{Spill: &SpillOptions{AfterBytes: 10, AfterLines: 10}})
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorUnknown {
		t.Fatalf("spill failure: %v", err)
	}
	if !strings.Contains(err.Error(), "Failed to preserve complete shell output") {
		t.Fatalf("spill failure message: %v", err)
	}
}

func TestExecTimeoutAfterSpillCreatedPreservesPathAndBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell loop")
	}
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	ready := make(chan string, 1)
	local.onSpillWritten = func(path string) {
		select {
		case ready <- path:
		default:
		}
	}
	done := make(chan error, 1)
	go func() {
		_, err := local.Exec(ctx, "printf ABCDEFGHIJ; sleep 5", ExecOptions{
			Timeout: 500 * time.Millisecond,
			Spill:   &SpillOptions{AfterBytes: 5, AfterLines: 100},
		})
		done <- err
	}()
	var path string
	select {
	case path = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("spill was never created")
	}
	err := <-done
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorTimeout {
		t.Fatalf("timeout after spill: %v", err)
	}
	if executionErr.SpillPath != path {
		t.Fatalf("spill path lost: %q vs %q", executionErr.SpillPath, path)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "ABCDEFGHIJ" {
		t.Fatalf("spill bytes: %q", data)
	}
}

func TestExecCancellationAfterSpillCreatedPreservesPathAndBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell loop")
	}
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	ready := make(chan string, 1)
	local.onSpillWritten = func(path string) {
		select {
		case ready <- path:
		default:
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := local.Exec(runCtx, "printf ABCDEFGHIJ; sleep 5", ExecOptions{
			Spill: &SpillOptions{AfterBytes: 5, AfterLines: 100},
		})
		done <- err
	}()
	var path string
	select {
	case path = <-ready:
	case <-time.After(10 * time.Second):
		t.Fatal("spill was never created")
	}
	cancel()
	err := <-done
	var executionErr *ExecutionError
	if !errors.As(err, &executionErr) || executionErr.Code != ExecutionErrorAborted {
		t.Fatalf("cancel after spill: %v", err)
	}
	if executionErr.SpillPath != path {
		t.Fatalf("spill path lost: %q vs %q", executionErr.SpillPath, path)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "ABCDEFGHIJ" {
		t.Fatalf("spill bytes: %q", data)
	}
}

func TestCleanupTerminatesActiveCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell loop")
	}
	ctx := shellCtx(t)
	local := newLocal(t, t.TempDir())
	done := make(chan error, 1)
	go func() {
		_, err := local.Exec(ctx, "touch started; sleep 30", ExecOptions{})
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if present, _ := local.Exists(ctx, "started"); present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("command never started")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := local.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cleanup did not terminate the running command")
	}
}

func TestLegacyWSLBashTransport(t *testing.T) {
	if !isLegacyWSLBashPath(`C:\Windows\System32\bash.exe`) {
		t.Fatal("windows system32 bash must be treated as legacy WSL")
	}
	if !isLegacyWSLBashPath("c:/windows/sysnative/bash.exe") {
		t.Fatal("slash form must be normalized")
	}
	if isLegacyWSLBashPath("/bin/bash") {
		t.Fatal("posix bash is not legacy WSL")
	}
	config := getBashShellConfig(`C:\Windows\System32\bash.exe`)
	if !config.commandFromStdin || len(config.args) != 1 || config.args[0] != "-s" {
		t.Fatalf("legacy config: %#v", config)
	}
	config = getBashShellConfig("/bin/bash")
	if config.commandFromStdin || len(config.args) != 1 || config.args[0] != "-c" {
		t.Fatalf("posix config: %#v", config)
	}
}

func TestResolveTimeout(t *testing.T) {
	if timeout, err := resolveTimeout(0); err != nil || timeout != nil {
		t.Fatalf("zero timeout: %v", err)
	}
	if timeout, err := resolveTimeout(2 * time.Second); err != nil || timeout == nil || *timeout != 2*time.Second {
		t.Fatalf("valid timeout: %v", err)
	}
	if _, err := resolveTimeout(-time.Second); err == nil || err.Code != ExecutionErrorTimeout {
		t.Fatalf("negative timeout: %v", err)
	}
}
