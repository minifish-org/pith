// Self-tests for the write tool.
//
// The upstream implementation ships no dedicated write-tool test file; these
// tests translate the frozen tools-write acceptance scope (empty file,
// overwrite, multibyte content, missing directories, permission failure,
// serialized concurrent mutations and failure-without-false-success) into Go
// tests against the real LocalExecutionEnv and CreateWriteTool. The behavior
// contract is packages/agent/src/harness/tools/write.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	harnesscontext "github.com/minifish-org/pith/packages/agent/harness/context"
	env "github.com/minifish-org/pith/packages/agent/harness/env"
	harnesstypes "github.com/minifish-org/pith/packages/agent/harness/types"
	agenttypes "github.com/minifish-org/pith/packages/agent/types"
)

func noopWriteUpdate(agenttypes.AgentToolResult[any], *harnesstypes.AgentHarnessToolUpdateOptions) {
}

func writeTestEnvironment(t *testing.T) *env.LocalExecutionEnv {
	t.Helper()
	return env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: t.TempDir()})
}

func executeWriteForTest(t *testing.T, tool harnesstypes.AgentHarnessTool[ExecutionToolContext, WriteToolInput, any], environment harnesstypes.ExecutionEnv, ctx harnesscontext.Context, input WriteToolInput) (agenttypes.AgentToolResult[any], error) {
	t.Helper()
	return tool.Execute("write-test", input, noopWriteUpdate, ExecutionToolContext{Env: environment}, nil, ctx)
}

func writeTextOutput(result agenttypes.AgentToolResult[any]) string {
	var parts []string
	for _, block := range result.Content {
		if block.IsText() && block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func assertFileContent(t *testing.T, root string, name string, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", name, string(data), want)
	}
}

func TestCreateWriteToolWritesEmptyFile(t *testing.T) {
	environment := writeTestEnvironment(t)
	result, err := executeWriteForTest(t, CreateWriteTool(), environment, harnesscontext.BackgroundContext, WriteToolInput{Path: "nested/file.txt", Content: ""})
	if err != nil {
		t.Fatal(err)
	}
	if got := writeTextOutput(result); got != "Successfully wrote to nested/file.txt" {
		t.Fatalf("message = %q", got)
	}
	if result.Details != nil {
		t.Fatalf("write details should be absent, got %#v", result.Details)
	}
	assertFileContent(t, environment.Cwd(), "nested/file.txt", "")
}

func TestCreateWriteToolWritesMultibyteContent(t *testing.T) {
	environment := writeTestEnvironment(t)
	content := "a\n中\n"
	if _, err := executeWriteForTest(t, CreateWriteTool(), environment, harnesscontext.BackgroundContext, WriteToolInput{Path: "nested/file.txt", Content: content}); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, environment.Cwd(), "nested/file.txt", content)
}

func TestCreateWriteToolOverwritesExistingFile(t *testing.T) {
	environment := writeTestEnvironment(t)
	if write := environment.WriteFile("file.txt", []byte("old"), harnesscontext.BackgroundContext); !write.OK {
		t.Fatal(write.Error.Message)
	}
	if _, err := executeWriteForTest(t, CreateWriteTool(), environment, harnesscontext.BackgroundContext, WriteToolInput{Path: "file.txt", Content: "new"}); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, environment.Cwd(), "file.txt", "new")
}

func TestCreateWriteToolResolvesAtPrefix(t *testing.T) {
	environment := writeTestEnvironment(t)
	if _, err := executeWriteForTest(t, CreateWriteTool(), environment, harnesscontext.BackgroundContext, WriteToolInput{Path: "@file.txt", Content: "body"}); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, environment.Cwd(), "file.txt", "body")
}

