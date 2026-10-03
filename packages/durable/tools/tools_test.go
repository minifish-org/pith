package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/minifish-org/pith/packages/durable/env"
	"github.com/minifish-org/pith/packages/durable/harness"
)

// fakeAPI embeds the real harness.ToolAPI surface and overrides only the
// environment/reporting capabilities the builtin tools use. This mirrors the
// independent judge's fixture and keeps the tools limited to api.Env,
// api.Output, api.Diagnostic and api.Details.
type fakeAPI struct {
	harness.ToolAPI
	environment env.ExecutionEnv
	output      []byte
	diagnostics []harness.ToolDiagnostic
	details     json.RawMessage
}

func (a *fakeAPI) Env() env.ExecutionEnv { return a.environment }
func (a *fakeAPI) Output(b []byte) error { a.output = append(a.output, b...); return nil }
func (a *fakeAPI) Diagnostic(d harness.ToolDiagnostic) error {
	a.diagnostics = append(a.diagnostics, d)
	return nil
}
func (a *fakeAPI) Details(_ context.Context, b json.RawMessage) error {
	a.details = append(json.RawMessage(nil), b...)
	return nil
}

func newEnv(t *testing.T) *env.Local {
	t.Helper()
	e, err := env.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Cleanup(context.Background()) })
	return e
}

func newAPI(t *testing.T) *fakeAPI {
	t.Helper()
	return &fakeAPI{environment: newEnv(t)}
}

func mustArgs(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func resultText(r harness.ToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Text != nil {
			b.WriteString(c.Text.Text)
		}
	}
	return b.String()
}

func diagnosticText(r harness.ToolResult) string {
	parts := make([]string, 0, len(r.Diagnostics))
	for _, d := range r.Diagnostics {
		parts = append(parts, d.Message)
	}
	return strings.Join(parts, "\n")
}

func runTool(t *testing.T, tool harness.ToolRegistration, args json.RawMessage, api *fakeAPI) (harness.ToolResult, error) {
	t.Helper()
	return tool.Execute(context.Background(), args, api)
}

func mustRun(t *testing.T, tool harness.ToolRegistration, args json.RawMessage, api *fakeAPI) harness.ToolResult {
	t.Helper()
	r, err := tool.Execute(context.Background(), args, api)
	if err != nil {
		t.Fatalf("tool error: %v", err)
	}
	if r.IsError {
		t.Fatalf("tool returned error result: %s", diagnosticText(r))
	}
	return r
}

func TestCodingToolsExtension(t *testing.T) {
	ext := CodingTools()
	if ext.Name != "coding-tools" || len(ext.Tools) != 4 {
		t.Fatalf("extension: %#v", ext)
	}
	order := []string{"read", "write", "edit", "bash"}
	for i, reg := range ext.Tools {
		if reg.Declaration.Name != order[i] {
			t.Fatalf("tool %d = %s, want %s", i, reg.Declaration.Name, order[i])
		}
		if reg.Replay == "safe" {
			t.Fatalf("invented replay guarantee for %s", reg.Declaration.Name)
		}
	}
}

func TestNoEnvironment(t *testing.T) {
	_, err := CreateReadTool().Execute(context.Background(), mustArgs(t, map[string]any{"path": "x"}), &fakeAPI{})
	if err == nil || !strings.Contains(err.Error(), "No execution environment") {
		t.Fatalf("want no-environment error, got %v", err)
	}
}

func TestDetectImageSignatures(t *testing.T) {
	for _, signature := range []string{"GIF87a", "GIF89a"} {
		mime := DetectSupportedImageMimeType([]byte(signature))
		if mime == nil || *mime != "image/gif" {
			t.Fatalf("signature %q: %v", signature, mime)
		}
	}
}

func TestReadOffsetsLimitsAndContinuation(t *testing.T) {
	api := newAPI(t)
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = "Line " + itoa(i+1)
	}
	if err := api.environment.WriteFile(context.Background(), "test.txt", []byte(strings.Join(lines, "\n"))); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, CreateReadTool(), mustArgs(t, map[string]any{"path": "test.txt", "offset": 41, "limit": 20}), api)
	out := resultText(r)
	if strings.Contains(out, "Line 40") || !strings.Contains(out, "Line 41") || !strings.Contains(out, "Line 60") || strings.Contains(out, "Line 61") {
		t.Fatalf("selected window wrong: %q", out)
	}
	if diagnosticText(r) != "40 more lines in file. Use offset=61 to continue." {
		t.Fatalf("continuation diagnostic: %q", diagnosticText(r))
	}
}

