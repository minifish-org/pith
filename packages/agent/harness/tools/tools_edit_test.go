// Self-tests for the edit tool and its shared diff utilities.
//
// The pinned upstream test suite in packages/agent/test/harness/tools.test.ts
// exercises the edit tool through an in-memory NodeExecutionEnv. The edit
// batch's reference ledger contains no separately materialized upstream files,
// so these Go self-tests translate the frozen acceptance scope (unique/missing/
// duplicate matches, overlap rejection, Unicode/BOM/CRLF preservation, fuzzy
// matching, symlink editing and the file mutation queue) into deterministic
// tests against the real LocalExecutionEnv. The behavior contract is
// packages/agent/src/harness/tools/edit.ts and edit-diff.ts at Pi revision
// f07218c4d4bbc12bef056a7058c3dd49dfe41abe.
package tools

import (
	"context"
	"encoding/json"
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

type editTool = harnesstypes.AgentHarnessTool[ExecutionToolContext, EditToolInput, *EditToolDetails]

func editNoopUpdate(agenttypes.AgentToolResult[*EditToolDetails], *harnesstypes.AgentHarnessToolUpdateOptions) {
}

func editTestEnvironment(t *testing.T) *env.LocalExecutionEnv {
	t.Helper()
	return env.NewLocalExecutionEnv(env.LocalExecutionEnvOptions{Cwd: t.TempDir()})
}

func executeEditForTest(t *testing.T, tool editTool, environment harnesstypes.ExecutionEnv, ctx harnesscontext.Context, input EditToolInput) (agenttypes.AgentToolResult[*EditToolDetails], error) {
	t.Helper()
	return tool.Execute("edit-test", input, editNoopUpdate, ExecutionToolContext{Env: environment}, nil, ctx)
}

func editTextOutput(result agenttypes.AgentToolResult[*EditToolDetails]) string {
	var parts []string
	for _, block := range result.Content {
		if block.IsText() && block.Text != nil {
			parts = append(parts, block.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func writeEditFile(t *testing.T, environment *env.LocalExecutionEnv, name string, content string) {
	t.Helper()
	if result := environment.WriteFile(name, []byte(content), harnesscontext.BackgroundContext); !result.OK {
		t.Fatalf("write %s: %v", name, result.Error.Message)
	}
}

func readEditFile(t *testing.T, environment *env.LocalExecutionEnv, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(environment.Cwd(), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func firstChangedLineOf(t *testing.T, result agenttypes.AgentToolResult[*EditToolDetails]) int {
	t.Helper()
	if result.Details == nil || result.Details.FirstChangedLine == nil {
		t.Fatalf("missing firstChangedLine: %#v", result.Details)
	}
	return *result.Details.FirstChangedLine
}

func TestCreateEditToolExactReplacementFixture(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "one\ntwo\n")

	result, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "two", NewText: "TWO"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := editTextOutput(result); got != "Successfully replaced 1 block(s) in file.txt." {
		t.Fatalf("message = %q", got)
	}
	if result.Details == nil {
		t.Fatal("missing details")
	}
	if got := result.Details.Diff; got != " 1 one\n-2 two\n+2 TWO" {
		t.Fatalf("diff = %q", got)
	}
	if got := result.Details.Patch; got != "--- file.txt\n+++ file.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n" {
		t.Fatalf("patch = %q", got)
	}
	if got := firstChangedLineOf(t, result); got != 2 {
		t.Fatalf("firstChangedLine = %d", got)
	}
	if got := readEditFile(t, environment, "file.txt"); got != "one\nTWO\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestCreateEditToolPreservesCRLF(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "a\r\nb\r\n")

	result, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "b", NewText: "B"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Details.Diff; got != " 1 a\n-2 b\n+2 B" {
		t.Fatalf("diff = %q", got)
	}
	if got := result.Details.Patch; got != "--- file.txt\n+++ file.txt\n@@ -1,2 +1,2 @@\n a\n-b\n+B\n" {
		t.Fatalf("patch = %q", got)
	}
	if got := firstChangedLineOf(t, result); got != 2 {
		t.Fatalf("firstChangedLine = %d", got)
	}
	if got := readEditFile(t, environment, "file.txt"); got != "a\r\nB\r\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestCreateEditToolPreservesBOM(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "\uFEFFa\nb\n")

	result, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "a", NewText: "A"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Details.Diff; got != "-1 a\n+1 A\n 2 b" {
		t.Fatalf("diff = %q", got)
	}
	if got := result.Details.Patch; got != "--- file.txt\n+++ file.txt\n@@ -1,2 +1,2 @@\n-a\n+A\n b\n" {
		t.Fatalf("patch = %q", got)
	}
	if got := firstChangedLineOf(t, result); got != 1 {
		t.Fatalf("firstChangedLine = %d", got)
	}
	if got := readEditFile(t, environment, "file.txt"); got != "\uFEFFA\nb\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestCreateEditToolPreservesBOMAndCRLF(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "\uFEFFone\r\ntwo\r\n")

	if _, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "two", NewText: "TWO"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := readEditFile(t, environment, "file.txt"); got != "\uFEFFone\r\nTWO\r\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestCreateEditToolUnicodeReplacement(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "你好世界")

	result, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "世界", NewText: "Pi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Details.Diff; got != "-1 你好世界\n+1 你好Pi" {
		t.Fatalf("diff = %q", got)
	}
	if got := result.Details.Patch; got != "--- file.txt\n+++ file.txt\n@@ -1,1 +1,1 @@\n-你好世界\n\\ No newline at end of file\n+你好Pi\n\\ No newline at end of file\n" {
		t.Fatalf("patch = %q", got)
	}
	if got := firstChangedLineOf(t, result); got != 1 {
		t.Fatalf("firstChangedLine = %d", got)
	}
	if got := readEditFile(t, environment, "file.txt"); got != "你好Pi" {
		t.Fatalf("content = %q", got)
	}
}

func TestCreateEditToolRejectsDuplicateTarget(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "x x")

	_, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "x", NewText: "z"}},
	})
	if err == nil {
		t.Fatal("expected duplicate-match error")
	}
	if !strings.Contains(err.Error(), "Found 2 occurrences of the text in file.txt") {
		t.Fatalf("error = %v", err)
	}
	if got := readEditFile(t, environment, "file.txt"); got != "x x" {
		t.Fatalf("content changed on failure: %q", got)
	}
}

