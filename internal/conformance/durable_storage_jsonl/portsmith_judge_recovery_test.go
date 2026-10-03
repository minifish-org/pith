package durable_storage_jsonl_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	d "github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/storage/jsonl"
)

func openDiskStorage(ctx context.Context, path string) (d.Storage, error) {
	return jsonl.Open(ctx, path, jsonl.Options{Fsync: true})
}

func TestPortsmithJudgeDurableJSONLStorageCrashChild(t *testing.T) {
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

func TestPortsmithJudgeDurableJSONLStorageKillAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Timeout is a broken-test guard only; the OS pipe establishes the crash point.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, executable, "-test.run=^TestPortsmithJudgeDurableJSONLStorageCrashChild$", "-test.count=1")
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

func seedJsonl(t *testing.T, path string) {
	t.Helper()
	s, err := openDiskStorage(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	root(t, s)
	mustCommit(t, s, d.StorageWrite{Type: "document.create", Record: &d.DocumentCreate{ID: 2, Kind: "state", Scope: d.DocumentScope{Kind: "session"}}, Content: &d.DocumentContent{Kind: "base", Version: 1, Value: d.JsonObject{"value": "kept"}}})
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPortsmithJudgeDurableJSONLTornUTF8Tail(t *testing.T) {
	path := t.TempDir()
	seedJsonl(t, path)
	paths := []string{filepath.Join(path, "main.jsonl"), filepath.Join(path, "doc-2.jsonl")}
	sizes := make([]int64, len(paths))
	for i, file := range paths {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		sizes[i] = info.Size()
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.Fatal(err)
		}
		torn := []byte("{\"torn\":\"\xe2\x82")
		if _, err = f.Write(torn); err != nil {
			t.Fatal(err)
		}
		if err = f.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s, err := openDiskStorage(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	for i, file := range paths {
		info, err := os.Stat(file)
		if err != nil || info.Size() != sizes[i] {
			t.Fatalf("tail not truncated at byte boundary %s size=%v err=%v", file, info, err)
		}
	}
	doc, err := s.Document(context.Background(), 2, d.CurrentDocument())
	if err != nil || doc == nil {
		t.Fatal(err)
	}
	objectEqual(t, doc.Value, `{"value":"kept"}`)
}

func TestPortsmithJudgeDurableJSONLUnconfirmedTail(t *testing.T) {
	path := t.TempDir()
	seedJsonl(t, path)
	file := filepath.Join(path, "doc-2.jsonl")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("{\"format\":1,\"type\":\"record\",\"seq\":3,\"ordinal\":0,\"payload\":{\"type\":\"document\",\"id\":2,\"content\":{\"kind\":\"base\",\"version\":1,\"value\":{\"value\":\"ghost\"}}}}\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s, err := openDiskStorage(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	after, err := os.ReadFile(file)
	if err != nil || string(after) != string(before) {
		t.Fatalf("unconfirmed tail not removed %v", err)
	}
	doc, err := s.Document(context.Background(), 2, d.CurrentDocument())
	if err != nil || doc == nil {
		t.Fatal(err)
	}
	objectEqual(t, doc.Value, `{"value":"kept"}`)
}

func TestPortsmithJudgeDurableJSONLCorruption(t *testing.T) {
	for _, mode := range []string{"missing-confirmed", "malformed-complete", "invalid-complete-utf8"} {
		t.Run(mode, func(t *testing.T) {
			path := t.TempDir()
			seedJsonl(t, path)
			var err error
			if mode == "missing-confirmed" {
				err = os.WriteFile(filepath.Join(path, "doc-2.jsonl"), nil, 0600)
			} else {
				f, e := os.OpenFile(filepath.Join(path, "main.jsonl"), os.O_WRONLY|os.O_APPEND, 0600)
				if e != nil {
					t.Fatal(e)
				}
				text := "not json\n"
				if mode == "invalid-complete-utf8" {
					text = "{\"x\":\"\xff\"}\n"
				}
				_, err = f.WriteString(text)
				f.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			s, err := openDiskStorage(context.Background(), path)
			if s != nil {
				_ = s.Close(context.Background())
			}
			if err == nil {
				t.Fatal("corrupted committed file was silently accepted")
			}
			var corruption *jsonl.CorruptionError
			if !errors.As(err, &corruption) {
				t.Fatalf("corruption error not identifiable: %T %v", err, err)
			}
		})
	}
}