func TestReadTruncatesByLineCount(t *testing.T) {
	api := newAPI(t)
	lines := make([]string, 2500)
	for i := range lines {
		lines[i] = "Line " + itoa(i+1)
	}
	if err := api.environment.WriteFile(context.Background(), "large.txt", []byte(strings.Join(lines, "\n"))); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, CreateReadTool(), mustArgs(t, map[string]string{"path": "large.txt"}), api)
	if diagnosticText(r) != "Showing lines 1-2000 of 2500. Use offset=2001 to continue." {
		t.Fatalf("diagnostic: %q", diagnosticText(r))
	}
	if len(r.Diagnostics) == 0 || r.Diagnostics[0].Code != "truncated" {
		t.Fatalf("code: %#v", r.Diagnostics)
	}
	var details struct {
		Truncation struct {
			Truncated   bool   `json:"truncated"`
			TruncatedBy string `json:"truncatedBy"`
			TotalLines  int    `json:"totalLines"`
			OutputLines int    `json:"outputLines"`
		} `json:"truncation"`
	}
	if err := json.Unmarshal(r.Details, &details); err != nil {
		t.Fatal(err)
	}
	if !details.Truncation.Truncated || details.Truncation.TruncatedBy != "lines" || details.Truncation.TotalLines != 2500 || details.Truncation.OutputLines != 2000 {
		t.Fatalf("truncation: %#v", details.Truncation)
	}
}

func TestReadTrailingNewlineAtLimit(t *testing.T) {
	api := newAPI(t)
	lines := make([]string, 2000)
	for i := range lines {
		lines[i] = "x"
	}
	if err := api.environment.WriteFile(context.Background(), "exact.txt", []byte(strings.Join(lines, "\n")+"\n")); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, CreateReadTool(), mustArgs(t, map[string]string{"path": "exact.txt"}), api)
	if r.Details != nil {
		t.Fatalf("unexpected details: %s", r.Details)
	}
	if len(r.Diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", r.Diagnostics)
	}
}

func TestReadLongFirstLine(t *testing.T) {
	api := newAPI(t)
	content := strings.Repeat("é", 40000) + "\nnext\n"
	if err := api.environment.WriteFile(context.Background(), "long.txt", []byte(content)); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, CreateReadTool(), mustArgs(t, map[string]string{"path": "long.txt"}), api)
	if resultText(r) != strings.Repeat("é", 25600) {
		t.Fatalf("long line head length = %d", len(resultText(r)))
	}
	want := "Line 1 is 78.1KB, exceeds the 50.0KB limit; showing its first 50.0KB. Use bash: sed -n '1p' long.txt | tail -c +51201"
	if diagnosticText(r) != want {
		t.Fatalf("diagnostic = %q", diagnosticText(r))
	}
	if strings.Contains(string(r.Details), "content") {
		t.Fatalf("details leaked content: %s", r.Details)
	}
	var details struct {
		Truncation struct {
			Truncated             bool `json:"truncated"`
			FirstLineExceedsLimit bool `json:"firstLineExceedsLimit"`
			OutputBytes           int  `json:"outputBytes"`
			OutputLines           int  `json:"outputLines"`
		} `json:"truncation"`
	}
	if err := json.Unmarshal(r.Details, &details); err != nil {
		t.Fatal(err)
	}
	if !details.Truncation.Truncated || !details.Truncation.FirstLineExceedsLimit || details.Truncation.OutputBytes != 51200 || details.Truncation.OutputLines != 1 {
		t.Fatalf("truncation: %#v", details.Truncation)
	}
}

