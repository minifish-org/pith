package durable_env_test

import (
	"context"
	"errors"
	denv "github.com/minifish-org/pith/packages/durable/env"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func local(t *testing.T) *denv.Local {
	t.Helper()
	e, err := denv.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := e.Cleanup(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return e
}

func TestPortsmithJudgeDurableEnvironmentFiles(t *testing.T) {
	ctx := context.Background()
	e := local(t)
	e2, err := denv.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if e.ID() == "" || e.ID() != e2.ID() {
		t.Fatal("local namespace identity must be shared across CWDs")
	}
	p, err := e.AbsolutePath(ctx, "lines.txt")
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("界", 30000)
	if err = e.WriteFile(ctx, p, []byte(long+"\r\nlast")); err != nil {
		t.Fatal(err)
	}
	r, err := e.OpenTextLineReader(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close(ctx)
	l, err := r.ReadLine(ctx)
	if err != nil || l == nil || l.Text != long || !l.Terminated {
		t.Fatalf("long CRLF line corrupted: %v", err)
	}
	l, err = r.ReadLine(ctx)
	if err != nil || l == nil || l.Text != "last" || l.Terminated {
		t.Fatalf("unterminated line: %#v %v", l, err)
	}
	l, err = r.ReadLine(ctx)
	if err != nil || l != nil {
		t.Fatalf("EOF: %#v %v", l, err)
	}
	if err = e.TruncateFile(ctx, p, 3); err != nil {
		t.Fatal(err)
	}
	if err = e.AppendFile(ctx, p, []byte("!")); err != nil {
		t.Fatal(err)
	}
	if err = e.FlushFile(ctx, p); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(e.CWD(), "renamed.txt")
	if err = e.RenameFile(ctx, p, dst); err != nil {
		t.Fatal(err)
	}
	got, err := e.ReadTextFile(ctx, dst)
	if err != nil || got != "界!" {
		t.Fatalf("file operation data: %q %v", got, err)
	}
	info, err := e.FileInfo(ctx, dst)
	if err != nil || info.Kind != "file" || info.Size != 4 {
		t.Fatalf("file metadata: %#v %v", info, err)
	}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(e.CWD(), "alias.txt")
		if err = os.Symlink(dst, alias); err != nil {
			t.Fatal(err)
		}
		a, ae := e.CanonicalPath(ctx, alias)
		b, be := e.CanonicalPath(ctx, dst)
		if ae != nil || be != nil || a != b {
			t.Fatalf("canonical aliases: %q %q %v %v", a, b, ae, be)
		}
	}
	_, err = e.ReadTextFile(ctx, filepath.Join(e.CWD(), "missing"))
	var fe *denv.FileError
	if !errors.As(err, &fe) || fe.Code != "not_found" {
		t.Fatalf("missing file must be typed: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err = e.WriteFile(cancelled, dst, []byte("corrupt"))
	if !errors.As(err, &fe) || fe.Code != "aborted" {
		t.Fatalf("precancel file error: %v", err)
	}
	got, _ = e.ReadTextFile(ctx, dst)
	if got != "界!" {
		t.Fatal("cancelled mutation ran")
	}
}

func TestPortsmithJudgeDurableEnvironmentShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX commands; native Windows coverage is candidate-owned")
	}
	e := local(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var chunks []byte
	result, err := e.Exec(ctx, "printf abc", denv.ExecOptions{Spill: &denv.SpillOptions{AfterBytes: 3, AfterLines: 100}, OnOutput: func(_ context.Context, b []byte) error { chunks = append(chunks, b...); return nil }})
	if err != nil || result.ExitCode != 0 || result.SpillPath != "" || string(chunks) != "abc" {
		t.Fatalf("threshold/nontruncated stream: %#v %v %q", result, err, chunks)
	}
	chunks = nil
	result, err = e.Exec(ctx, "printf abcdef; printf err >&2; exit 7", denv.ExecOptions{Spill: &denv.SpillOptions{AfterBytes: 3, AfterLines: 100}, OnOutput: func(_ context.Context, b []byte) error { chunks = append(chunks, b...); return nil }})
	if err != nil || result.ExitCode != 7 || result.SpillPath == "" {
		t.Fatalf("nonzero exit/spill: %#v %v", result, err)
	}
	b, err := os.ReadFile(result.SpillPath)
	if err != nil || string(b) != string(chunks) || !strings.Contains(string(b), "abcdef") || !strings.Contains(string(b), "err") {
		t.Fatalf("spill lost raw stream: %q %q %v", b, chunks, err)
	}
	stopCtx, stop := context.WithCancel(ctx)
	var cancellationOutput strings.Builder
	_, err = e.Exec(stopCtx, "printf READY; while :; do :; done", denv.ExecOptions{Spill: &denv.SpillOptions{AfterBytes: 1, AfterLines: 100}, OnOutput: func(_ context.Context, b []byte) error {
		cancellationOutput.Write(b)
		if strings.Contains(cancellationOutput.String(), "READY") {
			stop()
		}
		return nil
	}})
	var ee *denv.ExecutionError
	if !errors.As(err, &ee) || ee.Code != "aborted" {
		t.Fatalf("cancel must retain typed failure: %v", err)
	}
	// Source callbacks run before spill admission. Cancellation from this first
	// callback may precede spill creation; preserve an existing spill if present
	// without requiring a new file after the invocation has been cancelled.
	if ee.SpillPath != "" {
		if _, err := os.ReadFile(ee.SpillPath); err != nil {
			t.Fatalf("cancel discarded existing spill: %v", err)
		}
	}
	_, err = e.Exec(ctx, "printf output", denv.ExecOptions{OnOutput: func(context.Context, []byte) error { return errors.New("observer failed") }})
	if !errors.As(err, &ee) || ee.Code != "callback_error" {
		t.Fatalf("callback failure swallowed: %v", err)
	}
}
