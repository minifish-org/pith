package memory_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/storage/memory"
)

// This file ports the runner-independent cases of upstream
// packages/durable/src/testing/storage-conformance.ts against the in-memory
// reference storage. The shared suite is frozen separately as an independent
// judge; this port is candidate-owned evidence and documents the Go-specific
// adaptations where upstream JavaScript object semantics cannot be reproduced.

// TestStorageConformanceMemory runs every ported conformance case.
func TestStorageConformanceMemory(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"reserves ID 1 for the immutable root conversation", conformanceReservesRoot},
		{"commits mixed table writes atomically and rolls all of them back on failure", conformanceMixedAtomicity},
		{"detaches retained writes and every returned record", conformanceDetachesWrites},
		{"detaches prototype-like JSON keys without changing object prototypes", conformancePrototypeKeys},
		{"indexes entries committed out of ID order", conformanceIndexesOutOfOrder},
		{"continues an entry cursor below its last item after a newer commit", conformanceEntryCursor},
		{"paginates conversations by opaque cursor in ascending ID order", conformanceConversationCursor},
		{"filters and pages conversations by durable owner edges", conformanceConversationOwners},
		{"scans deep fork history newest-first through every ancestor cap", conformanceDeepFork},
		{"replaces complete task records and pages filtered task scans", conformanceTaskReplacement},
		{"stores owners and scans waiting and completing tasks by status", conformanceTaskStatuses},
		{"indexes request IDs per conversation and replaces complete submission records", conformanceRequestIDs},
		{"stores passive write submissions without input-only lifecycle states", conformancePassiveWrites},
		{"reconstructs rewindable documents and preserves half-open incarnations", conformanceRewindableDocuments},
		{"streams long document tails across root replacement deltas", conformanceLongTails},
		{"copies stored document bases independently and rejects ambiguous sources", conformanceDocumentCopies},
		{"uses bases for version transitions and rejects historical reads of current-only documents", conformanceVersionTransitions},
		{"indexes logical addresses and exact-scope scans independently", conformanceAddressIndexes},
		{"keeps document lifecycle failures atomic and gives create-plus-retire an empty lifetime", conformanceDocumentLifecycle},
		{"rolls back record tables and secondary indexes when a document command fails", conformanceDocumentRollback},
		{"keeps indexed string identities lossless", conformanceStringIdentity},
		{"keeps one global record ID namespace and rejects exhausted ID minting", conformanceGlobalNamespace},
		{"rejects every operation after close", conformanceAfterClose},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, testCase.run)
	}
}

func newConformanceStorage(t *testing.T) *memory.Storage {
	t.Helper()
	storage := memory.New()
	t.Cleanup(func() { _ = storage.Close(ctx) })
	return storage
}

func createRoot(t *testing.T, storage durable.Storage) durable.ConversationID {
	t.Helper()
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}})
	return durable.RootConversationID
}

func mustCommit(t *testing.T, storage durable.Storage, writes ...durable.StorageWrite) durable.Seq {
	t.Helper()
	seq, err := storage.Commit(ctx, writes)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func mustMint(t *testing.T, storage durable.Storage) durable.ID {
	t.Helper()
	id, err := storage.MintID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pendingTask(id, conversation durable.ID, phase string) durable.TaskRecord {
	if phase == "" {
		phase = "ready"
	}
	return durable.TaskRecord{
		ID:             durable.TaskID(id),
		ConversationID: durable.ConversationID(conversation),
		Kind:           "test.task",
		Version:        1,
		Input:          json.RawMessage(fmt.Sprintf(`{"value":%d}`, id)),
		State:          durable.TaskState{Status: durable.TaskStatusPending, Checkpoint: json.RawMessage(fmt.Sprintf(`{"phase":%q}`, phase))},
	}
}

func entryRecord(id, conversation durable.ID, kind string) durable.EntryRecord {
	if kind == "" {
		kind = "message"
	}
	return durable.EntryRecord{ID: durable.EntryID(id), ConversationID: durable.ConversationID(conversation), Kind: kind}
}

func ptr[T any](value T) *T { return &value }

// --- assertions -------------------------------------------------------------

func normalize(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %#v: %v", value, err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return out
}

func assertEqual(t *testing.T, got, want any) {
	t.Helper()
	left := normalize(t, got)
	right := normalize(t, want)
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("got %s want %s", mustJSON(t, left), mustJSON(t, right))
	}
}

func assertPartial(t *testing.T, got, want any) {
	t.Helper()
	if !partialMatch(normalize(t, got), normalize(t, want)) {
		t.Fatalf("got %s does not match %s", mustJSON(t, normalize(t, got)), mustJSON(t, normalize(t, want)))
	}
}

func partialMatch(got, want any) bool {
	switch wanted := want.(type) {
	case map[string]any:
		actual, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range wanted {
			other, present := actual[key]
			if !present || !partialMatch(other, value) {
				return false
			}
		}
		return true
	case []any:
		actual, ok := got.([]any)
		if !ok || len(actual) != len(wanted) {
			return false
		}
		for index := range wanted {
			if !partialMatch(actual[index], wanted[index]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(got, want)
	}
}

func assertContains(t *testing.T, err error, substring string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q", substring)
	}
	if !strings.Contains(err.Error(), substring) {
		t.Fatalf("error %q does not contain %q", err.Error(), substring)
	}
}

func assertNil(t *testing.T, value any) {
	t.Helper()
	if value == nil {
		return
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Ptr, reflect.Slice, reflect.Map, reflect.Interface:
		if reflected.IsNil() {
			return
		}
	}
	t.Fatalf("expected nil, got %#v", value)
}

func assertJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	assertEqual(t, json.RawMessage(got), json.RawMessage(want))
}

func assertIDs(t *testing.T, got []durable.ID, want ...durable.ID) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ids = %v want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("ids = %v want %v", got, want)
		}
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// --- cases ------------------------------------------------------------------

func conformanceReservesRoot(t *testing.T) {
	storage := newConformanceStorage(t)
	if id := mustMint(t, storage); id != 2 {
		t.Fatalf("first minted ID = %d", id)
	}
	if rootID := createRoot(t, storage); rootID != durable.RootConversationID {
		t.Fatalf("root id = %d", rootID)
	}
	conversation, err := storage.Conversation(ctx, durable.RootConversationID)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, conversation, &durable.ConversationRecord{ID: durable.RootConversationID})
	_, err = storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}}})
	assertContains(t, err, fmt.Sprintf("ID %d already belongs to conversation", durable.RootConversationID))
}