func TestReadOffsetBeyondFile(t *testing.T) {
	api := newAPI(t)
	if err := api.environment.WriteFile(context.Background(), "short.txt", []byte("one\ntwo\nthree")); err != nil {
		t.Fatal(err)
	}
	_, err := CreateReadTool().Execute(context.Background(), mustArgs(t, map[string]any{"path": "short.txt", "offset": 100}), api)
	if err == nil || err.Error() != "Offset 100 is beyond end of file (3 lines total)" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadReportsImagesUnsupported(t *testing.T) {
	api := newAPI(t)
	png := mustBase64(t, "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGNgYGD4DwABBAEAX+XDSwAAAABJRU5ErkJggg==")
	if err := api.environment.WriteFile(context.Background(), "image.txt", png); err != nil {
		t.Fatal(err)
	}
	r, err := CreateReadTool().Execute(context.Background(), mustArgs(t, map[string]string{"path": "image.txt"}), api)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsError || len(r.Content) != 0 {
		t.Fatalf("image result: %#v", r)
	}
	if diagnosticText(r) != "image.txt is an image (image/png); reading images is not supported" {
		t.Fatalf("diagnostic = %q", diagnosticText(r))
	}
}

func TestWriteCreatesParentDirectories(t *testing.T) {
	api := newAPI(t)
	r := mustRun(t, CreateWriteTool(), mustArgs(t, map[string]string{"path": "nested/dir/file.txt", "content": "hello"}), api)
	if resultText(r) != "Successfully wrote to nested/dir/file.txt" {
		t.Fatalf("text = %q", resultText(r))
	}
	got, err := api.environment.ReadTextFile(context.Background(), "nested/dir/file.txt")
	if err != nil || got != "hello" {
		t.Fatalf("read back: %q %v", got, err)
	}
}

func TestWritePreCancelledDoesNotMutate(t *testing.T) {
	api := newAPI(t)
	if err := api.environment.WriteFile(context.Background(), "file.txt", []byte("original")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CreateWriteTool().Execute(ctx, mustArgs(t, map[string]string{"path": "file.txt", "content": "broken"}), api)
	if err == nil {
		t.Fatal("pre-cancelled write succeeded")
	}
	got, _ := api.environment.ReadTextFile(context.Background(), "file.txt")
	if got != "original" {
		t.Fatalf("mutated file: %q", got)
	}
}

func TestEditAppliesDisjointEdits(t *testing.T) {
	api := newAPI(t)
	original := "alpha\nbeta\ngamma\ndelta\n"
	if err := api.environment.WriteFile(context.Background(), "edit.txt", []byte(original)); err != nil {
		t.Fatal(err)
	}
	input := mustArgs(t, map[string]any{
		"path": "edit.txt",
		"edits": []any{
			map[string]string{"oldText": "alpha\n", "newText": "ALPHA\n"},
			map[string]string{"oldText": "gamma\n", "newText": "GAMMA\n"},
		},
	})
	r := mustRun(t, CreateEditTool(), input, api)
	if resultText(r) != "Successfully replaced 2 block(s) in edit.txt." {
		t.Fatalf("text = %q", resultText(r))
	}
	var details EditToolDetails
	if err := json.Unmarshal(r.Details, &details); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(details.Diff, "ALPHA") || !strings.Contains(details.Diff, "GAMMA") {
		t.Fatalf("diff = %q", details.Diff)
	}
	if applied := applyUnifiedPatch(t, original, details.Patch); applied != "ALPHA\nbeta\nGAMMA\ndelta\n" {
		t.Fatalf("patch applied = %q\npatch:\n%s", applied, details.Patch)
	}
	got, _ := api.environment.ReadTextFile(context.Background(), "edit.txt")
	if got != "ALPHA\nbeta\nGAMMA\ndelta\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEditRepairsInputShapes(t *testing.T) {
	edit := map[string]any{"oldText": "a", "newText": "b"}

	asString := mustArgs(t, map[string]any{"path": "f", "edits": `[{"oldText":"a","newText":"b"}]`})
	prepared, err := prepareEditArguments(asString)
	if err != nil {
		t.Fatal(err)
	}
	assertArgsEqual(t, prepared, map[string]any{"path": "f", "edits": []any{edit}})

	singleString := mustArgs(t, map[string]any{"path": "f", "edits": `{"oldText":"a","newText":"b"}`})
	prepared, _ = prepareEditArguments(singleString)
	assertArgsEqual(t, prepared, map[string]any{"path": "f", "edits": []any{edit}})

	singleObject := mustArgs(t, map[string]any{"path": "f", "edits": edit})
	prepared, _ = prepareEditArguments(singleObject)
	assertArgsEqual(t, prepared, map[string]any{"path": "f", "edits": []any{edit}})

	legacy := mustArgs(t, map[string]any{"path": "f", "edits": []any{edit}, "oldText": "c", "newText": "d"})
	prepared, _ = prepareEditArguments(legacy)
	assertArgsEqual(t, prepared, map[string]any{"path": "f", "edits": []any{edit, map[string]any{"oldText": "c", "newText": "d"}}})

	notJSON := mustArgs(t, map[string]any{"path": "f", "edits": "not json"})
	prepared, _ = prepareEditArguments(notJSON)
	assertArgsEqual(t, prepared, map[string]any{"path": "f", "edits": "not json"})
}

func TestEditRejectsOverlapWithoutPartialWrite(t *testing.T) {
	api := newAPI(t)
	if err := api.environment.WriteFile(context.Background(), "edit.txt", []byte("one\ntwo\nthree\n")); err != nil {
		t.Fatal(err)
	}
	input := mustArgs(t, map[string]any{
		"path": "edit.txt",
		"edits": []any{
			map[string]string{"oldText": "one\ntwo\n", "newText": "ONE\nTWO\n"},
			map[string]string{"oldText": "two\nthree\n", "newText": "TWO\nTHREE\n"},
		},
	})
	_, err := CreateEditTool().Execute(context.Background(), input, api)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("err = %v", err)
	}
	got, _ := api.environment.ReadTextFile(context.Background(), "edit.txt")
	if got != "one\ntwo\nthree\n" {
		t.Fatalf("partial write: %q", got)
	}
}

func TestEditRejectsMissingAndDuplicateText(t *testing.T) {
	api := newAPI(t)
	if err := api.environment.WriteFile(context.Background(), "edit.txt", []byte("foo foo foo")); err != nil {
		t.Fatal(err)
	}
	tool := CreateEditTool()
	_, err := tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": "edit.txt", "edits": []any{map[string]string{"oldText": "bar", "newText": "baz"}}}), api)
	if err == nil || !strings.Contains(err.Error(), "Could not find the exact text") {
		t.Fatalf("missing err = %v", err)
	}
	_, err = tool.Execute(context.Background(), mustArgs(t, map[string]any{"path": "edit.txt", "edits": []any{map[string]string{"oldText": "foo", "newText": "bar"}}}), api)
	if err == nil || !strings.Contains(err.Error(), "Found 3 occurrences") {
		t.Fatalf("duplicate err = %v", err)
	}
}

func TestEditPreservesBOMAndCRLF(t *testing.T) {
	api := newAPI(t)
	original := "\ufeffone\r\ntwo\r\n"
	if err := api.environment.WriteFile(context.Background(), "edit.txt", []byte(original)); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareEditArguments(mustArgs(t, map[string]any{"path": "edit.txt", "edits": map[string]string{"oldText": "two", "newText": "TWO"}}))
	if err != nil {
		t.Fatal(err)
	}
	mustRun(t, CreateEditTool(), prepared, api)
	got, _ := api.environment.ReadTextFile(context.Background(), "edit.txt")
	if got != "\ufeffone\r\nTWO\r\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestEditThroughSymlink(t *testing.T) {
	api := newAPI(t)
	if err := api.environment.WriteFile(context.Background(), "target.txt", []byte("before\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(api.environment.CWD(), "link.txt")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	mustRun(t, CreateEditTool(), mustArgs(t, map[string]any{"path": "link.txt", "edits": []any{map[string]string{"oldText": "before", "newText": "after"}}}), api)
	got, _ := api.environment.ReadTextFile(context.Background(), "target.txt")
	if got != "after\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestBashStreamsCombinedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command")
	}
	api := newAPI(t)
	_, err := CreateBashTool().Execute(context.Background(), mustArgs(t, map[string]string{"command": "printf out; printf err >&2"}), api)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(api.output), "out") || !strings.Contains(string(api.output), "err") {
		t.Fatalf("output = %q", api.output)
	}
}

