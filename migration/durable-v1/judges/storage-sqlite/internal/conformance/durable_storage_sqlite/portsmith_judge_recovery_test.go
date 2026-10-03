package durable_storage_sqlite_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	d "github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/storage/sqlite"
	_ "modernc.org/sqlite"
)

func openDiskStorage(ctx context.Context, path string) (d.Storage, error) {
	return sqlite.Open(ctx, path, sqlite.Options{})
}

func TestPortsmithJudgeDurableSQLiteStorageCrashChild(t *testing.T) {
	path := os.Getenv("PORTSMITH_DURABLE_CRASH_PATH")
	if path == "" {
		return
	}
	ctx := context.Background()
	s, err := openDiskStorage(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := s.Commit(ctx, []d.StorageWrite{
		{Type: "conversation", Conversation: &d.ConversationRecord{ID: 1}},
		entryWrite(2, 1, "acknowledged", `{"value":"kept"}`),
		{Type: "task", Task: &d.TaskRecord{ID: 3, ConversationID: 1, Kind: "pending-job", Version: 1, Input: jsonValue(`{}`), State: d.TaskState{Status: "pending", Checkpoint: jsonValue(`{"phase":"resume"}`)}}},
		{Type: "submission", Submission: &d.SubmissionRecord{ID: 4, ConversationID: 1, RequestID: ptr("durable-request"), Type: "input", Status: "queued"}},
		{Type: "document.create", Record: &d.DocumentCreate{ID: 5, Kind: "state", Scope: d.DocumentScope{Kind: "session"}}, Content: &d.DocumentContent{Kind: "base", Version: 1, Value: d.JsonObject{"value": "kept"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	session := d.NewSession(s)
	if err = session.Commit(ctx, func(tx d.Tx) error {
		ghost, err := tx.AppendEntry(ctx, 1, d.EntryDraft{Kind: "uncommitted", Data: jsonValue(`{"ghost":true}`)})
		if err != nil {
			return err
		}
		line, _ := json.Marshal(struct {
			Seq   d.Seq
			Ghost d.ID
		}{seq, ghost.ID})
		if _, err = fmt.Fprintln(os.Stdout, string(line)); err != nil {
			return err
		}
		// Parent kills after this pipe announcement; no timer establishes the boundary.
		var one [1]byte
		_, err = os.Stdin.Read(one[:])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash child was released without being killed")
}

func TestPortsmithJudgeDurableSQLiteStorageKillAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Timeout is a broken-test guard only; the OS pipe establishes the crash point.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestPortsmithJudgeDurableSQLiteStorageCrashChild$", "-test.count=1")
	child.Env = append(os.Environ(), "PORTSMITH_DURABLE_CRASH_PATH="+path)
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	child.Stderr = os.Stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.Process != nil {
			_ = child.Process.Kill()
		}
	})
	line, err := bufio.NewReader(output).ReadBytes('\n')
	if err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("child never reached durable/uncommitted boundary: %v", err)
	}
	var ready struct {
		Seq   d.Seq
		Ghost d.ID
	}
	if err = json.Unmarshal(line, &ready); err != nil {
		_ = child.Process.Kill()
		_ = child.Wait()
		t.Fatalf("unexpected child output %q: %v", line, err)
	}
	if ready.Seq <= 0 || ready.Ghost <= 5 {
		t.Fatalf("invalid ready boundary %+v", ready)
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err == nil {
		t.Fatal("child exited normally instead of a real process kill")
	}
	s, err := openDiskStorage(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	acknowledged, err := s.Entry(context.Background(), 2)
	if err != nil || acknowledged == nil || acknowledged.CommitSeq != ready.Seq {
		t.Fatalf("acknowledged entry lost %+v %v", acknowledged, err)
	}
	assertJSON(t, acknowledged.Entry.Data, `{"value":"kept"}`)
	ghost, err := s.Entry(context.Background(), ready.Ghost)
	if err != nil || ghost != nil {
		t.Fatalf("uncommitted callback leaked %+v %v", ghost, err)
	}
	task, err := s.Task(context.Background(), 3)
	if err != nil || task == nil || task.State.Status != "pending" {
		t.Fatalf("checkpoint lost %+v %v", task, err)
	}
	assertJSON(t, task.State.Checkpoint, `{"phase":"resume"}`)
	sub, err := s.SubmissionByRequest(context.Background(), 1, "durable-request")
	if err != nil || sub == nil || sub.ID != 4 || sub.Status != "queued" {
		t.Fatalf("submission lost %+v %v", sub, err)
	}
	doc, err := s.Document(context.Background(), 5, d.CurrentDocument())
	if err != nil || doc == nil {
		t.Fatalf("document lost %+v %v", doc, err)
	}
	objectEqual(t, doc.Value, `{"value":"kept"}`)
	if id := mustID(t, s); id <= 5 {
		t.Fatalf("reopened allocator reused committed ID %d", id)
	}
}

func TestPortsmithJudgeDurableSQLiteRowsAndSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "durable.sqlite")
	ctx := context.Background()
	s, err := openDiskStorage(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	root(t, s)
	mustCommit(t, s, entryWrite(2, 1, "real-row", `{"value":1}`))
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(path)
	if err != nil || len(file) < 16 || string(file[:16]) != "SQLite format 3\x00" {
		t.Fatalf("backend is not a SQLite file: %v", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version, count int
	if err = db.QueryRow("SELECT version FROM durable_schema WHERE singleton=1").Scan(&version); err != nil || version != 1 {
		t.Fatalf("schema version %d %v", version, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM entries WHERE id=2 AND conversation_id=1").Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing ordinary indexed entry row %d %v", count, err)
	}
	// Newer schema is not a license to rewrite or silently downgrade a file.
	if _, err = db.Exec("UPDATE durable_schema SET version=999 WHERE singleton=1"); err != nil {
		t.Fatal(err)
	}
	newer, err := openDiskStorage(ctx, path)
	if newer != nil {
		_ = newer.Close(ctx)
	}
	if err == nil {
		t.Fatal("unsupported newer schema accepted")
	}
	if err = db.QueryRow("SELECT version FROM durable_schema WHERE singleton=1").Scan(&version); err != nil || version != 999 {
		t.Fatalf("newer schema was rewritten: %d %v", version, err)
	}
}