func conformanceMixedAtomicity(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	entryID := mustMint(t, storage)
	taskID := mustMint(t, storage)
	submissionID := mustMint(t, storage)
	task := pendingTask(taskID, rootID, "ready")
	input := durable.SubmissionRecord{
		ID:             durable.SubmissionID(submissionID),
		ConversationID: durable.ConversationID(rootID),
		RequestID:      ptr("request-1"),
		Type:           durable.SubmissionTypeInput,
		Status:         durable.SubmissionStatusPlaced,
		Entry:          ptr(durable.EntryID(entryID)),
	}
	initialSeq := mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "user", Data: json.RawMessage(`{"text":"hello"}`)}},
		durable.StorageWrite{Type: durable.WriteTask, Task: &task},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &input},
	)
	stored, err := storage.Entry(ctx, durable.EntryID(entryID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, stored, &durable.StoredEntry{Entry: durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "user", Data: json.RawMessage(`{"text":"hello"}`)}, CommitSeq: initialSeq})
	storedTask, err := storage.Task(ctx, durable.TaskID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedTask, &task)
	storedSubmission, err := storage.Submission(ctx, durable.SubmissionID(submissionID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedSubmission, &input)

	transientEntryID := mustMint(t, storage)
	runningTask := task
	runningTask.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: json.RawMessage(`{"phase":"effect"}`)}
	doneInput := input
	doneInput.Status = durable.SubmissionStatusDone
	doneInput.Answer = ptr(durable.EntryID(transientEntryID))
	_, err = storage.Commit(ctx, []durable.StorageWrite{
		{Type: durable.WriteTask, Task: &runningTask},
		{Type: durable.WriteSubmission, Submission: &doneInput},
		{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(transientEntryID), ConversationID: durable.ConversationID(rootID), Kind: "assistant"}},
		{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(rootID)}},
	})
	assertContains(t, err, fmt.Sprintf("ID %d already belongs to conversation", rootID))

	storedTask, err = storage.Task(ctx, durable.TaskID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedTask, &task)
	storedSubmission, err = storage.Submission(ctx, durable.SubmissionID(submissionID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedSubmission, &input)
	transient, err := storage.Entry(ctx, durable.EntryID(transientEntryID))
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, transient)

	afterRollbackSeq := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(mustMint(t, storage)), ConversationID: durable.ConversationID(rootID), Kind: "after-rollback"}})
	if afterRollbackSeq <= initialSeq {
		t.Fatalf("sequence after rollback %d <= %d", afterRollbackSeq, initialSeq)
	}
}

func conformanceDetachesWrites(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	entryID := mustMint(t, storage)
	taskID := mustMint(t, storage)
	submissionID := mustMint(t, storage)

	entryData := json.RawMessage(`{"nested":[1,2]}`)
	checkpoint := json.RawMessage(`{"phase":"ready","nested":{"count":1}}`)
	detail := json.RawMessage(`{"codes":["initial"]}`)

	storedEntry := durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "note", Data: entryData}
	storedTask := pendingTask(taskID, rootID, "ready")
	storedTask.State.Checkpoint = checkpoint
	storedInput := durable.SubmissionRecord{ID: durable.SubmissionID(submissionID), ConversationID: durable.ConversationID(rootID), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusUnanswered, Reason: "failed", Detail: detail}
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &storedEntry},
		durable.StorageWrite{Type: durable.WriteTask, Task: &storedTask},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &storedInput},
	)

	entryData[0] = '!'
	checkpoint[0] = '!'
	detail[0] = '!'

	readEntry, err := storage.Entry(ctx, durable.EntryID(entryID))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, readEntry.Entry.Data, `{"nested":[1,2]}`)
	readTask, err := storage.Task(ctx, durable.TaskID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, readTask.State.Checkpoint, `{"phase":"ready","nested":{"count":1}}`)
	readInput, err := storage.Submission(ctx, durable.SubmissionID(submissionID))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, readInput.Detail, `{"codes":["initial"]}`)

	readEntry.Entry.Data[0] = '!'
	readTask.State.Checkpoint[0] = '!'
	readInput.Detail[0] = '!'

	againEntry, err := storage.Entry(ctx, durable.EntryID(entryID))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, againEntry.Entry.Data, `{"nested":[1,2]}`)
	againTask, err := storage.Task(ctx, durable.TaskID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, againTask.State.Checkpoint, `{"phase":"ready","nested":{"count":1}}`)
	againInput, err := storage.Submission(ctx, durable.SubmissionID(submissionID))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, againInput.Detail, `{"codes":["initial"]}`)
}

func conformancePrototypeKeys(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	entryID := mustMint(t, storage)
	data := json.RawMessage(`{"__proto__":{"polluted":false},"constructor":{"label":"stored"},"toString":"value"}`)
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "note", Data: data}})
	data[0] = '!'
	read, err := storage.Entry(ctx, durable.EntryID(entryID))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, read.Entry.Data, `{"__proto__":{"polluted":false},"constructor":{"label":"stored"},"toString":"value"}`)
	read.Entry.Data[0] = '!'
	second, err := storage.Entry(ctx, durable.EntryID(entryID))
	if err != nil {
		t.Fatal(err)
	}
	assertJSON(t, second.Entry.Data, `{"__proto__":{"polluted":false},"constructor":{"label":"stored"},"toString":"value"}`)
}

func conformanceIndexesOutOfOrder(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: 30, ConversationID: durable.ConversationID(rootID), Kind: "message"}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: 10, ConversationID: durable.ConversationID(rootID), Kind: "message"}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: 20, ConversationID: durable.ConversationID(rootID), Kind: "marker", Head: ptr(durable.EntryID(10))}},
	)
	page, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: durable.ConversationID(rootID)}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, entryIDs(page.Items), 30, 20, 10)
	marker, err := storage.FindLatestHeadMarker(ctx, durable.ConversationID(rootID), nil)
	if err != nil {
		t.Fatal(err)
	}
	if marker == nil || marker.ID != 20 {
		t.Fatalf("head marker = %+v", marker)
	}
}

func conformanceEntryCursor(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	oldestID := mustMint(t, storage)
	middleID := mustMint(t, storage)
	newestID := mustMint(t, storage)
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(oldestID), ConversationID: durable.ConversationID(rootID)}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(middleID), ConversationID: durable.ConversationID(rootID)}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(newestID), ConversationID: durable.ConversationID(rootID)}},
	)
	first, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: durable.ConversationID(rootID)}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, entryIDs(first.Items), newestID, middleID)
	appendedID := mustMint(t, storage)
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(appendedID), ConversationID: durable.ConversationID(rootID)}})
	second, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: durable.ConversationID(rootID)}, 2, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertIDs(t, entryIDs(second.Items), oldestID)
	if len(second.Next) != 0 {
		t.Fatalf("unexpected continuation %s", second.Next)
	}
}

func entryIDs(entries []durable.EntryRecord) []durable.ID {
	out := make([]durable.ID, len(entries))
	for index, entry := range entries {
		out[index] = entry.ID
	}
	return out
}

func conformanceConversationCursor(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	secondID := mustMint(t, storage)
	thirdID := mustMint(t, storage)
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(thirdID)}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(secondID)}},
	)
	first, err := storage.ScanConversations(ctx, durable.ConversationQuery{}, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, conversationIDs(first.Items), []durable.ID{rootID, secondID})
	if len(first.Next) == 0 {
		t.Fatal("first page has no continuation")
	}
	second, err := storage.ScanConversations(ctx, durable.ConversationQuery{}, 2, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, conversationIDs(second.Items), []durable.ID{thirdID})
	if len(second.Next) != 0 {
		t.Fatalf("unexpected continuation %s", second.Next)
	}
}

func conversationIDs(records []durable.ConversationRecord) []durable.ID {
	out := make([]durable.ID, len(records))
	for index, record := range records {
		out[index] = record.ID
	}
	return out
}

