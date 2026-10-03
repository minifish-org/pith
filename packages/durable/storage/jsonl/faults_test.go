package jsonl_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/env"
	"github.com/minifish-org/pith/packages/durable/storage/jsonl"
)

// This file ports the fault-injection cases of upstream
// packages/durable/test/jsonl-storage.test.ts. A filesystem decorator injects
// append/flush/write/rename/remove failures at a chosen call so publication,
// poisoning and reclamation are exercised without sleeps or timing guesses.

type injectedFailure struct {
	operation string
	call      int
	mode      string
}

type instrumentedFS struct {
	env.FileSystem
	failure    *injectedFailure
	operations []string

	appendCalls int
	flushCalls  int
	writeCalls  int
	renameCalls int
	removeCalls int
}

func newInstrumentedFS(t *testing.T, directory string) *instrumentedFS {
	t.Helper()
	base, err := env.NewLocal(directory)
	if err != nil {
		t.Fatal(err)
	}
	return &instrumentedFS{FileSystem: base, operations: []string{}}
}

func (f *instrumentedFS) fail(failure injectedFailure) {
	f.failure = &failure
	f.reset()
}

func (f *instrumentedFS) clear() {
	f.failure = nil
	f.reset()
}

func (f *instrumentedFS) reset() {
	f.appendCalls = 0
	f.flushCalls = 0
	f.writeCalls = 0
	f.renameCalls = 0
	f.removeCalls = 0
	f.operations = []string{}
}

func injected(path string) *env.FileError {
	return &env.FileError{Code: env.FileErrorUnknown, Path: path, Cause: errors.New("injected failure")}
}

func (f *instrumentedFS) AppendFile(ctx context.Context, path string, content []byte) error {
	f.appendCalls++
	f.operations = append(f.operations, "append:"+filepath.Base(path))
	failure := f.failure
	if failure == nil || failure.operation != "append" || failure.call != f.appendCalls {
		return f.FileSystem.AppendFile(ctx, path, content)
	}
	switch failure.mode {
	case "before":
		return injected(path)
	case "short":
		partial := content[:max(1, len(content)/2)]
		if err := f.FileSystem.AppendFile(ctx, path, partial); err != nil {
			return err
		}
		return injected(path)
	default:
		if err := f.FileSystem.AppendFile(ctx, path, content); err != nil {
			return err
		}
		return injected(path)
	}
}

func (f *instrumentedFS) FlushFile(ctx context.Context, path string) error {
	f.flushCalls++
	f.operations = append(f.operations, "flush:"+filepath.Base(path))
	failure := f.failure
	if failure == nil || failure.operation != "flush" || failure.call != f.flushCalls {
		return f.FileSystem.FlushFile(ctx, path)
	}
	if failure.mode == "before" {
		return injected(path)
	}
	if err := f.FileSystem.FlushFile(ctx, path); err != nil {
		return err
	}
	return injected(path)
}

func (f *instrumentedFS) WriteFile(ctx context.Context, path string, content []byte) error {
	f.writeCalls++
	f.operations = append(f.operations, "write:"+filepath.Base(path))
	failure := f.failure
	if failure == nil || failure.operation != "write" || failure.call != f.writeCalls {
		return f.FileSystem.WriteFile(ctx, path, content)
	}
	switch failure.mode {
	case "before":
		return injected(path)
	case "short":
		partial := content[:max(1, len(content)/2)]
		if err := f.FileSystem.WriteFile(ctx, path, partial); err != nil {
			return err
		}
		return injected(path)
	default:
		if err := f.FileSystem.WriteFile(ctx, path, content); err != nil {
			return err
		}
		return injected(path)
	}
}

func (f *instrumentedFS) RenameFile(ctx context.Context, sourcePath, destinationPath string) error {
	f.renameCalls++
	f.operations = append(f.operations, "rename:"+filepath.Base(sourcePath)+"->"+filepath.Base(destinationPath))
	failure := f.failure
	if failure == nil || failure.operation != "rename" || failure.call != f.renameCalls {
		return f.FileSystem.RenameFile(ctx, sourcePath, destinationPath)
	}
	if failure.mode == "before" {
		return injected(sourcePath)
	}
	if err := f.FileSystem.RenameFile(ctx, sourcePath, destinationPath); err != nil {
		return err
	}
	return injected(sourcePath)
}

