package durable_storage_jsonl_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/minifish-org/pith/packages/chord"
	d "github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/storage/jsonl"
)

func openJudgeStorage(t *testing.T) d.Storage {
	t.Helper()
	s, err := jsonl.Open(context.Background(), t.TempDir(), jsonl.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func mustCommit(t *testing.T, s d.Storage, writes ...d.StorageWrite) d.Seq {
	t.Helper()
	seq, err := s.Commit(context.Background(), writes)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}
func mustID(t *testing.T, s d.Storage) d.ID {
	t.Helper()
	id, err := s.MintID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func jsonValue(s string) json.RawMessage { return json.RawMessage(s) }
func entryWrite(id, conversation d.ID, kind string, data string) d.StorageWrite {
	return d.StorageWrite{Type: "entry", Entry: &d.EntryRecord{ID: id, ConversationID: conversation, Kind: kind, Data: jsonValue(data)}}
}
func root(t *testing.T, s d.Storage) {
	t.Helper()
	mustCommit(t, s, d.StorageWrite{Type: "conversation", Conversation: &d.ConversationRecord{ID: d.RootConversationID}})
}
func ptr[T any](v T) *T { return &v }
func assertJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("JSON got %s want %s", got, want)
	}
}
func objectEqual(t *testing.T, got d.JsonObject, want string) {
	t.Helper()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, b, want)
}
func entryIDs(items []d.EntryRecord) []d.ID {
	out := make([]d.ID, len(items))
	for i, v := range items {
		out[i] = v.ID
	}
	return out
}

func TestPortsmithJudgeDurableJSONLStorageAtomicOwnership(t *testing.T) {
	s := openJudgeStorage(t)
	ctx := context.Background()
	if id := mustID(t, s); id != 2 {
		t.Fatalf("reserved root: first minted ID=%d", id)
	}
	root(t, s)
	input := d.EntryRecord{ID: 10, ConversationID: 1, Kind: "note", Data: jsonValue(`{"nested":[1,2],"__proto__":{"safe":true}}`)}
	seq := mustCommit(t, s, d.StorageWrite{Type: "entry", Entry: &input})
	input.Data[0] = '!'
	first, err := s.Entry(ctx, 10)
	if err != nil || first == nil || first.CommitSeq != seq {
		t.Fatalf("entry=%+v err=%v", first, err)
	}
	assertJSON(t, first.Entry.Data, `{"nested":[1,2],"__proto__":{"safe":true}}`)
	first.Entry.Data[0] = '!'
	second, err := s.Entry(ctx, 10)
	if err != nil || second == nil {
		t.Fatal(err)
	}
	assertJSON(t, second.Entry.Data, `{"nested":[1,2],"__proto__":{"safe":true}}`)
	for _, writes := range [][]d.StorageWrite{
		{entryWrite(11, 1, "transient", `{}`), {Type: "conversation", Conversation: &d.ConversationRecord{ID: 1}}},
		{{Type: "task", Task: &d.TaskRecord{ID: 10, ConversationID: 1, Kind: "collision", Version: 1, Input: jsonValue(`{}`), State: d.TaskState{Status: "pending", Checkpoint: jsonValue(`{"phase":"ready"}`)}}}},
		{entryWrite(12, 1, "duplicate", `{}`), entryWrite(12, 1, "duplicate", `{}`)},
	} {
		if _, err = s.Commit(ctx, writes); err == nil {
			t.Fatal("invalid atomic batch accepted")
		}
	}
	for _, id := range []d.ID{11, 12} {
		v, e := s.Entry(ctx, id)
		if e != nil || v != nil {
			t.Fatalf("failed batch published entry %d: %+v %v", id, v, e)
		}
	}
	if id := mustID(t, s); id <= 10 {
		t.Fatalf("global namespace did not advance past explicit write: %d", id)
	}
	if seq2 := mustCommit(t, s); seq2 <= seq {
		t.Fatalf("sequence not increasing: %d <= %d", seq2, seq)
	}
}