func TestCreateWriteToolFailureDoesNotReportSuccess(t *testing.T) {
	environment := writeTestEnvironment(t)
	if write := environment.WriteFile("blocker", []byte("not a directory"), harnesscontext.BackgroundContext); !write.OK {
		t.Fatal(write.Error.Message)
	}
	result, err := executeWriteForTest(t, CreateWriteTool(), environment, harnesscontext.BackgroundContext, WriteToolInput{Path: "blocker/child.txt", Content: "ignored"})
	if err == nil {
		t.Fatal("expected write failure")
	}
	if writeTextOutput(result) != "" {
		t.Fatalf("failed write must not report success: %q", writeTextOutput(result))
	}
	if _, statErr := os.Stat(filepath.Join(environment.Cwd(), "blocker", "child.txt")); statErr == nil {
		t.Fatal("failed write created the target file")
	}
}

func TestCreateWriteToolPermissionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	environment := writeTestEnvironment(t)
	locked := filepath.Join(environment.Cwd(), "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	result, err := executeWriteForTest(t, CreateWriteTool(), environment, harnesscontext.BackgroundContext, WriteToolInput{Path: "locked/file.txt", Content: "denied"})
	if err == nil {
		// A privileged user can bypass the directory bits; the generic failure
		// path is still covered by TestCreateWriteToolFailureDoesNotReportSuccess.
		t.Skip("filesystem did not enforce directory permissions")
	}
	if writeTextOutput(result) != "" {
		t.Fatalf("failed write must not report success: %q", writeTextOutput(result))
	}
}

func TestCreateWriteToolCancellation(t *testing.T) {
	environment := writeTestEnvironment(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := executeWriteForTest(t, CreateWriteTool(), environment, ctx, WriteToolInput{Path: "file.txt", Content: "aborted"})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if writeTextOutput(result) != "" {
		t.Fatalf("cancelled write must not report success: %q", writeTextOutput(result))
	}
}

func TestCreateWriteToolPrepareArguments(t *testing.T) {
	tool := CreateWriteTool()
	params, err := tool.PrepareArguments([]byte(`{"path":"a.txt","content":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if params.Path != "a.txt" || params.Content != "hello" {
		t.Fatalf("params = %#v", params)
	}
}

// recordingEnv wraps the real LocalExecutionEnv and records how many writes run
// concurrently. It proves the write tool routes mutations through the shared
// file mutation queue instead of writing in parallel.
type recordingEnv struct {
	*env.LocalExecutionEnv

	mu        sync.Mutex
	active    int
	maxActive int
}

func (r *recordingEnv) WriteFile(path string, content []byte, ctx harnesscontext.Context) harnesstypes.Result[struct{}, harnesstypes.FileError] {
	r.mu.Lock()
	r.active++
	if r.active > r.maxActive {
		r.maxActive = r.active
	}
	r.mu.Unlock()

	time.Sleep(2 * time.Millisecond)
	result := r.LocalExecutionEnv.WriteFile(path, content, ctx)

	r.mu.Lock()
	r.active--
	r.mu.Unlock()
	return result
}

func TestCreateWriteToolSerializesConcurrentWrites(t *testing.T) {
	base := writeTestEnvironment(t)
	environment := &recordingEnv{LocalExecutionEnv: base}
	// Pre-create the file so every mutation resolves to the same canonical key.
	if write := base.WriteFile("shared.txt", []byte("seed"), harnesscontext.BackgroundContext); !write.OK {
		t.Fatal(write.Error.Message)
	}

	tool := CreateWriteTool()
	const writers = 8
	var waitGroup sync.WaitGroup
	errs := make(chan error, writers)
	for index := 0; index < writers; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			content := "writer-" + itoa(index)
			_, err := executeWriteForTest(t, tool, environment, harnesscontext.BackgroundContext, WriteToolInput{Path: "shared.txt", Content: content})
			errs <- err
		}(index)
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write failed: %v", err)
		}
	}

	environment.mu.Lock()
	maxActive := environment.maxActive
	environment.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("write mutations overlapped: maxActive = %d", maxActive)
	}

	// The file must contain exactly one complete writer payload, never a mix.
	data, err := os.ReadFile(filepath.Join(base.Cwd(), "shared.txt"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "writer-") {
		t.Fatalf("unexpected final content %q", content)
	}
}