func conformanceConversationOwners(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	otherOwnerID := mustMint(t, storage)
	firstTaskID := mustMint(t, storage)
	secondTaskID := mustMint(t, storage)
	firstID := mustMint(t, storage)
	secondID := mustMint(t, storage)
	thirdID := mustMint(t, storage)
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(otherOwnerID)}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(firstID), Owner: &durable.ConversationOwner{ConversationID: durable.ConversationID(rootID), TaskID: durable.TaskID(firstTaskID)}}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(secondID), Owner: &durable.ConversationOwner{ConversationID: durable.ConversationID(rootID), TaskID: durable.TaskID(secondTaskID)}}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(thirdID), Owner: &durable.ConversationOwner{ConversationID: durable.ConversationID(otherOwnerID), TaskID: durable.TaskID(firstTaskID)}}},
	)
	first, err := storage.ScanConversations(ctx, durable.ConversationQuery{OwnerConversationID: ptr(durable.ConversationID(rootID))}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, conversationIDs(first.Items), []durable.ID{firstID})
	if len(first.Next) == 0 {
		t.Fatal("first page has no continuation")
	}
	second, err := storage.ScanConversations(ctx, durable.ConversationQuery{OwnerConversationID: ptr(durable.ConversationID(rootID))}, 1, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, conversationIDs(second.Items), []durable.ID{secondID})
	if len(second.Next) != 0 {
		t.Fatalf("unexpected continuation %s", second.Next)
	}
	byTask, err := storage.ScanConversations(ctx, durable.ConversationQuery{OwnerTaskID: ptr(durable.TaskID(firstTaskID))}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, conversationIDs(byTask.Items), []durable.ID{firstID, thirdID})
	both, err := storage.ScanConversations(ctx, durable.ConversationQuery{OwnerConversationID: ptr(durable.ConversationID(rootID)), OwnerTaskID: ptr(durable.TaskID(firstTaskID))}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, conversationIDs(both.Items), []durable.ID{firstID})
}

func conformanceDeepFork(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	rootFirst := mustMint(t, storage)
	rootForkPoint := mustMint(t, storage)
	rootExcludedSameCommit := mustMint(t, storage)
	rootEntriesSeq := mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(rootFirst), ConversationID: durable.ConversationID(rootID), Kind: "message"}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(rootForkPoint), ConversationID: durable.ConversationID(rootID), Kind: "marker", Head: ptr(durable.EntryID(rootFirst))}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(rootExcludedSameCommit), ConversationID: durable.ConversationID(rootID), Kind: "message"}},
	)
	childID := mustMint(t, storage)
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(childID), Parent: &durable.ConversationParent{ConversationID: durable.ConversationID(rootID), At: durable.EntryID(rootForkPoint)}}})
	childForkPoint := mustMint(t, storage)
	childExcluded := mustMint(t, storage)
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(childForkPoint), ConversationID: durable.ConversationID(childID), Kind: "note"}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(childExcluded), ConversationID: durable.ConversationID(childID), Kind: "message"}},
	)
	rootExcludedLater := mustMint(t, storage)
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(rootExcludedLater), ConversationID: durable.ConversationID(rootID), Kind: "message"}})
	grandchildID := mustMint(t, storage)
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(grandchildID), Parent: &durable.ConversationParent{ConversationID: durable.ConversationID(childID), At: durable.EntryID(childForkPoint)}}})
	grandchildHead := mustMint(t, storage)
	grandchildTail := mustMint(t, storage)
	grandchildEntriesSeq := mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(grandchildHead), ConversationID: durable.ConversationID(grandchildID), Kind: "marker", Head: ptr(durable.EntryID(grandchildHead))}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(grandchildTail), ConversationID: durable.ConversationID(grandchildID), Kind: "message"}},
	)
	childExcludedLater := mustMint(t, storage)
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(childExcludedLater), ConversationID: durable.ConversationID(childID), Kind: "message"}})

	query := durable.EntryQuery{ConversationID: durable.ConversationID(grandchildID)}
	first, err := storage.ScanEntries(ctx, query, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entryIDs(first.Items), []durable.ID{grandchildTail, grandchildHead})
	second, err := storage.ScanEntries(ctx, query, 2, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entryIDs(second.Items), []durable.ID{childForkPoint, rootForkPoint})
	third, err := storage.ScanEntries(ctx, query, 2, second.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entryIDs(third.Items), []durable.ID{rootFirst})
	if len(third.Next) != 0 {
		t.Fatalf("unexpected continuation %s", third.Next)
	}

	currentMarker, err := storage.FindLatestHeadMarker(ctx, durable.ConversationID(grandchildID), nil)
	if err != nil {
		t.Fatal(err)
	}
	if currentMarker == nil || currentMarker.ID != durable.EntryID(grandchildHead) || currentMarker.Head == nil || *currentMarker.Head != durable.EntryID(grandchildHead) {
		t.Fatalf("current marker = %+v", currentMarker)
	}
	historicalMarker, err := storage.FindLatestHeadMarker(ctx, durable.ConversationID(grandchildID), ptr(durable.EntryID(childForkPoint)))
	if err != nil {
		t.Fatal(err)
	}
	if historicalMarker == nil || historicalMarker.ID != durable.EntryID(rootForkPoint) || historicalMarker.Head == nil || *historicalMarker.Head != durable.EntryID(rootFirst) {
		t.Fatalf("historical marker = %+v", historicalMarker)
	}
	none, err := storage.FindLatestHeadMarker(ctx, durable.ConversationID(grandchildID), ptr(durable.EntryID(rootFirst)))
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, none)

	activeFirst, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: durable.ConversationID(grandchildID), MinEntryID: currentMarker.Head}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entryIDs(activeFirst.Items), []durable.ID{grandchildTail})
	if len(activeFirst.Next) == 0 {
		t.Fatal("active first page has no continuation")
	}
	activeSecond, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: durable.ConversationID(grandchildID), MinEntryID: currentMarker.Head}, 1, activeFirst.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entryIDs(activeSecond.Items), []durable.ID{grandchildHead})
	if len(activeSecond.Next) != 0 {
		t.Fatalf("unexpected continuation %s", activeSecond.Next)
	}

	bounded, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: durable.ConversationID(grandchildID), MinEntryID: historicalMarker.Head, MaxEntryID: ptr(durable.EntryID(childForkPoint))}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, entryIDs(bounded.Items), []durable.ID{childForkPoint, rootForkPoint, rootFirst})

	rootFirstEntry, err := storage.Entry(ctx, durable.EntryID(rootFirst))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, rootFirstEntry, &durable.StoredEntry{Entry: durable.EntryRecord{ID: durable.EntryID(rootFirst), ConversationID: durable.ConversationID(rootID), Kind: "message"}, CommitSeq: rootEntriesSeq})
	rootForkEntry, err := storage.Entry(ctx, durable.EntryID(rootForkPoint))
	if err != nil {
		t.Fatal(err)
	}
	if rootForkEntry.CommitSeq != rootEntriesSeq {
		t.Fatalf("root fork commit seq = %d", rootForkEntry.CommitSeq)
	}
	grandchildHeadEntry, err := storage.Entry(ctx, durable.EntryID(grandchildHead))
	if err != nil {
		t.Fatal(err)
	}
	if grandchildHeadEntry.CommitSeq != grandchildEntriesSeq {
		t.Fatalf("grandchild head commit seq = %d", grandchildHeadEntry.CommitSeq)
	}
	missing, err := storage.Entry(ctx, durable.EntryID(999999))
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, missing)

	inherited, err := storage.VisibleEntry(ctx, durable.ConversationID(grandchildID), durable.EntryID(rootFirst))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, inherited, &durable.StoredEntry{Entry: durable.EntryRecord{ID: durable.EntryID(rootFirst), ConversationID: durable.ConversationID(rootID), Kind: "message"}, CommitSeq: rootEntriesSeq})
	childForkEntry, err := storage.VisibleEntry(ctx, durable.ConversationID(grandchildID), durable.EntryID(childForkPoint))
	if err != nil {
		t.Fatal(err)
	}
	if childForkEntry.Entry.ConversationID != durable.ConversationID(childID) {
		t.Fatalf("child fork conversation = %d", childForkEntry.Entry.ConversationID)
	}
	for _, id := range []durable.ID{rootExcludedSameCommit, rootExcludedLater, childExcluded, childExcludedLater} {
		hidden, err := storage.VisibleEntry(ctx, durable.ConversationID(grandchildID), durable.EntryID(id))
		if err != nil {
			t.Fatal(err)
		}
		assertNil(t, hidden)
	}
	reverse, err := storage.VisibleEntry(ctx, durable.ConversationID(rootID), durable.EntryID(grandchildHead))
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, reverse)
	if _, err := storage.VisibleEntry(ctx, 999999, durable.EntryID(rootFirst)); err == nil {
		t.Fatal("visible entry of unknown conversation was accepted")
	}
	if _, err := storage.ScanEntries(ctx, durable.EntryQuery{ConversationID: 999999}, 10, nil); err == nil {
		t.Fatal("entry scan of unknown conversation was accepted")
	}
}