func TestPortsmithJudgeDurableJSONLStorageReplacementAndDedup(t *testing.T) {
	s := openJudgeStorage(t)
	ctx := context.Background()
	root(t, s)
	task := d.TaskRecord{ID: 20, ConversationID: 1, Kind: "job", Version: 1, Input: jsonValue(`{"arg":1}`), State: d.TaskState{Status: "pending", Checkpoint: jsonValue(`{"phase":"ready"}`)}, Memos: map[string]json.RawMessage{"once": jsonValue(`"saved"`)}}
	sub := d.SubmissionRecord{ID: 21, ConversationID: 1, Type: "input", Status: "queued", RequestID: ptr("same")}
	mustCommit(t, s, d.StorageWrite{Type: "task", Task: &task}, d.StorageWrite{Type: "submission", Submission: &sub}, d.StorageWrite{Type: "conversation", Conversation: &d.ConversationRecord{ID: 30}}, d.StorageWrite{Type: "submission", Submission: &d.SubmissionRecord{ID: 31, ConversationID: 30, Type: "write", Status: "queued", RequestID: ptr("same")}})
	task.Memos["once"][0] = '!'
	got, err := s.Task(ctx, 20)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	assertJSON(t, got.Memos["once"], `"saved"`)
	got.Memos["once"][0] = '!'
	task2 := task
	task2.Memos = nil
	task2.State = d.TaskState{Status: "terminal", Outcome: &d.TaskOutcome{Status: "completed", Result: jsonValue(`7`)}}
	sub2 := sub
	sub2.Status = "done"
	sub2.Entry = ptr(d.ID(40))
	sub2.Answer = ptr(d.ID(41))
	mustCommit(t, s, d.StorageWrite{Type: "task", Task: &task2}, d.StorageWrite{Type: "submission", Submission: &sub2})
	got, err = s.Task(ctx, 20)
	if err != nil || got == nil || got.Memos != nil || got.State.Checkpoint != nil || got.State.Status != "terminal" {
		t.Fatalf("not full replacement: %+v %v", got, err)
	}
	for _, conversation := range []d.ID{1, 30} {
		v, e := s.SubmissionByRequest(ctx, conversation, "same")
		want := d.ID(21)
		if conversation == 30 {
			want = 31
		}
		if e != nil || v == nil || v.ID != want {
			t.Fatalf("dedup conversation %d got %+v %v", conversation, v, e)
		}
	}
	page, err := s.ScanTasks(ctx, d.TaskQuery{Status: "pending"}, 10, nil)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("stale task index %+v %v", page, err)
	}
	page, err = s.ScanTasks(ctx, d.TaskQuery{ConversationID: ptr(d.ID(1)), Kind: "job", Status: "terminal", Background: ptr(false)}, 10, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != 20 {
		t.Fatalf("filtered tasks %+v %v", page, err)
	}
	subs, err := s.ScanSubmissions(ctx, d.SubmissionQuery{Status: "done"}, 1, nil)
	if err != nil || len(subs.Items) != 1 || subs.Items[0].ID != 21 {
		t.Fatalf("submission replacement index %+v %v", subs, err)
	}
}

func TestPortsmithJudgeDurableJSONLStorageForkCapsAndPaging(t *testing.T) {
	s := openJudgeStorage(t)
	ctx := context.Background()
	root(t, s)
	seq := mustCommit(t, s, entryWrite(10, 1, "old", `{}`), entryWrite(20, 1, "cut", `{}`), entryWrite(30, 1, "excluded", `{}`))
	mustCommit(t, s, d.StorageWrite{Type: "conversation", Conversation: &d.ConversationRecord{ID: 40, Parent: &d.ConversationParent{ConversationID: 1, At: 20}}}, entryWrite(50, 40, "child", `{}`), d.StorageWrite{Type: "entry", Entry: &d.EntryRecord{ID: 60, ConversationID: 40, Kind: "marker", Head: ptr(d.ID(50))}})
	mustCommit(t, s, d.StorageWrite{Type: "conversation", Conversation: &d.ConversationRecord{ID: 70, Parent: &d.ConversationParent{ConversationID: 40, At: 50}}}, entryWrite(80, 70, "grandchild", `{}`))
	page, err := s.ScanEntries(ctx, d.EntryQuery{ConversationID: 70}, 2, nil)
	if err != nil || !reflect.DeepEqual(entryIDs(page.Items), []d.ID{80, 50}) || len(page.Next) == 0 {
		t.Fatalf("first page %+v %v", page, err)
	}
	mustCommit(t, s, entryWrite(90, 70, "newer", `{}`))
	rest, err := s.ScanEntries(ctx, d.EntryQuery{ConversationID: 70}, 2, page.Next)
	if err != nil || !reflect.DeepEqual(entryIDs(rest.Items), []d.ID{20, 10}) || len(rest.Next) != 0 {
		t.Fatalf("cursor after new commit %+v %v", rest, err)
	}
	visible, err := s.VisibleEntry(ctx, 70, 20)
	if err != nil || visible == nil || visible.CommitSeq != seq {
		t.Fatalf("inherited exact entry %+v %v", visible, err)
	}
	for _, id := range []d.ID{30, 60} {
		v, e := s.VisibleEntry(ctx, 70, id)
		if e != nil || v != nil {
			t.Fatalf("entry %d leaked through ancestry %+v %v", id, v, e)
		}
	}
	marker, err := s.FindLatestHeadMarker(ctx, 40, nil)
	if err != nil || marker == nil || marker.ID != 60 || marker.Head == nil || *marker.Head != 50 {
		t.Fatalf("head marker %+v %v", marker, err)
	}
	marker, err = s.FindLatestHeadMarker(ctx, 70, nil)
	if err != nil || marker != nil {
		t.Fatalf("capped head leaked %+v %v", marker, err)
	}
	bounded, err := s.ScanEntries(ctx, d.EntryQuery{ConversationID: 70, MinEntryID: ptr(d.ID(20)), MaxEntryID: ptr(d.ID(50))}, 10, nil)
	if err != nil || !reflect.DeepEqual(entryIDs(bounded.Items), []d.ID{50, 20}) {
		t.Fatalf("inclusive bounds %+v %v", bounded, err)
	}
}