func (f *instrumentedFS) Remove(ctx context.Context, path string, options env.RemoveOptions) error {
	f.removeCalls++
	f.operations = append(f.operations, "remove:"+filepath.Base(path))
	failure := f.failure
	if failure == nil || failure.operation != "remove" || failure.call != f.removeCalls {
		return f.FileSystem.Remove(ctx, path, options)
	}
	if failure.mode == "before" {
		return injected(path)
	}
	if err := f.FileSystem.Remove(ctx, path, options); err != nil {
		return err
	}
	return injected(path)
}

func openInstrumented(t *testing.T, directory string, fs *instrumentedFS, options jsonl.Options) *jsonl.Storage {
	t.Helper()
	options.FileSystem = fs
	storage, err := jsonl.Open(ctx, directory, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close(ctx) })
	return storage
}

func assertPoisoned(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a poison error")
	}
	var poisoned *jsonl.PoisonedError
	if !errors.As(err, &poisoned) {
		t.Fatalf("error %v is not *jsonl.PoisonedError", err)
	}
	if !contains(err.Error(), "poisoned") {
		t.Fatalf("error %q does not mention poison", err.Error())
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestJSONLRemovesCompletePlusTornMultiRecordAppend(t *testing.T) {
	directory := t.TempDir()
	fs := newInstrumentedFS(t, directory)
	storage := openInstrumented(t, directory, fs, jsonl.Options{})
	createRootJSONL(t, storage)
	taskID := durable.TaskID(mustMintJSONL(t, storage))
	fs.fail(injectedFailure{operation: "append", call: 1, mode: "short"})
	_, err := storage.Commit(ctx, []durable.StorageWrite{
		{Type: durable.WriteTask, Task: pendingJSONLTask(taskID, "first")},
		{Type: durable.WriteTask, Task: pendingJSONLTask(taskID, "second-"+repeat("x", 512))},
	})
	assertPoisoned(t, err)
	sidecarPath := filepath.Join(directory, "task-"+itoa(taskID)+".jsonl")
	data, readErr := os.ReadFile(sidecarPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		t.Fatalf("expected a torn final line, got %q", data)
	}
	if newlines := countBytes(data, '\n'); newlines != 1 {
		t.Fatalf("complete lines=%d want 1", newlines)
	}
	_ = storage.Close(ctx)
	reopened := openJSONL(t, directory, jsonl.Options{})
	if task, err := reopened.Task(ctx, taskID); err != nil || task != nil {
		t.Fatalf("unconfirmed task visible %+v %v", task, err)
	}
	if size := fileSize(t, sidecarPath); size != 0 {
		t.Fatalf("sidecar size=%d want 0", size)
	}
}

func TestJSONLSerializesCandidateBeforeIO(t *testing.T) {
	directory := t.TempDir()
	fs := newInstrumentedFS(t, directory)
	storage := openInstrumented(t, directory, fs, jsonl.Options{})
	createRootJSONL(t, storage)
	fs.clear()
	if _, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteEntry}}); err == nil {
		t.Fatal("invalid batch accepted")
	}
	if len(fs.operations) != 0 {
		t.Fatalf("preparation failure performed I/O: %v", fs.operations)
	}
	if seq := mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: 10, ConversationID: 1, Kind: "good"}}); seq != 2 {
		t.Fatalf("later commit seq=%d want 2", seq)
	}
}