func TestBashNonzeroAndTimeouts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command")
	}
	api := newAPI(t)
	tool := CreateBashTool()
	_, err := tool.Execute(context.Background(), mustArgs(t, map[string]string{"command": "printf failed; exit 7"}), api)
	if err == nil || err.Error() != "Command exited with code 7" {
		t.Fatalf("nonzero err = %v", err)
	}
	if string(api.output) != "failed" {
		t.Fatalf("streamed = %q", api.output)
	}
	api2 := newAPI(t)
	_, err = tool.Execute(context.Background(), mustArgs(t, map[string]any{"command": "sleep 2", "timeout": 0.01}), api2)
	if err == nil || err.Error() != "Command timed out after 0.01 seconds" {
		t.Fatalf("timeout err = %v", err)
	}
}

func TestBashInvalidTimeouts(t *testing.T) {
	api := newAPI(t)
	for _, timeout := range []float64{0, -1, 2147484} {
		_, err := CreateBashTool().Execute(context.Background(), mustArgs(t, map[string]any{"command": "exit 0", "timeout": timeout}), api)
		if err == nil {
			t.Fatalf("timeout %v accepted", timeout)
		}
	}
}

func TestBashPrefixAndPrepare(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command")
	}
	api := newAPI(t)
	prepared := false
	bash := CreateBashTool(BashToolOptions{
		CommandPrefix: "export DURABLE_TEST_PREFIX=present",
		Prepare: func(_ context.Context, e *BashExecution, _ harness.ToolAPI) error {
			prepared = true
			e.Env["DURABLE_TEST_LOCAL"] = "native"
			return nil
		},
	})
	_, err := bash.Execute(context.Background(), mustArgs(t, map[string]string{"command": `printf '%s:%s' "$DURABLE_TEST_PREFIX" "$DURABLE_TEST_LOCAL"`}), api)
	if err != nil {
		t.Fatal(err)
	}
	if !prepared || string(api.output) != "present:native" {
		t.Fatalf("prepared=%v output=%q", prepared, api.output)
	}
}