func conformanceTaskReplacement(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	firstID := mustMint(t, storage)
	secondID := mustMint(t, storage)
	thirdID := mustMint(t, storage)
	first := pendingTask(firstID, rootID, "ready")
	first.Memos = map[string]json.RawMessage{"winner": json.RawMessage(`"first"`)}
	second := pendingTask(secondID, rootID, "ready")
	second.Background = true
	third := pendingTask(thirdID, rootID, "ready")
	third.AbortRequested = true
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteTask, Task: &first},
		durable.StorageWrite{Type: durable.WriteTask, Task: &second},
		durable.StorageWrite{Type: durable.WriteTask, Task: &third},
	)
	running := first
	running.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: json.RawMessage(`{"phase":"effect","attempt":1}`)}
	running.AbortRequested = true
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteTask, Task: &running})
	stored, err := storage.Task(ctx, durable.TaskID(firstID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, stored, &running)

	terminal := durable.TaskRecord{
		ID:             durable.TaskID(firstID),
		ConversationID: durable.ConversationID(rootID),
		Kind:           first.Kind,
		Version:        first.Version,
		Input:          first.Input,
		State:          durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: json.RawMessage(`{"entryId":99}`)}},
		Background:     false,
		AbortRequested: true,
	}
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteTask, Task: &terminal})
	stored, err = storage.Task(ctx, durable.TaskID(firstID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, stored, &terminal)

	pendingPage, err := storage.ScanTasks(ctx, durable.TaskQuery{Status: durable.TaskStatusPending}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, taskIDs(pendingPage.Items), []durable.ID{secondID})
	if len(pendingPage.Next) == 0 {
		t.Fatal("pending page has no continuation")
	}
	pendingRest, err := storage.ScanTasks(ctx, durable.TaskQuery{Status: durable.TaskStatusPending}, 1, pendingPage.Next)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, taskIDs(pendingRest.Items), []durable.ID{thirdID})
	terminalPage, err := storage.ScanTasks(ctx, durable.TaskQuery{Status: durable.TaskStatusTerminal, AbortRequested: ptr(true)}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, terminalPage.Items, []durable.TaskRecord{terminal})
	backgroundPage, err := storage.ScanTasks(ctx, durable.TaskQuery{Background: ptr(true)}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, taskIDs(backgroundPage.Items), []durable.ID{secondID})
}

func taskIDs(tasks []durable.TaskRecord) []durable.ID {
	out := make([]durable.ID, len(tasks))
	for index, task := range tasks {
		out[index] = task.ID
	}
	return out
}

func conformanceTaskStatuses(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	ownerID := mustMint(t, storage)
	waitingID := mustMint(t, storage)
	completingID := mustMint(t, storage)
	owner := pendingTask(ownerID, rootID, "ready")
	waiting := pendingTask(waitingID, rootID, "next")
	waiting.Owner = ptr(durable.TaskID(ownerID))
	waiting.State = durable.TaskState{Status: durable.TaskStatusWaiting, Checkpoint: json.RawMessage(`{"phase":"next"}`), On: []durable.TaskID{durable.TaskID(ownerID)}, Policy: durable.PolicyAllSettled}
	waiting.Memos = map[string]json.RawMessage{"kept": json.RawMessage(`true`)}
	completing := durable.TaskRecord{
		ID:             durable.TaskID(completingID),
		ConversationID: durable.ConversationID(rootID),
		Kind:           "test.task",
		Version:        1,
		Input:          json.RawMessage(fmt.Sprintf(`{"value":%d}`, completingID)),
		Owner:          ptr(durable.TaskID(ownerID)),
		State:          durable.TaskState{Status: durable.TaskStatusCompleting, Outcome: &durable.TaskOutcome{Status: durable.OutcomeFailed, Error: &durable.TaskOutcomeError{Message: "held"}}},
	}
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteTask, Task: &owner},
		durable.StorageWrite{Type: durable.WriteTask, Task: &waiting},
		durable.StorageWrite{Type: durable.WriteTask, Task: &completing},
	)
	storedWaiting, err := storage.Task(ctx, durable.TaskID(waitingID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedWaiting, &waiting)
	storedCompleting, err := storage.Task(ctx, durable.TaskID(completingID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedCompleting, &completing)

	scan := func(status string) []durable.TaskRecord {
		page, err := storage.ScanTasks(ctx, durable.TaskQuery{Status: status}, 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		return page.Items
	}
	assertEqual(t, scan(durable.TaskStatusWaiting), []durable.TaskRecord{waiting})
	assertEqual(t, scan(durable.TaskStatusCompleting), []durable.TaskRecord{completing})
	assertEqual(t, taskIDs(scan(durable.TaskStatusPending)), []durable.ID{ownerID})

	terminal := completing
	terminal.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: completing.State.Outcome}
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteTask, Task: &terminal})
	assertEqual(t, scan(durable.TaskStatusCompleting), []durable.TaskRecord{})
	assertEqual(t, scan(durable.TaskStatusTerminal), []durable.TaskRecord{terminal})
}