func TestCreateEditToolRejectsMissingTarget(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "a")

	_, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "b", NewText: "c"}},
	})
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "Could not find the exact text in file.txt") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateEditToolRejectsOverlappingEdits(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "one\ntwo\nthree\n")

	_, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path: "file.txt",
		Edits: []Edit{
			{OldText: "one\ntwo\n", NewText: "ONE\nTWO\n"},
			{OldText: "two\nthree\n", NewText: "TWO\nTHREE\n"},
		},
	})
	if err == nil {
		t.Fatal("expected overlap error")
	}
	if !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("error = %v", err)
	}
	if got := readEditFile(t, environment, "file.txt"); got != "one\ntwo\nthree\n" {
		t.Fatalf("content changed on overlap failure: %q", got)
	}
}

func TestCreateEditToolAppliesDisjointEdits(t *testing.T) {
	environment := editTestEnvironment(t)
	original := "alpha\nbeta\ngamma\ndelta\n"
	writeEditFile(t, environment, "edit.txt", original)

	result, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path: "edit.txt",
		Edits: []Edit{
			{OldText: "alpha\n", NewText: "ALPHA\n"},
			{OldText: "gamma\n", NewText: "GAMMA\n"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := editTextOutput(result); got != "Successfully replaced 2 block(s) in edit.txt." {
		t.Fatalf("message = %q", got)
	}
	if !strings.Contains(result.Details.Diff, "ALPHA") || !strings.Contains(result.Details.Diff, "GAMMA") {
		t.Fatalf("diff = %q", result.Details.Diff)
	}
	if got := readEditFile(t, environment, "edit.txt"); got != "ALPHA\nbeta\nGAMMA\ndelta\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestCreateEditToolRejectsNoChange(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "abc")

	_, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "abc", NewText: "abc"}},
	})
	if err == nil {
		t.Fatal("expected no-change error")
	}
	if !strings.Contains(err.Error(), "No changes made to file.txt") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateEditToolRejectsEmptyOldText(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "abc")

	_, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "", NewText: "x"}},
	})
	if err == nil {
		t.Fatal("expected empty-oldText error")
	}
	if !strings.Contains(err.Error(), "oldText must not be empty in file.txt.") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateEditToolFuzzyMatchPreservesUnchangedLines(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "alpha\u2019s  \nbeta  \n")

	result, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "alpha's", NewText: "ALPHA"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The touched line is rewritten from the normalized base (smart quote and
	// trailing whitespace gone) while the untouched line keeps its whitespace.
	if got := readEditFile(t, environment, "file.txt"); got != "ALPHA\nbeta  \n" {
		t.Fatalf("content = %q", got)
	}
	if got := firstChangedLineOf(t, result); got != 1 {
		t.Fatalf("firstChangedLine = %d", got)
	}
}