func TestJSONLPoisonsAfterAppendFailure(t *testing.T) {
	cases := []injectedFailure{
		{operation: "append", call: 1, mode: "before"},
		{operation: "append", call: 1, mode: "after"},
		{operation: "append", call: 1, mode: "short"},
		{operation: "append", call: 2, mode: "before"},
		{operation: "append", call: 2, mode: "after"},
		{operation: "append", call: 3, mode: "before"},
		{operation: "append", call: 3, mode: "short"},
		{operation: "append", call: 3, mode: "after"},
	}
	for _, failure := range cases {
		t.Run(fmt.Sprintf("%s-%d", failure.mode, failure.call), func(t *testing.T) {
			directory := t.TempDir()
			fs := newInstrumentedFS(t, directory)
			storage := openInstrumented(t, directory, fs, jsonl.Options{})
			createRootJSONL(t, storage)
			firstID := durable.DocumentID(mustMintJSONL(t, storage))
			secondID := durable.DocumentID(mustMintJSONL(t, storage))
			fs.fail(failure)
			_, err := storage.Commit(ctx, []durable.StorageWrite{
				{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(firstID, "first")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"text": "α"}}},
				{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(secondID, "second")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"text": "β"}}},
			})
			assertPoisoned(t, err)
			if _, readErr := storage.Document(ctx, firstID, durable.CurrentDocument()); readErr == nil {
				t.Fatal("poisoned read succeeded")
			}
			_ = storage.Close(ctx)
			reopened := openJSONL(t, directory, jsonl.Options{})
			markerSurvived := failure.call == 3 && failure.mode == "after"
			for id, want := range map[durable.DocumentID]string{firstID: "α", secondID: "β"} {
				doc, err := reopened.Document(ctx, id, durable.CurrentDocument())
				if err != nil {
					t.Fatal(err)
				}
				if markerSurvived {
					if doc == nil {
						t.Fatalf("confirmed document %d lost", id)
					}
					assertJSONEqual(t, doc.Value["text"], fmt.Sprintf("%q", want))
				} else if doc != nil {
					t.Fatalf("unconfirmed document %d survived", id)
				}
			}
		})
	}
}

func TestJSONLPoisonsAfterFlushFailureAndNeverWritesMarker(t *testing.T) {
	cases := []injectedFailure{
		{operation: "flush", call: 1, mode: "before"},
		{operation: "flush", call: 1, mode: "after"},
		{operation: "flush", call: 2, mode: "before"},
		{operation: "flush", call: 2, mode: "after"},
	}
	for _, failure := range cases {
		t.Run(fmt.Sprintf("%s-%d", failure.mode, failure.call), func(t *testing.T) {
			directory := t.TempDir()
			fs := newInstrumentedFS(t, directory)
			storage := openInstrumented(t, directory, fs, jsonl.Options{Fsync: true})
			createRootJSONL(t, storage)
			firstID := durable.DocumentID(mustMintJSONL(t, storage))
			secondID := durable.DocumentID(mustMintJSONL(t, storage))
			fs.fail(failure)
			_, err := storage.Commit(ctx, []durable.StorageWrite{
				{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(firstID, "flush.first")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{}}},
				{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(secondID, "flush.second")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{}}},
			})
			assertPoisoned(t, err)
			_ = storage.Close(ctx)
			reopened := openJSONL(t, directory, jsonl.Options{})
			for _, id := range []durable.DocumentID{firstID, secondID} {
				if doc, err := reopened.Document(ctx, id, durable.CurrentDocument()); err != nil || doc != nil {
					t.Fatalf("document %d survived a failed flush: %+v %v", id, doc, err)
				}
			}
		})
	}
}