func conformanceRequestIDs(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	secondConversationID := mustMint(t, storage)
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(secondConversationID)}})
	firstID := mustMint(t, storage)
	secondID := mustMint(t, storage)
	otherConversationID := mustMint(t, storage)
	first := durable.SubmissionRecord{ID: durable.SubmissionID(firstID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("same"), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}
	second := durable.SubmissionRecord{ID: durable.SubmissionID(secondID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("other"), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}
	otherConversation := durable.SubmissionRecord{ID: durable.SubmissionID(otherConversationID), ConversationID: durable.ConversationID(secondConversationID), RequestID: ptr("same"), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &first},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &second},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &otherConversation},
	)
	byRequest, err := storage.SubmissionByRequest(ctx, durable.ConversationID(rootID), "same")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, byRequest, &first)
	byRequest, err = storage.SubmissionByRequest(ctx, durable.ConversationID(secondConversationID), "same")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, byRequest, &otherConversation)

	placedSecond := second
	placedSecond.Status = durable.SubmissionStatusPlaced
	placedSecond.Entry = ptr(durable.EntryID(mustMint(t, storage)))
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteSubmission, Submission: &placedSecond})
	stored, err := storage.Submission(ctx, durable.SubmissionID(secondID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, stored, &placedSecond)
	byRequest, err = storage.SubmissionByRequest(ctx, durable.ConversationID(rootID), "other")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, byRequest, &placedSecond)

	ids := func(query durable.SubmissionQuery) []durable.ID {
		found := []durable.ID{}
		var cursor durable.Cursor
		for {
			page, err := storage.ScanSubmissions(ctx, query, 1, cursor)
			if err != nil {
				t.Fatal(err)
			}
			for _, submission := range page.Items {
				found = append(found, submission.ID)
			}
			if len(page.Next) == 0 {
				break
			}
			cursor = page.Next
		}
		return found
	}
	assertEqual(t, ids(durable.SubmissionQuery{}), []durable.ID{firstID, secondID, otherConversationID})
	assertEqual(t, ids(durable.SubmissionQuery{ConversationID: ptr(durable.ConversationID(rootID))}), []durable.ID{firstID, secondID})
	assertEqual(t, ids(durable.SubmissionQuery{Status: durable.SubmissionStatusQueued}), []durable.ID{firstID, otherConversationID})
	assertEqual(t, ids(durable.SubmissionQuery{Status: durable.SubmissionStatusPlaced}), []durable.ID{secondID})
	assertEqual(t, ids(durable.SubmissionQuery{ConversationID: ptr(durable.ConversationID(secondConversationID)), Status: durable.SubmissionStatusQueued}), []durable.ID{otherConversationID})
	assertEqual(t, ids(durable.SubmissionQuery{ConversationID: ptr(durable.ConversationID(secondConversationID)), Status: durable.SubmissionStatusPlaced}), []durable.ID{})
	placedPage, err := storage.ScanSubmissions(ctx, durable.SubmissionQuery{Status: durable.SubmissionStatusPlaced}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, placedPage.Items, []durable.SubmissionRecord{placedSecond})
}

func conformancePassiveWrites(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	doneID := mustMint(t, storage)
	failedID := mustMint(t, storage)
	queuedDone := durable.SubmissionRecord{ID: durable.SubmissionID(doneID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("passive-done"), Type: durable.SubmissionTypeWrite, Status: durable.SubmissionStatusQueued}
	queuedFailed := durable.SubmissionRecord{ID: durable.SubmissionID(failedID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("passive-failed"), Type: durable.SubmissionTypeWrite, Status: durable.SubmissionStatusQueued}
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &queuedDone},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &queuedFailed},
	)
	done := queuedDone
	done.Status = durable.SubmissionStatusDone
	done.Entry = ptr(durable.EntryID(mustMint(t, storage)))
	unanswered := queuedFailed
	unanswered.Status = durable.SubmissionStatusUnanswered
	unanswered.Reason = "closed"
	unanswered.Detail = json.RawMessage(`{"retryable":false}`)
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &done},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &unanswered},
	)
	storedDone, err := storage.Submission(ctx, durable.SubmissionID(doneID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedDone, &done)
	byRequest, err := storage.SubmissionByRequest(ctx, durable.ConversationID(rootID), "passive-done")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, byRequest, &done)
	storedUnanswered, err := storage.Submission(ctx, durable.SubmissionID(failedID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedUnanswered, &unanswered)
	byRequest, err = storage.SubmissionByRequest(ctx, durable.ConversationID(rootID), "passive-failed")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, byRequest, &unanswered)
}

func conformanceRewindableDocuments(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	firstID := mustMint(t, storage)
	scope := durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}
	firstRecord := durable.DocumentCreate{ID: durable.DocumentID(firstID), Kind: "conversation.notes", Scope: scope, History: durable.HistoryRewindable, Fork: durable.ForkAsOf}
	initial := durable.JsonObject{"items": []any{"a"}, "nested": durable.JsonObject{"count": 1}}
	createdAt := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &firstRecord, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: initial}})
	ops := []chord.Op{
		{"p", []any{"items"}, 1, 0, []any{"b"}},
		{"s", []any{"nested", "count"}, 2},
	}
	changedAt := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: ops}})

	initial["items"] = append(initial["items"].([]any), "caller mutation")
	atCreate, err := storage.Document(ctx, durable.DocumentID(firstID), durable.AtSeq(createdAt))
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, atCreate, map[string]any{"version": 1, "value": map[string]any{"items": []any{"a"}, "nested": map[string]any{"count": 1}}, "deltasSinceBase": 0})
	changed, err := storage.Document(ctx, durable.DocumentID(firstID), durable.AtSeq(changedAt))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, changed.Value, durable.JsonObject{"items": []any{"a", "b"}, "nested": durable.JsonObject{"count": 2}})
	if changed.DeltasSinceBase != 1 {
		t.Fatalf("deltas since base = %d", changed.DeltasSinceBase)
	}
	changed.Value["items"] = append(changed.Value["items"].([]any), "read mutation")
	current, err := storage.Document(ctx, durable.DocumentID(firstID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, current.Value, durable.JsonObject{"items": []any{"a", "b"}, "nested": durable.JsonObject{"count": 2}})

	checkpointAt := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 2, Value: durable.JsonObject{"items": []any{"checkpoint"}, "nested": durable.JsonObject{"count": 3}}}})
	replacedAt := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 2, Ops: []chord.Op{{"r", durable.JsonObject{"items": []any{"replacement"}, "nested": durable.JsonObject{"count": 4}}}}}})
	historical, err := storage.Document(ctx, durable.DocumentID(firstID), durable.AtSeq(changedAt))
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, historical, map[string]any{"version": 1, "value": map[string]any{"items": []any{"a", "b"}, "nested": map[string]any{"count": 2}}})
	atCheckpoint, err := storage.Document(ctx, durable.DocumentID(firstID), durable.AtSeq(checkpointAt))
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, atCheckpoint, map[string]any{"version": 2, "value": map[string]any{"items": []any{"checkpoint"}, "nested": map[string]any{"count": 3}}, "deltasSinceBase": 0})
	atReplaced, err := storage.Document(ctx, durable.DocumentID(firstID), durable.AtSeq(replacedAt))
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, atReplaced, map[string]any{"value": map[string]any{"items": []any{"replacement"}, "nested": map[string]any{"count": 4}}, "deltasSinceBase": 1})
	currentNow, err := storage.Document(ctx, durable.DocumentID(firstID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if currentNow.DeltasSinceBase != 1 {
		t.Fatalf("current deltas since base = %d", currentNow.DeltasSinceBase)
	}

	secondID := mustMint(t, storage)
	secondRecord := firstRecord
	secondRecord.ID = durable.DocumentID(secondID)
	retiredAt := mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &secondRecord, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"items": []any{"new"}}}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(firstID)},
		durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 2, Ops: []chord.Op{{"s", []any{"retiring"}, true}}}},
	)
	address := durable.DocumentAddress{Kind: firstRecord.Kind, Scope: firstRecord.Scope}
	found, err := storage.FindDocument(ctx, address, durable.AtSeq(changedAt))
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != durable.DocumentID(firstID) {
		t.Fatalf("historic address = %+v", found)
	}
	found, err = storage.FindDocument(ctx, address, durable.AtSeq(retiredAt))
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, found, map[string]any{"id": secondID, "createdAt": retiredAt})
	docsAtChanged, err := storage.ScanDocuments(ctx, durable.DocumentQuery{Scope: firstRecord.Scope, At: durable.AtSeq(changedAt)}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, documentIDs(docsAtChanged.Items), []durable.ID{firstID})
	docsAtRetired, err := storage.ScanDocuments(ctx, durable.DocumentQuery{Scope: firstRecord.Scope, At: durable.AtSeq(retiredAt)}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, documentIDs(docsAtRetired.Items), []durable.ID{secondID})
	retired, err := storage.Document(ctx, durable.DocumentID(firstID), durable.AtSeq(retiredAt))
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, retired)
	newValue, err := storage.Document(ctx, durable.DocumentID(secondID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, newValue.Value, durable.JsonObject{"items": []any{"new"}})
}