func TestCreateEditToolFuzzyMatchSmartQuotes(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "it\u2019s here\n")

	if _, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "it's", NewText: "it is"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := readEditFile(t, environment, "file.txt"); got != "it is here\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestPrepareEditArgumentsLegacyForm(t *testing.T) {
	tool := CreateEditTool()
	params, err := tool.PrepareArguments(json.RawMessage(`{"path":"a.txt","oldText":"one","newText":"two"}`))
	if err != nil {
		t.Fatal(err)
	}
	if params.Path != "a.txt" || len(params.Edits) != 1 || params.Edits[0].OldText != "one" || params.Edits[0].NewText != "two" {
		t.Fatalf("params = %#v", params)
	}
}

func TestPrepareEditArgumentsJSONStringAndSingleObject(t *testing.T) {
	tool := CreateEditTool()

	encoded, err := tool.PrepareArguments(json.RawMessage(`{"path":"a.txt","edits":"[{\"oldText\":\"x\",\"newText\":\"y\"}]"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded.Edits) != 1 || encoded.Edits[0].OldText != "x" {
		t.Fatalf("encoded edits = %#v", encoded)
	}

	single, err := tool.PrepareArguments(json.RawMessage(`{"path":"a.txt","edits":{"oldText":"x","newText":"y"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(single.Edits) != 1 || single.Edits[0].NewText != "y" {
		t.Fatalf("single edits = %#v", single)
	}
}

func TestCreateEditToolRejectsEmptyEditList(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "abc")

	_, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{Path: "file.txt"})
	if err == nil {
		t.Fatal("expected invalid-input error")
	}
	if !strings.Contains(err.Error(), "edits must contain at least one replacement") {
		t.Fatalf("error = %v", err)
	}
}

func TestCreateEditToolEditsThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows")
	}
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "target.txt", "before\n")
	if err := os.Symlink("target.txt", filepath.Join(environment.Cwd(), "link.txt")); err != nil {
		t.Fatal(err)
	}

	if _, err := executeEditForTest(t, CreateEditTool(), environment, harnesscontext.BackgroundContext, EditToolInput{
		Path:  "link.txt",
		Edits: []Edit{{OldText: "before", NewText: "after"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := readEditFile(t, environment, "target.txt"); got != "after\n" {
		t.Fatalf("content = %q", got)
	}
}

// editSlowReadEnv wraps the real LocalExecutionEnv and records concurrent text
// reads. It proves concurrent edits of a canonical path (including the symlink
// spelling) are serialized by the shared file mutation queue rather than racing
// their read-modify-write cycles.
type editSlowReadEnv struct {
	*env.LocalExecutionEnv

	mu        sync.Mutex
	active    int
	maxActive int
}

func (e *editSlowReadEnv) ReadTextFile(path string, ctx harnesscontext.Context) harnesstypes.Result[string, harnesstypes.FileError] {
	e.mu.Lock()
	e.active++
	if e.active > e.maxActive {
		e.maxActive = e.active
	}
	e.mu.Unlock()

	time.Sleep(2 * time.Millisecond)
	result := e.LocalExecutionEnv.ReadTextFile(path, ctx)

	e.mu.Lock()
	e.active--
	e.mu.Unlock()
	return result
}

func TestCreateEditToolSerializesConcurrentEdits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not generally available on Windows")
	}
	base := editTestEnvironment(t)
	environment := &editSlowReadEnv{LocalExecutionEnv: base}
	writeEditFile(t, base, "target.txt", "alpha\nbeta\ngamma\n")
	if err := os.Symlink("target.txt", filepath.Join(base.Cwd(), "link.txt")); err != nil {
		t.Fatal(err)
	}

	tool := CreateEditTool()
	var waitGroup sync.WaitGroup
	errs := make(chan error, 2)
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		_, err := executeEditForTest(t, tool, environment, harnesscontext.BackgroundContext, EditToolInput{
			Path:  "target.txt",
			Edits: []Edit{{OldText: "alpha", NewText: "ALPHA"}},
		})
		errs <- err
	}()
	go func() {
		defer waitGroup.Done()
		_, err := executeEditForTest(t, tool, environment, harnesscontext.BackgroundContext, EditToolInput{
			Path:  "link.txt",
			Edits: []Edit{{OldText: "beta", NewText: "BETA"}},
		})
		errs <- err
	}()
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent edit failed: %v", err)
		}
	}

	environment.mu.Lock()
	maxActive := environment.maxActive
	environment.mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("edit read-modify-write cycles overlapped: maxActive = %d", maxActive)
	}
	if got := readEditFile(t, base, "target.txt"); got != "ALPHA\nBETA\ngamma\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestCreateEditToolCancellation(t *testing.T) {
	environment := editTestEnvironment(t)
	writeEditFile(t, environment, "file.txt", "abc")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := executeEditForTest(t, CreateEditTool(), environment, ctx, EditToolInput{
		Path:  "file.txt",
		Edits: []Edit{{OldText: "abc", NewText: "xyz"}},
	})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if got := readEditFile(t, environment, "file.txt"); got != "abc" {
		t.Fatalf("cancelled edit changed the file: %q", got)
	}
}