func TestBashSpillWithinAndBeyondLimits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command")
	}
	api := newAPI(t)
	_, err := CreateBashTool().Execute(context.Background(), mustArgs(t, map[string]string{"command": "printf small"}), api)
	if err != nil {
		t.Fatal(err)
	}
	if len(api.diagnostics) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", api.diagnostics)
	}

	api2 := newAPI(t)
	_, err = CreateBashTool().Execute(context.Background(), mustArgs(t, map[string]string{"command": "i=1; while [ $i -le 3000 ]; do echo line-$i; i=$((i + 1)); done"}), api2)
	if err != nil {
		t.Fatal(err)
	}
	expected := ""
	for i := 1; i <= 3000; i++ {
		expected += "line-" + itoa(i) + "\n"
	}
	if string(api2.output) != expected {
		t.Fatalf("streamed bytes mismatch: %d", len(api2.output))
	}
	if len(api2.diagnostics) == 0 || api2.diagnostics[0].Code != "full_output" {
		t.Fatalf("diagnostics: %#v", api2.diagnostics)
	}
	path := strings.TrimPrefix(api2.diagnostics[0].Message, "Full output: ")
	full, err := api2.environment.ReadTextFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if full != expected {
		t.Fatalf("spill content mismatch")
	}
}

// timeoutSpillEnv simulates a command that streams output and then times out
// after spilling the complete stream.
type timeoutSpillEnv struct {
	*env.Local
}

func (e *timeoutSpillEnv) Exec(ctx context.Context, _ string, options env.ExecOptions) (env.ExecResult, error) {
	lines := make([]string, DefaultMaxLines+1)
	for i := range lines {
		lines[i] = "line-" + itoa(i+1)
	}
	output := strings.Join(lines, "\n") + "\n"
	spillPath, err := e.Local.CreateTempFile(ctx, env.TempFileOptions{Prefix: "timeout-", Suffix: ".log"})
	if err != nil {
		return env.ExecResult{}, err
	}
	if err := e.Local.WriteFile(ctx, spillPath, []byte(output)); err != nil {
		return env.ExecResult{}, err
	}
	if options.OnOutput != nil {
		if err := options.OnOutput(ctx, []byte(output)); err != nil {
			return env.ExecResult{}, err
		}
	}
	return env.ExecResult{SpillPath: spillPath}, &env.ExecutionError{Code: env.ExecutionErrorTimeout, SpillPath: spillPath}
}

func TestBashReportsSpillOnTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command")
	}
	local := newEnv(t)
	api := &fakeAPI{environment: &timeoutSpillEnv{Local: local}}
	_, err := CreateBashTool().Execute(context.Background(), mustArgs(t, map[string]any{"command": "emit-output-then-time-out", "timeout": 0.05}), api)
	if err == nil || err.Error() != "Command timed out after 0.05 seconds" {
		t.Fatalf("err = %v", err)
	}
	if len(api.diagnostics) == 0 {
		t.Fatal("no full_output diagnostic")
	}
	path := strings.TrimPrefix(api.diagnostics[0].Message, "Full output: ")
	full, err := api.environment.ReadTextFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(full, "line-1\nline-2") || !strings.Contains(full, "line-2000\nline-2001") {
		t.Fatalf("spill content wrong")
	}
}

func TestBashPreparesCwdAndEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command")
	}
	local := newEnv(t)
	api := &fakeAPI{environment: local}
	if err := local.CreateDir(context.Background(), "workspace", true); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(local.CWD(), "workspace")
	bash := CreateBashTool(BashToolOptions{Prepare: func(_ context.Context, e *BashExecution, _ harness.ToolAPI) error {
		e.CWD = workspace
		e.Env = map[string]string{"PI_BASH_PREPARE_EXPLICIT": "explicit"}
		e.InheritEnv = false
		e.Command += "\nprintf '%s' \"$PI_BASH_PREPARE_EXPLICIT\""
		return nil
	}})
	if _, err := bash.Execute(context.Background(), mustArgs(t, map[string]string{"command": ":"}), api); err != nil {
		t.Fatal(err)
	}
	if string(api.output) != "explicit" {
		t.Fatalf("output = %q", api.output)
	}
}

func TestReadUTF8Boundary(t *testing.T) {
	api := newAPI(t)
	content := strings.Repeat("界", 30000)
	if err := api.environment.WriteFile(context.Background(), "unicode.txt", []byte(content)); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, CreateReadTool(), mustArgs(t, map[string]string{"path": "unicode.txt"}), api)
	out := resultText(r)
	if !utf8.ValidString(out) || len(out) > DefaultMaxBytes {
		t.Fatalf("utf8 bound: bytes=%d", len(out))
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func mustBase64(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertArgsEqual(t *testing.T, raw json.RawMessage, want map[string]any) {
	t.Helper()
	var got any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	wantRaw, _ := json.Marshal(want)
	var wantValue any
	_ = json.Unmarshal(wantRaw, &wantValue)
	gotRaw, _ := json.Marshal(got)
	wantOut, _ := json.Marshal(wantValue)
	if string(gotRaw) != string(wantOut) {
		t.Fatalf("args = %s, want %s", gotRaw, wantOut)
	}
}

// applyUnifiedPatch applies the unified patch produced by GenerateUnifiedPatch
// to original content. It is a minimal applier used only to verify the patch
// round-trips to the same bytes the tool wrote.
func applyUnifiedPatch(t *testing.T, original string, patch string) string {
	t.Helper()
	orig := strings.Split(original, "\n")
	patchLines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	var out []string
	oldIdx := 0
	i := 0
	for i < len(patchLines) && !strings.HasPrefix(patchLines[i], "@@") {
		i++
	}
	for i < len(patchLines) {
		line := patchLines[i]
		if !strings.HasPrefix(line, "@@") {
			i++
			continue
		}
		var oldStart, oldLines, newStart, newLines int
		if _, err := fmt.Sscanf(line, "@@ -%d,%d +%d,%d @@", &oldStart, &oldLines, &newStart, &newLines); err != nil {
			t.Fatalf("bad hunk header %q: %v", line, err)
		}
		i++
		for oldIdx < oldStart-1 && oldIdx < len(orig) {
			out = append(out, orig[oldIdx])
			oldIdx++
		}
		for i < len(patchLines) && !strings.HasPrefix(patchLines[i], "@@") {
			body := patchLines[i]
			i++
			if body == "\\ No newline at end of file" || body == "" {
				continue
			}
			switch body[0] {
			case ' ':
				out = append(out, body[1:])
				oldIdx++
			case '-':
				oldIdx++
			case '+':
				out = append(out, body[1:])
			}
		}
	}
	for oldIdx < len(orig) {
		out = append(out, orig[oldIdx])
		oldIdx++
	}
	return strings.Join(out, "\n")
}