func documentIDs(records []durable.DocumentRecord) []durable.ID {
	out := make([]durable.ID, len(records))
	for index, record := range records {
		out[index] = record.ID
	}
	return out
}

func conformanceLongTails(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	id := mustMint(t, storage)
	record := durable.DocumentCreate{ID: durable.DocumentID(id), Kind: "conversation.long-tail", Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryRewindable, Fork: durable.ForkAsOf}
	rows := make([]any, 512)
	for index := range rows {
		rows[index] = durable.JsonObject{"value": index, "stable": fmt.Sprintf("row-%d", index)}
	}
	initial := durable.JsonObject{"revision": 0, "rows": rows}
	createdAt := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: initial}})

	beforeReplacement := durable.JsonObject{"revision": 0, "rows": cloneRows(t, rows)}
	beforeReplacementAt := createdAt
	for revision := 1; revision <= 24; revision++ {
		index := (revision * 17) % 512
		beforeReplacement["rows"].([]any)[index].(durable.JsonObject)["value"] = -revision
		beforeReplacement["revision"] = revision
		beforeReplacementAt = mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{
			{"s", []any{"rows", index, "value"}, -revision},
			{"s", []any{"revision"}, revision},
		}}})
	}

	newRows := make([]any, 512)
	for index := range newRows {
		newRows[index] = durable.JsonObject{"value": 10000 + index, "stable": fmt.Sprintf("new-%d", index)}
	}
	replacement := durable.JsonObject{"revision": 100, "rows": newRows}
	replacementSnapshot := durable.JsonObject{"revision": 100, "rows": cloneRows(t, newRows)}
	replacementAt := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"r", replacement}}}})
	replacement["rows"].([]any)[0].(durable.JsonObject)["value"] = -999

	current := cloneObject(t, replacementSnapshot)
	for revision := 101; revision <= 124; revision++ {
		index := (revision * 19) % 512
		current["rows"].([]any)[index].(durable.JsonObject)["value"] = -revision
		current["revision"] = revision
		mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{
			{"s", []any{"rows", index, "value"}, -revision},
			{"s", []any{"revision"}, revision},
		}}})
	}

	atCreate, err := storage.Document(ctx, durable.DocumentID(id), durable.AtSeq(createdAt))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, atCreate.Value, initial)
	atBefore, err := storage.Document(ctx, durable.DocumentID(id), durable.AtSeq(beforeReplacementAt))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, atBefore.Value, beforeReplacement)
	atReplacement, err := storage.Document(ctx, durable.DocumentID(id), durable.AtSeq(replacementAt))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, atReplacement.Value, replacementSnapshot)
	read, err := storage.Document(ctx, durable.DocumentID(id), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, read.Value, current)
	read.Value["rows"].([]any)[0].(durable.JsonObject)["value"] = -1000
	again, err := storage.Document(ctx, durable.DocumentID(id), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, again.Value, current)
}

func cloneRows(t *testing.T, rows []any) []any {
	t.Helper()
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var out []any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func cloneObject(t *testing.T, value durable.JsonObject) durable.JsonObject {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out durable.JsonObject
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func conformanceDocumentCopies(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	childID := mustMint(t, storage)
	secondChildID := mustMint(t, storage)
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(childID)}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(secondChildID)}},
	)
	sourceID := mustMint(t, storage)
	sourceRecord := durable.DocumentCreate{ID: durable.DocumentID(sourceID), Kind: "copy.source", Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryRewindable, Fork: durable.ForkAsOf}
	createdAt := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &sourceRecord, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 2, Value: durable.JsonObject{"count": 1, "rows": []any{durable.JsonObject{"value": "base"}}}}})
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(sourceID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 2, Ops: []chord.Op{
		{"s", []any{"count"}, 2},
		{"p", []any{"rows"}, 1, 0, []any{durable.JsonObject{"value": "current"}}},
	}}})
	historicalCopyID := mustMint(t, storage)
	currentCopyID := mustMint(t, storage)
	retiredCopyID := mustMint(t, storage)
	childRecord := func(id, conversationID durable.ID) durable.DocumentCreate {
		return durable.DocumentCreate{ID: durable.DocumentID(id), Kind: sourceRecord.Kind, Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(conversationID)}, History: durable.HistoryRewindable, Fork: durable.ForkAsOf}
	}
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: ptr(childRecord(historicalCopyID, childID)), Source: &durable.DocumentCopySource{ID: durable.DocumentID(sourceID), At: durable.AtSeq(createdAt)}},
		durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: ptr(childRecord(currentCopyID, secondChildID)), Source: &durable.DocumentCopySource{ID: durable.DocumentID(sourceID), At: durable.CurrentDocument()}},
		durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: ptr(childRecord(retiredCopyID, rootID)), Source: &durable.DocumentCopySource{ID: durable.DocumentID(sourceID), At: durable.CurrentDocument()}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(retiredCopyID)},
	)
	historical, err := storage.Document(ctx, durable.DocumentID(historicalCopyID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, historical, map[string]any{"version": 2, "value": map[string]any{"count": 1, "rows": []any{map[string]any{"value": "base"}}}})
	currentCopy, err := storage.Document(ctx, durable.DocumentID(currentCopyID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, currentCopy, map[string]any{"version": 2, "value": map[string]any{"count": 2, "rows": []any{map[string]any{"value": "base"}, map[string]any{"value": "current"}}}})
	retiredCopy, err := storage.Document(ctx, durable.DocumentID(retiredCopyID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, retiredCopy)

	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(sourceID), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 2, Value: durable.JsonObject{"count": 99, "rows": []any{}}}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(sourceID)},
	)
	copied, err := storage.Document(ctx, durable.DocumentID(currentCopyID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, copied.Value, durable.JsonObject{"count": 2, "rows": []any{durable.JsonObject{"value": "base"}, durable.JsonObject{"value": "current"}}})

	latestSourceID := mustMint(t, storage)
	latestCopyID := mustMint(t, storage)
	latestSource := durable.DocumentCreate{ID: durable.DocumentID(latestSourceID), Kind: "copy.latest", Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryLatest, Fork: durable.ForkCurrent}
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &latestSource, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 4, Value: durable.JsonObject{"retained": "copy"}}})
	latestCopy := latestSource
	latestCopy.ID = durable.DocumentID(latestCopyID)
	latestCopy.Scope = durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(childID)}
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: &latestCopy, Source: &durable.DocumentCopySource{ID: durable.DocumentID(latestSourceID), At: durable.CurrentDocument()}})
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(latestSourceID), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 4, Value: durable.JsonObject{"retained": "source-only"}}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(latestSourceID)},
	)
	latestCopyValue, err := storage.Document(ctx, durable.DocumentID(latestCopyID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, latestCopyValue, map[string]any{"version": 4, "value": map[string]any{"retained": "copy"}})

	// Ambiguous source: the source is retired in the same batch as the copy. The
	// Go contract (like upstream) rejects a changed source as a definite
	// StorageRejected and publishes nothing.
	conflictID := mustMint(t, storage)
	conflictRecord := childRecord(conflictID, childID)
	_, err = storage.Commit(ctx, []durable.StorageWrite{
		{Type: durable.WriteDocumentCopy, Record: &conflictRecord, Source: &durable.DocumentCopySource{ID: durable.DocumentID(currentCopyID), At: durable.CurrentDocument()}},
		{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(currentCopyID)},
	})
	assertContains(t, err, "changed in the copy batch")
	unpublished, err := storage.Document(ctx, durable.DocumentID(conflictID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, unpublished)
	preserved, err := storage.Document(ctx, durable.DocumentID(currentCopyID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, preserved.Value, durable.JsonObject{"count": 2, "rows": []any{durable.JsonObject{"value": "base"}, durable.JsonObject{"value": "current"}}})

	// Go adaptation: document copy is definition-free, so a different target
	// kind is legal and copies the stored base and version unchanged.
	defFreeID := mustMint(t, storage)
	defFree := childRecord(defFreeID, childID)
	defFree.Kind = "copy.mismatch"
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: &defFree, Source: &durable.DocumentCopySource{ID: durable.DocumentID(currentCopyID), At: durable.CurrentDocument()}})
	defFreeValue, err := storage.Document(ctx, durable.DocumentID(defFreeID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, defFreeValue, map[string]any{"version": 2, "value": map[string]any{"count": 2}})
}

func conformanceVersionTransitions(t *testing.T) {
	storage := newConformanceStorage(t)
	createRoot(t, storage)
	id := mustMint(t, storage)
	record := durable.DocumentCreate{ID: durable.DocumentID(id), Kind: "session.settings", Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 1}}})
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 2}}}})
	migratedAt := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 2, Value: durable.JsonObject{"count": 3}}})
	current, err := storage.Document(ctx, durable.DocumentID(id), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertPartial(t, current, map[string]any{"version": 2, "value": map[string]any{"count": 3}})
	if _, err := storage.Document(ctx, durable.DocumentID(id), durable.AtSeq(migratedAt)); err == nil {
		t.Fatal("historical read of a current-only document was accepted")
	}
	_, err = storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 4}}}}})
	assertContains(t, err, "version transition requires a base")
	current, err = storage.Document(ctx, durable.DocumentID(id), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, current.Value, durable.JsonObject{"count": 3})
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(id)})
	retired, err := storage.Document(ctx, durable.DocumentID(id), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, retired)
}