func TestJSONLOrdersPublicationFlushesExactly(t *testing.T) {
	for _, fsync := range []bool{false, true} {
		t.Run(fmt.Sprintf("fsync=%v", fsync), func(t *testing.T) {
			directory := t.TempDir()
			fs := newInstrumentedFS(t, directory)
			storage := openInstrumented(t, directory, fs, jsonl.Options{Fsync: fsync})
			createRootJSONL(t, storage)
			firstID := durable.DocumentID(mustMintJSONL(t, storage))
			secondID := durable.DocumentID(mustMintJSONL(t, storage))
			fs.clear()
			mustCommitJSONL(t, storage,
				durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(secondID, "second")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{}}},
				durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(firstID, "first")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{}}},
			)
			expected := []string{
				"append:doc-" + itoa(secondID) + ".jsonl",
				"append:doc-" + itoa(firstID) + ".jsonl",
			}
			if fsync {
				expected = append(expected, "flush:doc-"+itoa(secondID)+".jsonl", "flush:doc-"+itoa(firstID)+".jsonl")
			}
			expected = append(expected, "append:main.jsonl")
			assertOperations(t, fs.operations, expected)

			fs.clear()
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: firstID, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"checkpoint": true}}})
			reclaim := []string{"append:doc-" + itoa(firstID) + ".jsonl"}
			if fsync {
				reclaim = append(reclaim, "flush:doc-"+itoa(firstID)+".jsonl")
			}
			reclaim = append(reclaim, "append:main.jsonl")
			if fsync {
				reclaim = append(reclaim, "flush:main.jsonl")
			}
			reclaim = append(reclaim,
				"write:doc-"+itoa(firstID)+".jsonl.reclaim",
			)
			if fsync {
				reclaim = append(reclaim, "flush:doc-"+itoa(firstID)+".jsonl.reclaim")
			}
			reclaim = append(reclaim, "rename:doc-"+itoa(firstID)+".jsonl.reclaim->doc-"+itoa(firstID)+".jsonl")
			assertOperations(t, fs.operations, reclaim)

			fs.clear()
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: mustMintJSONL(t, storage), ConversationID: 1, Kind: "main-only"}})
			assertOperations(t, fs.operations, []string{"append:main.jsonl"})

			taskID := durable.TaskID(mustMintJSONL(t, storage))
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteTask, Task: pendingJSONLTask(taskID, "")})
			fs.clear()
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteTask, Task: terminalJSONLTask(taskID)})
			expected = []string{"append:main.jsonl"}
			if fsync {
				expected = append(expected, "flush:main.jsonl")
			}
			expected = append(expected, "remove:task-"+itoa(taskID)+".jsonl")
			assertOperations(t, fs.operations, expected)
		})
	}
}

func TestJSONLRecoversCommittedBaseAcrossReclaimFailures(t *testing.T) {
	cases := []injectedFailure{
		{operation: "write", call: 1, mode: "before"},
		{operation: "write", call: 1, mode: "short"},
		{operation: "write", call: 1, mode: "after"},
		{operation: "rename", call: 1, mode: "before"},
		{operation: "rename", call: 1, mode: "after"},
	}
	for _, failure := range cases {
		t.Run(fmt.Sprintf("%s-%s", failure.operation, failure.mode), func(t *testing.T) {
			directory := t.TempDir()
			fs := newInstrumentedFS(t, directory)
			storage := openInstrumented(t, directory, fs, jsonl.Options{})
			createRootJSONL(t, storage)
			id := durable.DocumentID(mustMintJSONL(t, storage))
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(id, "")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 0}}})
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: id, Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 1}}}})

			fs.fail(failure)
			seq, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentChange, ID: id, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 2}}}})
			if err != nil || seq != 4 {
				t.Fatalf("commit seq=%d err=%v", seq, err)
			}
			current, err := storage.Document(ctx, id, durable.CurrentDocument())
			if err != nil || current == nil {
				t.Fatal(err)
			}
			assertJSONEqual(t, current.Value, `{"count":2}`)
			_ = storage.Close(ctx)

			reopened := openJSONL(t, directory, jsonl.Options{})
			value, err := reopened.Document(ctx, id, durable.CurrentDocument())
			if err != nil || value == nil {
				t.Fatal(err)
			}
			assertJSONEqual(t, value.Value, `{"count":2}`)
			if lines := readLinesFile(t, filepath.Join(directory, "doc-"+itoa(id)+".jsonl")); len(lines) != 1 {
				t.Fatalf("sidecar lines=%d want 1", len(lines))
			}
			if remnants := reclaimRemnants(t, directory); len(remnants) != 0 {
				t.Fatalf("reclaim remnants: %v", remnants)
			}
		})
	}
}