func TestGenerateDiffStringCollapsesDistantContext(t *testing.T) {
	oldLines := make([]string, 20)
	newLines := make([]string, 20)
	for i := range oldLines {
		oldLines[i] = "line " + itoa(i+1)
		newLines[i] = oldLines[i]
	}
	newLines[9] = "CHANGED"
	oldContent := strings.Join(oldLines, "\n") + "\n"
	newContent := strings.Join(newLines, "\n") + "\n"

	diff, firstChanged := GenerateDiffString(oldContent, newContent)
	if firstChanged == nil || *firstChanged != 10 {
		t.Fatalf("firstChangedLine = %v", firstChanged)
	}
	if !strings.Contains(diff, "? ...") && !strings.Contains(diff, " ...") {
		t.Fatalf("expected collapsed context markers, got %q", diff)
	}
	if !strings.Contains(diff, "+10 CHANGED") {
		t.Fatalf("expected changed line, got %q", diff)
	}
}

func TestDetectLineEndingAndNormalization(t *testing.T) {
	if got := DetectLineEnding("a\nb\r\n"); got != "\n" {
		t.Fatalf("mixed endings = %q", got)
	}
	if got := DetectLineEnding("a\r\nb\n"); got != "\r\n" {
		t.Fatalf("leading CRLF = %q", got)
	}
	if got := NormalizeToLF("a\r\nb\rc\n"); got != "a\nb\nc\n" {
		t.Fatalf("normalize = %q", got)
	}
	if got := RestoreLineEndings("a\nb\n", "\r\n"); got != "a\r\nb\r\n" {
		t.Fatalf("restore = %q", got)
	}
	bom, text := StripBom("\uFEFFhello")
	if bom != "\uFEFF" || text != "hello" {
		t.Fatalf("stripBom = %q, %q", bom, text)
	}
}

func TestNormalizeForFuzzyMatch(t *testing.T) {
	got := NormalizeForFuzzyMatch("  a\u2019b\u2014c\u00a0d  \n\n")
	if got != "  a'b-c d\n\n" {
		t.Fatalf("normalize = %q", got)
	}
}

func TestApplyEditsToNormalizedContentRejectsDuplicatesAndMissing(t *testing.T) {
	if _, err := ApplyEditsToNormalizedContent("foo foo foo", []Edit{{OldText: "foo", NewText: "bar"}}, "f"); err == nil || !strings.Contains(err.Error(), "Found 3 occurrences") {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := ApplyEditsToNormalizedContent("abc", []Edit{{OldText: "zzz", NewText: "y"}}, "f"); err == nil || !strings.Contains(err.Error(), "Could not find") {
		t.Fatalf("missing error = %v", err)
	}
	if _, err := ApplyEditsToNormalizedContent("abc", []Edit{{OldText: "", NewText: "y"}}, "f"); err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("empty error = %v", err)
	}
}

func TestGenerateUnifiedPatchHeaderOnly(t *testing.T) {
	patch := GenerateUnifiedPatch("file.txt", "one\ntwo\n", "one\nTWO\n")
	want := "--- file.txt\n+++ file.txt\n@@ -1,2 +1,2 @@\n one\n-two\n+TWO\n"
	if patch != want {
		t.Fatalf("patch = %q, want %q", patch, want)
	}
}