func conformanceAddressIndexes(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	firstID := mustMint(t, storage)
	secondID := mustMint(t, storage)
	conversationDocumentID := mustMint(t, storage)
	taskID := mustMint(t, storage)
	taskSingletonID := mustMint(t, storage)
	taskFamilyID := mustMint(t, storage)
	taskOtherKindID := mustMint(t, storage)
	createdAt := mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteTask, Task: ptr(pendingTask(taskID, rootID, "ready"))},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(firstID), Kind: "cache", Scope: durable.DocumentScope{Kind: durable.ScopeSession}, Key: ptr("__proto__")}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "first"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(secondID), Kind: "cache", Scope: durable.DocumentScope{Kind: durable.ScopeSession}, Key: ptr("constructor")}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "second"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(conversationDocumentID), Kind: "cache", Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryLatest, Fork: durable.ForkCurrent, Key: ptr("__proto__")}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "conversation"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(taskSingletonID), Kind: "task.cache", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "singleton"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(taskFamilyID), Kind: "task.cache", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}, Key: ptr("member")}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "family"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(taskOtherKindID), Kind: "task.other", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "other"}}},
	)

	found, err := storage.FindDocument(ctx, durable.DocumentAddress{Kind: "cache", Scope: durable.DocumentScope{Kind: durable.ScopeSession}, Key: ptr("__proto__")}, durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != durable.DocumentID(firstID) {
		t.Fatalf("session family member = %+v", found)
	}
	first, err := storage.ScanDocuments(ctx, durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeSession}, At: durable.CurrentDocument()}, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 {
		t.Fatalf("first session page = %+v", first.Items)
	}
	second, err := storage.ScanDocuments(ctx, durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeSession}, At: durable.CurrentDocument()}, 1, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	combined := append(append([]durable.DocumentRecord{}, first.Items...), second.Items...)
	assertEqual(t, documentIDs(combined), []durable.ID{firstID, secondID})
	conversationDocs, err := storage.ScanDocuments(ctx, durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, At: durable.CurrentDocument()}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, documentIDs(conversationDocs.Items), []durable.ID{conversationDocumentID})
	found, err = storage.FindDocument(ctx, durable.DocumentAddress{Kind: "task.cache", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}}, durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != durable.DocumentID(taskSingletonID) {
		t.Fatalf("task singleton = %+v", found)
	}
	found, err = storage.FindDocument(ctx, durable.DocumentAddress{Kind: "task.cache", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}, Key: ptr("member")}, durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if found == nil || found.ID != durable.DocumentID(taskFamilyID) {
		t.Fatalf("task family = %+v", found)
	}
	taskDocs, err := storage.ScanDocuments(ctx, durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}, At: durable.CurrentDocument(), Kind: "task.cache"}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, documentIDs(taskDocs.Items), []durable.ID{taskSingletonID, taskFamilyID})
	if _, err := storage.Document(ctx, durable.DocumentID(taskSingletonID), durable.AtSeq(createdAt)); err == nil {
		t.Fatal("historical read of a current-only task document was accepted")
	}
}

func conformanceDocumentLifecycle(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	firstID := mustMint(t, storage)
	secondID := mustMint(t, storage)
	record := durable.DocumentCreate{ID: durable.DocumentID(firstID), Kind: "singleton", Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"value": 1}}})
	_, err := storage.Commit(ctx, []durable.StorageWrite{
		{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(secondID), Kind: record.Kind, Scope: record.Scope}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"value": 2}}},
		{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{}}},
	})
	assertContains(t, err, "already has a current incarnation")
	first, err := storage.Document(ctx, durable.DocumentID(firstID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, first.Value, durable.JsonObject{"value": 1})
	second, err := storage.Document(ctx, durable.DocumentID(secondID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, second)

	emptyID := mustMint(t, storage)
	emptyAt := mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(emptyID), Kind: record.Kind, Key: ptr("empty"), Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryRewindable, Fork: durable.ForkInitial}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{}}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(emptyID)},
	)
	empty, err := storage.Document(ctx, durable.DocumentID(emptyID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, empty)
	empty, err = storage.Document(ctx, durable.DocumentID(emptyID), durable.AtSeq(emptyAt))
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, empty)
	found, err := storage.FindDocument(ctx, durable.DocumentAddress{Kind: record.Kind, Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, Key: ptr("empty")}, durable.AtSeq(emptyAt))
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, found)
}