func TestJSONLRecoversDocumentRetirementReclamation(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			fs := newInstrumentedFS(t, directory)
			storage := openInstrumented(t, directory, fs, jsonl.Options{})
			createRootJSONL(t, storage)
			taskID := durable.TaskID(mustMintJSONL(t, storage))
			id := durable.DocumentID(mustMintJSONL(t, storage))
			mustCommitJSONL(t, storage,
				durable.StorageWrite{Type: durable.WriteTask, Task: pendingJSONLTask(taskID, "")},
				durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: id, Kind: "task.document", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: taskID}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 1}}},
			)
			fs.fail(injectedFailure{operation: "remove", call: 1, mode: mode})
			seq, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentRetire, ID: id}})
			if err != nil || seq != 3 {
				t.Fatalf("commit seq=%d err=%v", seq, err)
			}
			if doc, err := storage.Document(ctx, id, durable.CurrentDocument()); err != nil || doc != nil {
				t.Fatalf("retired document visible %+v %v", doc, err)
			}
			_ = storage.Close(ctx)

			reopened := openJSONL(t, directory, jsonl.Options{})
			if doc, err := reopened.Document(ctx, id, durable.CurrentDocument()); err != nil || doc != nil {
				t.Fatalf("retired document visible after reopen %+v %v", doc, err)
			}
			task, err := reopened.Task(ctx, taskID)
			if err != nil || task == nil || task.State.Status != durable.TaskStatusPending {
				t.Fatalf("task %+v %v", task, err)
			}
			if fileExists(t, filepath.Join(directory, "doc-"+itoa(id)+".jsonl")) {
				t.Fatal("retired sidecar not reclaimed")
			}
			if remnants := reclaimRemnants(t, directory); len(remnants) != 0 {
				t.Fatalf("reclaim remnants: %v", remnants)
			}
		})
	}
}

func TestJSONLDefersReclamationAfterAuthorizingMainFlushFailure(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			fs := newInstrumentedFS(t, directory)
			storage := openInstrumented(t, directory, fs, jsonl.Options{Fsync: true})
			createRootJSONL(t, storage)
			id := durable.DocumentID(mustMintJSONL(t, storage))
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(id, "")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 0}}})
			fs.fail(injectedFailure{operation: "flush", call: 2, mode: mode})
			seq, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentChange, ID: id, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 2}}}})
			if err != nil || seq != 3 {
				t.Fatalf("commit seq=%d err=%v", seq, err)
			}
			assertOperations(t, fs.operations, []string{
				"append:doc-" + itoa(id) + ".jsonl",
				"flush:doc-" + itoa(id) + ".jsonl",
				"append:main.jsonl",
				"flush:main.jsonl",
			})
			if lines := readLinesFile(t, filepath.Join(directory, "doc-"+itoa(id)+".jsonl")); len(lines) != 2 {
				t.Fatalf("sidecar lines=%d want 2", len(lines))
			}
			_ = storage.Close(ctx)

			recoveryFS := newInstrumentedFS(t, directory)
			recoveryFS.fail(injectedFailure{operation: "flush", call: 1, mode: mode})
			deferred := openInstrumented(t, directory, recoveryFS, jsonl.Options{Fsync: true})
			value, err := deferred.Document(ctx, id, durable.CurrentDocument())
			if err != nil || value == nil {
				t.Fatal(err)
			}
			assertJSONEqual(t, value.Value, `{"count":2}`)
			assertOperations(t, recoveryFS.operations, []string{"flush:main.jsonl"})
			if lines := readLinesFile(t, filepath.Join(directory, "doc-"+itoa(id)+".jsonl")); len(lines) != 2 {
				t.Fatalf("deferred sidecar lines=%d want 2", len(lines))
			}
			_ = deferred.Close(ctx)

			reclaimed := openJSONL(t, directory, jsonl.Options{Fsync: true})
			value, err = reclaimed.Document(ctx, id, durable.CurrentDocument())
			if err != nil || value == nil {
				t.Fatal(err)
			}
			assertJSONEqual(t, value.Value, `{"count":2}`)
			if lines := readLinesFile(t, filepath.Join(directory, "doc-"+itoa(id)+".jsonl")); len(lines) != 1 {
				t.Fatalf("reclaimed sidecar lines=%d want 1", len(lines))
			}
		})
	}
}