func TestPortsmithJudgeDurableJSONLStorageDocuments(t *testing.T) {
	s := openJudgeStorage(t)
	ctx := context.Background()
	root(t, s)
	scope := d.DocumentScope{Kind: "conversation", ConversationID: 1}
	record := d.DocumentCreate{ID: 100, Kind: "counter", Scope: scope, History: "rewindable", Fork: "asOf"}
	content := d.DocumentContent{Kind: "base", Version: 1, Value: d.JsonObject{"count": 0, "nested": d.JsonObject{"ok": true}}}
	create := mustCommit(t, s, d.StorageWrite{Type: "document.create", Record: &record, Content: &content})
	content.Value["count"] = 99
	changed := mustCommit(t, s, d.StorageWrite{Type: "document.change", ID: 100, Content: &d.DocumentContent{Kind: "delta", Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 1}}}})
	old, err := s.Document(ctx, 100, d.AtSeq(create))
	if err != nil || old == nil || old.DeltasSinceBase != 0 {
		t.Fatalf("base %+v %v", old, err)
	}
	objectEqual(t, old.Value, `{"count":0,"nested":{"ok":true}}`)
	cur, err := s.Document(ctx, 100, d.CurrentDocument())
	if err != nil || cur == nil || cur.DeltasSinceBase != 1 {
		t.Fatalf("delta %+v %v", cur, err)
	}
	objectEqual(t, cur.Value, `{"count":1,"nested":{"ok":true}}`)
	cur.Value["nested"].(map[string]any)["ok"] = false
	cur, err = s.Document(ctx, 100, d.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	objectEqual(t, cur.Value, `{"count":1,"nested":{"ok":true}}`)
	if _, err = s.Commit(ctx, []d.StorageWrite{{Type: "document.change", ID: 100, Content: &d.DocumentContent{Kind: "delta", Version: 2, Ops: nil}}}); err == nil {
		t.Fatal("delta crossed definition version")
	}
	checkpoint := mustCommit(t, s, d.StorageWrite{Type: "document.change", ID: 100, Content: &d.DocumentContent{Kind: "base", Version: 2, Value: d.JsonObject{"count": 2}}})
	prior, err := s.Document(ctx, 100, d.AtSeq(changed))
	if err != nil || prior == nil || prior.Version != 1 {
		t.Fatalf("checkpoint erased history %+v %v", prior, err)
	}
	objectEqual(t, prior.Value, `{"count":1,"nested":{"ok":true}}`)
	retire := mustCommit(t, s, d.StorageWrite{Type: "document.retire", ID: 100}, d.StorageWrite{Type: "document.create", Record: &d.DocumentCreate{ID: 101, Kind: "counter", Scope: scope, History: "rewindable", Fork: "asOf"}, Content: &d.DocumentContent{Kind: "base", Version: 3, Value: d.JsonObject{"count": 3}}})
	addr := d.DocumentAddress{Kind: "counter", Scope: scope}
	live, err := s.FindDocument(ctx, addr, d.CurrentDocument())
	if err != nil || live == nil || live.ID != 101 {
		t.Fatalf("reincarnation %+v %v", live, err)
	}
	alive, err := s.FindDocument(ctx, addr, d.AtSeq(checkpoint))
	if err != nil || alive == nil || alive.ID != 100 {
		t.Fatalf("historic address %+v %v", alive, err)
	}
	retired, err := s.Document(ctx, 100, d.AtSeq(retire))
	if err != nil || retired != nil {
		t.Fatalf("retirement not half-open %+v %v", retired, err)
	}
	before, err := s.Document(ctx, 100, d.AtSeq(checkpoint))
	if err != nil || before == nil {
		t.Fatal(err)
	}
	objectEqual(t, before.Value, `{"count":2}`)
	copied := d.DocumentCreate{ID: 102, Kind: "copy", Scope: scope, History: "rewindable", Fork: "asOf"}
	mustCommit(t, s, d.StorageWrite{Type: "document.copy", Record: &copied, Source: &d.DocumentCopySource{ID: 100, At: d.AtSeq(checkpoint)}})
	copyValue, err := s.Document(ctx, 102, d.CurrentDocument())
	if err != nil || copyValue == nil || copyValue.Version != 2 {
		t.Fatalf("definition-free copy %+v %v", copyValue, err)
	}
	objectEqual(t, copyValue.Value, `{"count":2}`)
	docs, err := s.ScanDocuments(ctx, d.DocumentQuery{Scope: scope, At: d.CurrentDocument()}, 1, nil)
	if err != nil || len(docs.Items) != 1 || docs.Items[0].ID != 101 || len(docs.Next) == 0 {
		t.Fatalf("document paging %+v %v", docs, err)
	}
	next, err := s.ScanDocuments(ctx, d.DocumentQuery{Scope: scope, At: d.CurrentDocument()}, 1, docs.Next)
	if err != nil || len(next.Items) != 1 || next.Items[0].ID != 102 || len(next.Next) != 0 {
		t.Fatalf("document continuation %+v %v", next, err)
	}
}