func conformanceDocumentRollback(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	taskID := mustMint(t, storage)
	submissionID := mustMint(t, storage)
	documentID := mustMint(t, storage)
	task := pendingTask(taskID, rootID, "ready")
	submission := durable.SubmissionRecord{ID: durable.SubmissionID(submissionID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("atomic"), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}
	record := durable.DocumentCreate{ID: durable.DocumentID(documentID), Kind: "atomic", Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
	baselineSeq := mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteTask, Task: &task},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &submission},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 1}}},
	)

	entryID := mustMint(t, storage)
	conflictingDocumentID := mustMint(t, storage)
	running := task
	running.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: json.RawMessage(`{"phase":"effect"}`)}
	unanswered := submission
	unanswered.Status = durable.SubmissionStatusUnanswered
	unanswered.Reason = "failed"
	_, err := storage.Commit(ctx, []durable.StorageWrite{
		{Type: durable.WriteTask, Task: &running},
		{Type: durable.WriteSubmission, Submission: &unanswered},
		{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "transient"}},
		{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(conflictingDocumentID), Kind: record.Kind, Scope: record.Scope}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 2}}},
	})
	assertContains(t, err, "already has a current incarnation")
	storedTask, err := storage.Task(ctx, durable.TaskID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, storedTask, &task)
	pendingPage, err := storage.ScanTasks(ctx, durable.TaskQuery{Status: durable.TaskStatusPending}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, pendingPage.Items, []durable.TaskRecord{task})
	byRequest, err := storage.SubmissionByRequest(ctx, durable.ConversationID(rootID), "atomic")
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, byRequest, &submission)
	transient, err := storage.Entry(ctx, durable.EntryID(entryID))
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, transient)
	conflicting, err := storage.Document(ctx, durable.DocumentID(conflictingDocumentID), durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	assertNil(t, conflicting)
	existing, err := storage.FindDocument(ctx, durable.DocumentAddress{Kind: record.Kind, Scope: record.Scope}, durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if existing == nil || existing.ID != durable.DocumentID(documentID) {
		t.Fatalf("existing document = %+v", existing)
	}
	afterRollbackSeq := mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(documentID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 3}}}})
	if afterRollbackSeq <= baselineSeq {
		t.Fatalf("sequence after rollback %d <= %d", afterRollbackSeq, baselineSeq)
	}
}

// conformanceStringIdentity ports the upstream "keeps indexed string identities
// lossless" case with a documented Go adaptation: isolated UTF-16 surrogates are
// not representable as valid Go strings, so distinct supplementary code points
// exercise the same multi-byte identity boundary.
func conformanceStringIdentity(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	first := "\U0001F600"
	second := "\U0001F601"
	firstTaskID := mustMint(t, storage)
	secondTaskID := mustMint(t, storage)
	firstSubmissionID := mustMint(t, storage)
	secondSubmissionID := mustMint(t, storage)
	firstKindDocumentID := mustMint(t, storage)
	secondKindDocumentID := mustMint(t, storage)
	firstKeyDocumentID := mustMint(t, storage)
	secondKeyDocumentID := mustMint(t, storage)
	firstTask := pendingTask(firstTaskID, rootID, "ready")
	firstTask.Kind = first
	secondTask := pendingTask(secondTaskID, rootID, "ready")
	secondTask.Kind = second
	mustCommit(t, storage,
		durable.StorageWrite{Type: durable.WriteTask, Task: &firstTask},
		durable.StorageWrite{Type: durable.WriteTask, Task: &secondTask},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &durable.SubmissionRecord{ID: durable.SubmissionID(firstSubmissionID), ConversationID: durable.ConversationID(rootID), RequestID: ptr(first), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &durable.SubmissionRecord{ID: durable.SubmissionID(secondSubmissionID), ConversationID: durable.ConversationID(rootID), RequestID: ptr(second), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(firstKindDocumentID), Kind: first, Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"identity": "first kind"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(secondKindDocumentID), Kind: second, Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"identity": "second kind"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(firstKeyDocumentID), Kind: "family", Key: ptr(first), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"identity": "first key"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(secondKeyDocumentID), Kind: "family", Key: ptr(second), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"identity": "second key"}}},
	)

	firstKindTasks, err := storage.ScanTasks(ctx, durable.TaskQuery{Kind: first}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, taskIDs(firstKindTasks.Items), []durable.ID{firstTaskID})
	secondKindTasks, err := storage.ScanTasks(ctx, durable.TaskQuery{Kind: second}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, taskIDs(secondKindTasks.Items), []durable.ID{secondTaskID})
	storedFirstTask, err := storage.Task(ctx, durable.TaskID(firstTaskID))
	if err != nil {
		t.Fatal(err)
	}
	if storedFirstTask.Kind != first {
		t.Fatalf("first task kind = %q", storedFirstTask.Kind)
	}
	storedSecondTask, err := storage.Task(ctx, durable.TaskID(secondTaskID))
	if err != nil {
		t.Fatal(err)
	}
	if storedSecondTask.Kind != second {
		t.Fatalf("second task kind = %q", storedSecondTask.Kind)
	}
	firstRequest, err := storage.SubmissionByRequest(ctx, durable.ConversationID(rootID), first)
	if err != nil {
		t.Fatal(err)
	}
	if firstRequest == nil || firstRequest.ID != durable.SubmissionID(firstSubmissionID) {
		t.Fatalf("first request = %+v", firstRequest)
	}
	secondRequest, err := storage.SubmissionByRequest(ctx, durable.ConversationID(rootID), second)
	if err != nil {
		t.Fatal(err)
	}
	if secondRequest == nil || secondRequest.ID != durable.SubmissionID(secondSubmissionID) {
		t.Fatalf("second request = %+v", secondRequest)
	}
	firstKind, err := storage.FindDocument(ctx, durable.DocumentAddress{Kind: first, Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if firstKind == nil || firstKind.ID != durable.DocumentID(firstKindDocumentID) {
		t.Fatalf("first kind document = %+v", firstKind)
	}
	secondKind, err := storage.FindDocument(ctx, durable.DocumentAddress{Kind: second, Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if secondKind == nil || secondKind.ID != durable.DocumentID(secondKindDocumentID) {
		t.Fatalf("second kind document = %+v", secondKind)
	}
	firstKey, err := storage.FindDocument(ctx, durable.DocumentAddress{Kind: "family", Key: ptr(first), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if firstKey == nil || firstKey.ID != durable.DocumentID(firstKeyDocumentID) {
		t.Fatalf("first key document = %+v", firstKey)
	}
	secondKey, err := storage.FindDocument(ctx, durable.DocumentAddress{Kind: "family", Key: ptr(second), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if secondKey == nil || secondKey.ID != durable.DocumentID(secondKeyDocumentID) {
		t.Fatalf("second key document = %+v", secondKey)
	}
	firstKindScan, err := storage.ScanDocuments(ctx, durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeSession}, At: durable.CurrentDocument(), Kind: first}, 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertEqual(t, documentIDs(firstKindScan.Items), []durable.ID{firstKindDocumentID})
}

func conformanceGlobalNamespace(t *testing.T) {
	storage := newConformanceStorage(t)
	rootID := createRoot(t, storage)
	explicitEntryID := durable.ID(100)
	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(explicitEntryID), ConversationID: durable.ConversationID(rootID)}})
	if id := mustMint(t, storage); id != 101 {
		t.Fatalf("minted ID = %d", id)
	}
	collision := pendingTask(explicitEntryID, rootID, "ready")
	_, err := storage.Commit(ctx, []durable.StorageWrite{{Type: durable.WriteTask, Task: &collision}})
	assertContains(t, err, fmt.Sprintf("ID %d already belongs to entry", explicitEntryID))

	mustCommit(t, storage, durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(durable.MaxSafeInteger), ConversationID: durable.ConversationID(rootID), Kind: "last-id"}})
	if _, err := storage.MintID(ctx); err == nil {
		t.Fatal("minting after the ID space is exhausted was accepted")
	}
	if _, err := storage.MintID(ctx); err == nil {
		t.Fatal("minting after the ID space is exhausted was accepted twice")
	}
}

func conformanceAfterClose(t *testing.T) {
	storage := memory.New()
	createRoot(t, storage)
	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Conversation(ctx, durable.RootConversationID); err == nil {
		t.Fatal("read after close was accepted")
	}
	if _, err := storage.Commit(ctx, []durable.StorageWrite{}); err == nil {
		t.Fatal("commit after close was accepted")
	}
	if _, err := storage.MintID(ctx); err == nil {
		t.Fatal("mint after close was accepted")
	}
}