func TestJSONLKeepsCommittedBaseAfterReclaimTempFlushFailure(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			fs := newInstrumentedFS(t, directory)
			storage := openInstrumented(t, directory, fs, jsonl.Options{Fsync: true})
			createRootJSONL(t, storage)
			id := durable.DocumentID(mustMintJSONL(t, storage))
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(id, "")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 0}}})
			fs.fail(injectedFailure{operation: "flush", call: 3, mode: mode})
			seq, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentChange, ID: id, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 2}}}})
			if err != nil || seq != 3 {
				t.Fatalf("commit seq=%d err=%v", seq, err)
			}
			fs.clear()
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: id, Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 3}}}})
			_ = storage.Close(ctx)

			reopened := openJSONL(t, directory, jsonl.Options{Fsync: true})
			value, err := reopened.Document(ctx, id, durable.CurrentDocument())
			if err != nil || value == nil {
				t.Fatal(err)
			}
			assertJSONEqual(t, value.Value, `{"count":3}`)
			if lines := readLinesFile(t, filepath.Join(directory, "doc-"+itoa(id)+".jsonl")); len(lines) != 2 {
				t.Fatalf("sidecar lines=%d want 2", len(lines))
			}
		})
	}
}

func TestJSONLRecoversTerminalTaskReclamation(t *testing.T) {
	for _, mode := range []string{"before", "after"} {
		t.Run(mode, func(t *testing.T) {
			directory := t.TempDir()
			fs := newInstrumentedFS(t, directory)
			storage := openInstrumented(t, directory, fs, jsonl.Options{})
			createRootJSONL(t, storage)
			id := durable.TaskID(mustMintJSONL(t, storage))
			mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteTask, Task: pendingJSONLTask(id, "")})
			fs.fail(injectedFailure{operation: "remove", call: 1, mode: mode})
			seq, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteTask, Task: terminalJSONLTask(id)}})
			if err != nil || seq != 3 {
				t.Fatalf("commit seq=%d err=%v", seq, err)
			}
			task, err := storage.Task(ctx, id)
			if err != nil || task == nil || task.State.Status != durable.TaskStatusTerminal {
				t.Fatalf("terminal task %+v %v", task, err)
			}
			_ = storage.Close(ctx)

			reopened := openJSONL(t, directory, jsonl.Options{})
			task, err = reopened.Task(ctx, id)
			if err != nil || task == nil || task.State.Status != durable.TaskStatusTerminal {
				t.Fatalf("terminal task after reopen %+v %v", task, err)
			}
			if fileExists(t, filepath.Join(directory, "task-"+itoa(id)+".jsonl")) {
				t.Fatal("terminal sidecar not reclaimed")
			}
			if remnants := reclaimRemnants(t, directory); len(remnants) != 0 {
				t.Fatalf("reclaim remnants: %v", remnants)
			}
		})
	}
}

func TestJSONLReclaimsRetiredDocumentOrdinalSidecars(t *testing.T) {
	// A retired current-only document that had a base and deltas must drop its
	// sidecar and stay unreadable after reopen.
	directory := t.TempDir()
	storage := openJSONL(t, directory, jsonl.Options{})
	createRootJSONL(t, storage)
	id := durable.DocumentID(mustMintJSONL(t, storage))
	mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: jsonlPtr(sessionJSONLDocument(id, "")), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 0}}})
	mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: id, Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 1}}}})
	mustCommitJSONL(t, storage, durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: id})
	if fileExists(t, filepath.Join(directory, "doc-"+itoa(id)+".jsonl")) {
		t.Fatal("sidecar survived retirement")
	}
	_ = storage.Close(ctx)
	reopened := openJSONL(t, directory, jsonl.Options{})
	if doc, err := reopened.Document(ctx, id, durable.CurrentDocument()); err != nil || doc != nil {
		t.Fatalf("retired document readable %+v %v", doc, err)
	}
}

func assertOperations(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("operations=%v want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("operations[%d]=%q want %q (full %v)", index, got[index], want[index], got)
		}
	}
}

func reclaimRemnants(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	remnants := []string{}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".reclaim" {
			remnants = append(remnants, entry.Name())
		}
	}
	return remnants
}

func repeat(value string, times int) string {
	out := make([]byte, 0, len(value)*times)
	for i := 0; i < times; i++ {
		out = append(out, value...)
	}
	return string(out)
}

func countBytes(data []byte, target byte) int {
	count := 0
	for _, value := range data {
		if value == target {
			count++
		}
	}
	return count
}