func TestPortsmithJudgeDurableJSONLStorageCurrentOnlyAndAddressKeys(t *testing.T) {
	s := openJudgeStorage(t)
	ctx := context.Background()
	root(t, s)
	scope := d.DocumentScope{Kind: "session"}
	first := d.DocumentCreate{ID: 200, Kind: "value", Scope: scope}
	second := d.DocumentCreate{ID: 201, Kind: "value", Scope: scope, Key: ptr("")}
	created := mustCommit(t, s, d.StorageWrite{Type: "document.create", Record: &first, Content: &d.DocumentContent{Kind: "base", Version: 1, Value: d.JsonObject{"a": 1}}}, d.StorageWrite{Type: "document.create", Record: &second, Content: &d.DocumentContent{Kind: "base", Version: 1, Value: d.JsonObject{"a": 2}}})
	a, err := s.FindDocument(ctx, d.DocumentAddress{Kind: "value", Scope: scope}, d.CurrentDocument())
	if err != nil || a == nil || a.ID != 200 {
		t.Fatalf("singleton %+v %v", a, err)
	}
	b, err := s.FindDocument(ctx, d.DocumentAddress{Kind: "value", Scope: scope, Key: ptr("")}, d.CurrentDocument())
	if err != nil || b == nil || b.ID != 201 {
		t.Fatalf("empty family key %+v %v", b, err)
	}
	if _, err = s.Document(ctx, 200, d.AtSeq(created)); err == nil {
		t.Fatal("current-only document provided historical content")
	}
	if _, err = s.Commit(ctx, []d.StorageWrite{{Type: "document.create", Record: &d.DocumentCreate{ID: 202, Kind: "value", Scope: scope}, Content: &d.DocumentContent{Kind: "base", Version: 1, Value: d.JsonObject{}}}}); err == nil {
		t.Fatal("duplicate live address accepted")
	}
	seq := mustCommit(t, s, d.StorageWrite{Type: "document.create", Record: &d.DocumentCreate{ID: 203, Kind: "empty", Scope: scope}, Content: &d.DocumentContent{Kind: "base", Version: 1, Value: d.JsonObject{}}}, d.StorageWrite{Type: "document.retire", ID: 203})
	docs, err := s.ScanDocuments(ctx, d.DocumentQuery{Scope: scope, At: d.AtSeq(seq), Kind: "empty"}, 10, nil)
	if err != nil || len(docs.Items) != 0 {
		t.Fatalf("empty lifetime appeared %+v %v", docs, err)
	}
}

func TestPortsmithJudgeDurableJSONLStorageClose(t *testing.T) {
	s := openJudgeStorage(t)
	ctx := context.Background()
	root(t, s)
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(ctx, nil); err == nil {
		t.Fatal("closed commit accepted")
	}
	if _, err := s.MintID(ctx); err == nil {
		t.Fatal("closed ID allocation accepted")
	}
	if _, err := s.Conversation(ctx, 1); err == nil {
		t.Fatal("closed read accepted")
	}
}
