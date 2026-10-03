package durabletesting

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
)

// CreateStorageConformance ports createStorageConformance from
// packages/durable/src/testing/storage-conformance.ts. Every case name matches
// the source, exercises real storage assertions, and never fabricates success:
// a case returns a descriptive error when the adapter diverges.
//
// The provider is called exactly once per case with a fresh adapter.
func CreateStorageConformance(provider StorageProvider) []StorageConformanceCase {
	definitions := []struct {
		name string
		test func(*conformanceRun)
	}{
		{"reserves ID 1 for the immutable root conversation", caseReservesRoot},
		{"commits mixed table writes atomically and rolls all of them back on failure", caseMixedAtomicity},
		{"detaches retained writes and every returned record", caseDetachesWrites},
		{"detaches prototype-like JSON keys without changing object prototypes", casePrototypeKeys},
		{"indexes entries committed out of ID order", caseIndexesOutOfOrder},
		{"continues an entry cursor below its last item after a newer commit", caseEntryCursor},
		{"paginates conversations by opaque cursor in ascending ID order", caseConversationCursor},
		{"filters and pages conversations by durable owner edges", caseConversationOwners},
		{"scans deep fork history newest-first through every ancestor cap", caseDeepFork},
		{"replaces complete task records and pages filtered task scans", caseTaskReplacement},
		{"stores owners and scans waiting and completing tasks by status", caseTaskStatuses},
		{"indexes request IDs per conversation and replaces complete submission records", caseRequestIDs},
		{"stores passive write submissions without input-only lifecycle states", casePassiveWrites},
		{"reconstructs rewindable documents and preserves half-open incarnations", caseRewindableDocuments},
		{"streams long document tails across root replacement deltas", caseLongTails},
		{"copies stored document bases independently and rejects ambiguous sources", caseDocumentCopies},
		{"uses bases for version transitions and rejects historical reads of current-only documents", caseVersionTransitions},
		{"indexes logical addresses and exact-scope scans independently", caseAddressIndexes},
		{"keeps document lifecycle failures atomic and gives create-plus-retire an empty lifetime", caseDocumentLifecycle},
		{"rolls back record tables and secondary indexes when a document command fails", caseDocumentRollback},
		{"keeps indexed string identities lossless", caseStringIdentity},
		{"keeps one global record ID namespace and rejects exhausted ID minting", caseGlobalNamespace},
		{"rejects every operation after close", caseAfterClose},
	}
	cases := make([]StorageConformanceCase, 0, len(definitions))
	for _, definition := range definitions {
		definition := definition
		cases = append(cases, StorageConformanceCase{
			Name: definition.name,
			Run: func(ctx context.Context) error {
				return provider(ctx, func(storage durable.Storage) error {
					run := &conformanceRun{ctx: ctx, storage: storage, assert: NativeAssertions()}
					definition.test(run)
					return run.err
				})
			},
		})
	}
	return cases
}

// conformanceRun is a sticky-error case executor. Storage helpers record the
// first failure and short-circuit, so case bodies read close to the source
// while returning descriptive errors rather than panicking.
type conformanceRun struct {
	ctx     context.Context
	storage durable.Storage
	assert  StorageConformanceAssertions
	err     error
}

func (r *conformanceRun) fail(format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf(format, args...)
	}
}

// --- operations -------------------------------------------------------------

func (r *conformanceRun) mint() durable.ID {
	if r.err != nil {
		return 0
	}
	id, err := r.storage.MintID(r.ctx)
	if err != nil {
		r.fail("mint ID: %v", err)
	}
	return id
}

func (r *conformanceRun) commit(writes ...durable.StorageWrite) durable.Seq {
	if r.err != nil {
		return 0
	}
	seq, err := r.storage.Commit(r.ctx, writes)
	if err != nil {
		r.fail("commit: %v", err)
	}
	return seq
}

// expectError runs fn and asserts it fails with a message containing contains.
// Pass an empty contains to accept any error.
func (r *conformanceRun) expectError(contains string, fn func() error) {
	if r.err != nil {
		return
	}
	err := fn()
	if err == nil {
		if contains == "" {
			r.fail("expected an error but none was returned")
		} else {
			r.fail("expected an error containing %q but none was returned", contains)
		}
		return
	}
	if contains != "" && !containsString(err.Error(), contains) {
		r.fail("error %q does not contain %q", err.Error(), contains)
	}
}

func (r *conformanceRun) createRoot() durable.ConversationID {
	r.commit(durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}})
	return durable.RootConversationID
}

func (r *conformanceRun) conversation(id durable.ConversationID) *durable.ConversationRecord {
	if r.err != nil {
		return nil
	}
	record, err := r.storage.Conversation(r.ctx, id)
	if err != nil {
		r.fail("conversation %d: %v", id, err)
	}
	return record
}

func (r *conformanceRun) entry(id durable.EntryID) *durable.StoredEntry {
	if r.err != nil {
		return nil
	}
	record, err := r.storage.Entry(r.ctx, id)
	if err != nil {
		r.fail("entry %d: %v", id, err)
	}
	return record
}

func (r *conformanceRun) task(id durable.TaskID) *durable.TaskRecord {
	if r.err != nil {
		return nil
	}
	record, err := r.storage.Task(r.ctx, id)
	if err != nil {
		r.fail("task %d: %v", id, err)
	}
	return record
}

func (r *conformanceRun) submission(id durable.SubmissionID) *durable.SubmissionRecord {
	if r.err != nil {
		return nil
	}
	record, err := r.storage.Submission(r.ctx, id)
	if err != nil {
		r.fail("submission %d: %v", id, err)
	}
	return record
}

func (r *conformanceRun) submissionByRequest(id durable.ConversationID, key string) *durable.SubmissionRecord {
	if r.err != nil {
		return nil
	}
	record, err := r.storage.SubmissionByRequest(r.ctx, id, key)
	if err != nil {
		r.fail("submission by request %q: %v", key, err)
	}
	return record
}

func (r *conformanceRun) document(id durable.DocumentID, point durable.DocumentPoint) *durable.StoredDocument {
	if r.err != nil {
		return nil
	}
	record, err := r.storage.Document(r.ctx, id, point)
	if err != nil {
		r.fail("document %d: %v", id, err)
	}
	return record
}

func (r *conformanceRun) findDocument(address durable.DocumentAddress, point durable.DocumentPoint) *durable.DocumentRecord {
	if r.err != nil {
		return nil
	}
	record, err := r.storage.FindDocument(r.ctx, address, point)
	if err != nil {
		r.fail("find document: %v", err)
	}
	return record
}

func (r *conformanceRun) headMarker(id durable.ConversationID, at *durable.EntryID) *durable.EntryRecord {
	if r.err != nil {
		return nil
	}
	record, err := r.storage.FindLatestHeadMarker(r.ctx, id, at)
	if err != nil {
		r.fail("head marker: %v", err)
	}
	return record
}

func (r *conformanceRun) scanEntries(query durable.EntryQuery, limit int, cursor durable.Cursor) durable.Page[durable.EntryRecord] {
	if r.err != nil {
		return durable.Page[durable.EntryRecord]{}
	}
	page, err := r.storage.ScanEntries(r.ctx, query, limit, cursor)
	if err != nil {
		r.fail("scan entries: %v", err)
	}
	return page
}

func (r *conformanceRun) scanConversations(query durable.ConversationQuery, limit int, cursor durable.Cursor) durable.Page[durable.ConversationRecord] {
	if r.err != nil {
		return durable.Page[durable.ConversationRecord]{}
	}
	page, err := r.storage.ScanConversations(r.ctx, query, limit, cursor)
	if err != nil {
		r.fail("scan conversations: %v", err)
	}
	return page
}

func (r *conformanceRun) scanTasks(query durable.TaskQuery, limit int, cursor durable.Cursor) durable.Page[durable.TaskRecord] {
	if r.err != nil {
		return durable.Page[durable.TaskRecord]{}
	}
	page, err := r.storage.ScanTasks(r.ctx, query, limit, cursor)
	if err != nil {
		r.fail("scan tasks: %v", err)
	}
	return page
}

func (r *conformanceRun) scanSubmissions(query durable.SubmissionQuery, limit int, cursor durable.Cursor) durable.Page[durable.SubmissionRecord] {
	if r.err != nil {
		return durable.Page[durable.SubmissionRecord]{}
	}
	page, err := r.storage.ScanSubmissions(r.ctx, query, limit, cursor)
	if err != nil {
		r.fail("scan submissions: %v", err)
	}
	return page
}

func (r *conformanceRun) scanDocuments(query durable.DocumentQuery, limit int, cursor durable.Cursor) durable.Page[durable.DocumentRecord] {
	if r.err != nil {
		return durable.Page[durable.DocumentRecord]{}
	}
	page, err := r.storage.ScanDocuments(r.ctx, query, limit, cursor)
	if err != nil {
		r.fail("scan documents: %v", err)
	}
	return page
}

// --- assertions -------------------------------------------------------------

func (r *conformanceRun) equal(actual, expected any) {
	if r.err != nil {
		return
	}
	if err := r.assert.DeepEqual(actual, expected); err != nil {
		r.err = err
	}
}

func (r *conformanceRun) partial(actual, expected any) {
	if r.err != nil {
		return
	}
	if err := r.assert.PartialDeepEqual(actual, expected); err != nil {
		r.err = err
	}
}

func (r *conformanceRun) nilValue(value any) {
	if r.err != nil {
		return
	}
	if !isNilValue(value) {
		r.fail("expected nil, got %s", compactJSON(value))
	}
}

func (r *conformanceRun) greaterThan(actual, expected int64) {
	if r.err != nil {
		return
	}
	if err := r.assert.GreaterThan(actual, expected); err != nil {
		r.err = err
	}
}

func (r *conformanceRun) ids(actual []durable.ID, expected ...durable.ID) {
	if r.err != nil {
		return
	}
	if len(actual) != len(expected) {
		r.fail("ids = %v want %v", actual, expected)
		return
	}
	for index := range expected {
		if actual[index] != expected[index] {
			r.fail("ids = %v want %v", actual, expected)
			return
		}
	}
}

func (r *conformanceRun) rawJSON(actual json.RawMessage, expected string) {
	if r.err != nil {
		return
	}
	if err := r.assert.DeepEqual(actual, json.RawMessage(expected)); err != nil {
		r.err = err
	}
}

// --- fixtures ---------------------------------------------------------------

func ptr[T any](value T) *T { return &value }

func pendingTaskRecord(id, conversation durable.ID, phase string) durable.TaskRecord {
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

func entryIDs(entries []durable.EntryRecord) []durable.ID {
	out := make([]durable.ID, len(entries))
	for index, entry := range entries {
		out[index] = entry.ID
	}
	return out
}

func conversationIDs(records []durable.ConversationRecord) []durable.ID {
	out := make([]durable.ID, len(records))
	for index, record := range records {
		out[index] = record.ID
	}
	return out
}

func taskIDs(tasks []durable.TaskRecord) []durable.ID {
	out := make([]durable.ID, len(tasks))
	for index, task := range tasks {
		out[index] = task.ID
	}
	return out
}

func documentIDs(records []durable.DocumentRecord) []durable.ID {
	out := make([]durable.ID, len(records))
	for index, record := range records {
		out[index] = record.ID
	}
	return out
}

func cloneRows(rows []any) ([]any, error) {
	data, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	var out []any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneObject(value durable.JsonObject) (durable.JsonObject, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out durable.JsonObject
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func containsString(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

// --- cases ------------------------------------------------------------------

func caseReservesRoot(r *conformanceRun) {
	if id := r.mint(); id != 2 {
		r.fail("first minted ID = %d want 2", id)
	}
	if root := r.createRoot(); root != durable.RootConversationID {
		r.fail("root id = %d", root)
	}
	r.equal(r.conversation(durable.RootConversationID), &durable.ConversationRecord{ID: durable.RootConversationID})
	r.expectError(fmt.Sprintf("ID %d already belongs to conversation", durable.RootConversationID), func() error {
		_, err := r.storage.Commit(r.ctx, []durable.StorageWrite{{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}}})
		return err
	})
}

func caseMixedAtomicity(r *conformanceRun) {
	rootID := r.createRoot()
	entryID := r.mint()
	taskID := r.mint()
	submissionID := r.mint()
	task := pendingTaskRecord(taskID, rootID, "ready")
	input := durable.SubmissionRecord{
		ID:             durable.SubmissionID(submissionID),
		ConversationID: durable.ConversationID(rootID),
		RequestID:      ptr("request-1"),
		Type:           durable.SubmissionTypeInput,
		Status:         durable.SubmissionStatusPlaced,
		Entry:          ptr(durable.EntryID(entryID)),
	}
	initialSeq := r.commit(
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "user", Data: json.RawMessage(`{"text":"hello"}`)}},
		durable.StorageWrite{Type: durable.WriteTask, Task: &task},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &input},
	)
	r.equal(r.entry(durable.EntryID(entryID)), &durable.StoredEntry{Entry: durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "user", Data: json.RawMessage(`{"text":"hello"}`)}, CommitSeq: initialSeq})
	r.equal(r.task(durable.TaskID(taskID)), &task)
	r.equal(r.submission(durable.SubmissionID(submissionID)), &input)

	transientEntryID := r.mint()
	runningTask := task
	runningTask.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: json.RawMessage(`{"phase":"effect"}`)}
	doneInput := input
	doneInput.Status = durable.SubmissionStatusDone
	doneInput.Answer = ptr(durable.EntryID(transientEntryID))
	r.expectError(fmt.Sprintf("ID %d already belongs to conversation", rootID), func() error {
		_, err := r.storage.Commit(r.ctx, []durable.StorageWrite{
			{Type: durable.WriteTask, Task: &runningTask},
			{Type: durable.WriteSubmission, Submission: &doneInput},
			{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(transientEntryID), ConversationID: durable.ConversationID(rootID), Kind: "assistant"}},
			{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(rootID)}},
		})
		return err
	})
	r.equal(r.task(durable.TaskID(taskID)), &task)
	r.equal(r.submission(durable.SubmissionID(submissionID)), &input)
	r.nilValue(r.entry(durable.EntryID(transientEntryID)))
	afterRollbackSeq := r.commit(durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(r.mint()), ConversationID: durable.ConversationID(rootID), Kind: "after-rollback"}})
	r.greaterThan(int64(afterRollbackSeq), int64(initialSeq))
}

func caseDetachesWrites(r *conformanceRun) {
	rootID := r.createRoot()
	entryID := r.mint()
	taskID := r.mint()
	submissionID := r.mint()

	entryData := json.RawMessage(`{"nested":[1,2]}`)
	checkpoint := json.RawMessage(`{"phase":"ready","nested":{"count":1}}`)
	detail := json.RawMessage(`{"codes":["initial"]}`)

	storedEntry := durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "note", Data: entryData}
	storedTask := pendingTaskRecord(taskID, rootID, "ready")
	storedTask.State.Checkpoint = checkpoint
	storedInput := durable.SubmissionRecord{ID: durable.SubmissionID(submissionID), ConversationID: durable.ConversationID(rootID), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusUnanswered, Reason: "failed", Detail: detail}
	r.commit(
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &storedEntry},
		durable.StorageWrite{Type: durable.WriteTask, Task: &storedTask},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &storedInput},
	)

	entryData[0] = '!'
	checkpoint[0] = '!'
	detail[0] = '!'

	readEntry := r.entry(durable.EntryID(entryID))
	if readEntry != nil {
		r.rawJSON(readEntry.Entry.Data, `{"nested":[1,2]}`)
	}
	readTask := r.task(durable.TaskID(taskID))
	if readTask != nil {
		r.rawJSON(readTask.State.Checkpoint, `{"phase":"ready","nested":{"count":1}}`)
	}
	readInput := r.submission(durable.SubmissionID(submissionID))
	if readInput != nil {
		r.rawJSON(readInput.Detail, `{"codes":["initial"]}`)
	}

	if readEntry != nil {
		readEntry.Entry.Data[0] = '!'
	}
	if readTask != nil {
		readTask.State.Checkpoint[0] = '!'
	}
	if readInput != nil {
		readInput.Detail[0] = '!'
	}

	againEntry := r.entry(durable.EntryID(entryID))
	if againEntry != nil {
		r.rawJSON(againEntry.Entry.Data, `{"nested":[1,2]}`)
	}
	againTask := r.task(durable.TaskID(taskID))
	if againTask != nil {
		r.rawJSON(againTask.State.Checkpoint, `{"phase":"ready","nested":{"count":1}}`)
	}
	againInput := r.submission(durable.SubmissionID(submissionID))
	if againInput != nil {
		r.rawJSON(againInput.Detail, `{"codes":["initial"]}`)
	}
}

func casePrototypeKeys(r *conformanceRun) {
	rootID := r.createRoot()
	entryID := r.mint()
	data := json.RawMessage(`{"__proto__":{"polluted":false},"constructor":{"label":"stored"},"toString":"value"}`)
	r.commit(durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "note", Data: data}})
	data[0] = '!'
	read := r.entry(durable.EntryID(entryID))
	if read != nil {
		r.rawJSON(read.Entry.Data, `{"__proto__":{"polluted":false},"constructor":{"label":"stored"},"toString":"value"}`)
		read.Entry.Data[0] = '!'
	}
	second := r.entry(durable.EntryID(entryID))
	if second != nil {
		r.rawJSON(second.Entry.Data, `{"__proto__":{"polluted":false},"constructor":{"label":"stored"},"toString":"value"}`)
	}
}

func caseIndexesOutOfOrder(r *conformanceRun) {
	rootID := r.createRoot()
	r.commit(
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: 30, ConversationID: durable.ConversationID(rootID), Kind: "message"}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: 10, ConversationID: durable.ConversationID(rootID), Kind: "message"}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: 20, ConversationID: durable.ConversationID(rootID), Kind: "marker", Head: ptr(durable.EntryID(10))}},
	)
	page := r.scanEntries(durable.EntryQuery{ConversationID: durable.ConversationID(rootID)}, 10, nil)
	r.ids(entryIDs(page.Items), 30, 20, 10)
	marker := r.headMarker(durable.ConversationID(rootID), nil)
	if marker == nil || marker.ID != 20 {
		if r.err == nil {
			r.fail("head marker = %+v", marker)
		}
	}
}

func caseEntryCursor(r *conformanceRun) {
	rootID := r.createRoot()
	oldestID := r.mint()
	middleID := r.mint()
	newestID := r.mint()
	r.commit(
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(oldestID), ConversationID: durable.ConversationID(rootID)}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(middleID), ConversationID: durable.ConversationID(rootID)}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(newestID), ConversationID: durable.ConversationID(rootID)}},
	)
	first := r.scanEntries(durable.EntryQuery{ConversationID: durable.ConversationID(rootID)}, 2, nil)
	r.ids(entryIDs(first.Items), newestID, middleID)
	appendedID := r.mint()
	r.commit(durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(appendedID), ConversationID: durable.ConversationID(rootID)}})
	second := r.scanEntries(durable.EntryQuery{ConversationID: durable.ConversationID(rootID)}, 2, first.Next)
	r.ids(entryIDs(second.Items), oldestID)
	if len(second.Next) != 0 {
		r.fail("unexpected continuation %s", second.Next)
	}
}

func caseConversationCursor(r *conformanceRun) {
	rootID := r.createRoot()
	secondID := r.mint()
	thirdID := r.mint()
	r.commit(
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(thirdID)}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(secondID)}},
	)
	first := r.scanConversations(durable.ConversationQuery{}, 2, nil)
	r.equal(conversationIDs(first.Items), []durable.ID{rootID, secondID})
	if len(first.Next) == 0 {
		r.fail("first page has no continuation")
	}
	second := r.scanConversations(durable.ConversationQuery{}, 2, first.Next)
	r.equal(conversationIDs(second.Items), []durable.ID{thirdID})
	if len(second.Next) != 0 {
		r.fail("unexpected continuation %s", second.Next)
	}
}

func caseConversationOwners(r *conformanceRun) {
	rootID := r.createRoot()
	otherOwnerID := r.mint()
	firstTaskID := r.mint()
	secondTaskID := r.mint()
	firstID := r.mint()
	secondID := r.mint()
	thirdID := r.mint()
	r.commit(
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(otherOwnerID)}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(firstID), Owner: &durable.ConversationOwner{ConversationID: durable.ConversationID(rootID), TaskID: durable.TaskID(firstTaskID)}}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(secondID), Owner: &durable.ConversationOwner{ConversationID: durable.ConversationID(rootID), TaskID: durable.TaskID(secondTaskID)}}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(thirdID), Owner: &durable.ConversationOwner{ConversationID: durable.ConversationID(otherOwnerID), TaskID: durable.TaskID(firstTaskID)}}},
	)
	first := r.scanConversations(durable.ConversationQuery{OwnerConversationID: ptr(durable.ConversationID(rootID))}, 1, nil)
	r.equal(conversationIDs(first.Items), []durable.ID{firstID})
	if len(first.Next) == 0 {
		r.fail("first page has no continuation")
	}
	second := r.scanConversations(durable.ConversationQuery{OwnerConversationID: ptr(durable.ConversationID(rootID))}, 1, first.Next)
	r.equal(conversationIDs(second.Items), []durable.ID{secondID})
	if len(second.Next) != 0 {
		r.fail("unexpected continuation %s", second.Next)
	}
	byTask := r.scanConversations(durable.ConversationQuery{OwnerTaskID: ptr(durable.TaskID(firstTaskID))}, 10, nil)
	r.equal(conversationIDs(byTask.Items), []durable.ID{firstID, thirdID})
	both := r.scanConversations(durable.ConversationQuery{OwnerConversationID: ptr(durable.ConversationID(rootID)), OwnerTaskID: ptr(durable.TaskID(firstTaskID))}, 10, nil)
	r.equal(conversationIDs(both.Items), []durable.ID{firstID})
}

func caseDeepFork(r *conformanceRun) {
	rootID := r.createRoot()
	rootFirst := r.mint()
	rootForkPoint := r.mint()
	rootExcludedSameCommit := r.mint()
	rootEntriesSeq := r.commit(
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(rootFirst), ConversationID: durable.ConversationID(rootID), Kind: "message"}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(rootForkPoint), ConversationID: durable.ConversationID(rootID), Kind: "marker", Head: ptr(durable.EntryID(rootFirst))}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(rootExcludedSameCommit), ConversationID: durable.ConversationID(rootID), Kind: "message"}},
	)
	childID := r.mint()
	r.commit(durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(childID), Parent: &durable.ConversationParent{ConversationID: durable.ConversationID(rootID), At: durable.EntryID(rootForkPoint)}}})
	childForkPoint := r.mint()
	childExcluded := r.mint()
	r.commit(
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(childForkPoint), ConversationID: durable.ConversationID(childID), Kind: "note"}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(childExcluded), ConversationID: durable.ConversationID(childID), Kind: "message"}},
	)
	rootExcludedLater := r.mint()
	r.commit(durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(rootExcludedLater), ConversationID: durable.ConversationID(rootID), Kind: "message"}})
	grandchildID := r.mint()
	r.commit(durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(grandchildID), Parent: &durable.ConversationParent{ConversationID: durable.ConversationID(childID), At: durable.EntryID(childForkPoint)}}})
	grandchildHead := r.mint()
	grandchildTail := r.mint()
	grandchildEntriesSeq := r.commit(
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(grandchildHead), ConversationID: durable.ConversationID(grandchildID), Kind: "marker", Head: ptr(durable.EntryID(grandchildHead))}},
		durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(grandchildTail), ConversationID: durable.ConversationID(grandchildID), Kind: "message"}},
	)
	childExcludedLater := r.mint()
	r.commit(durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(childExcludedLater), ConversationID: durable.ConversationID(childID), Kind: "message"}})

	query := durable.EntryQuery{ConversationID: durable.ConversationID(grandchildID)}
	first := r.scanEntries(query, 2, nil)
	r.equal(entryIDs(first.Items), []durable.ID{grandchildTail, grandchildHead})
	second := r.scanEntries(query, 2, first.Next)
	r.equal(entryIDs(second.Items), []durable.ID{childForkPoint, rootForkPoint})
	third := r.scanEntries(query, 2, second.Next)
	r.equal(entryIDs(third.Items), []durable.ID{rootFirst})
	if len(third.Next) != 0 {
		r.fail("unexpected continuation %s", third.Next)
	}

	currentMarker := r.headMarker(durable.ConversationID(grandchildID), nil)
	if currentMarker == nil || currentMarker.ID != durable.EntryID(grandchildHead) || currentMarker.Head == nil || *currentMarker.Head != durable.EntryID(grandchildHead) {
		if r.err == nil {
			r.fail("current marker = %+v", currentMarker)
		}
	}
	historicalMarker := r.headMarker(durable.ConversationID(grandchildID), ptr(durable.EntryID(childForkPoint)))
	if historicalMarker == nil || historicalMarker.ID != durable.EntryID(rootForkPoint) || historicalMarker.Head == nil || *historicalMarker.Head != durable.EntryID(rootFirst) {
		if r.err == nil {
			r.fail("historical marker = %+v", historicalMarker)
		}
	}
	r.nilValue(r.headMarker(durable.ConversationID(grandchildID), ptr(durable.EntryID(rootFirst))))

	if currentMarker != nil {
		activeFirst := r.scanEntries(durable.EntryQuery{ConversationID: durable.ConversationID(grandchildID), MinEntryID: currentMarker.Head}, 1, nil)
		r.equal(entryIDs(activeFirst.Items), []durable.ID{grandchildTail})
		if len(activeFirst.Next) == 0 {
			r.fail("active first page has no continuation")
		}
		activeSecond := r.scanEntries(durable.EntryQuery{ConversationID: durable.ConversationID(grandchildID), MinEntryID: currentMarker.Head}, 1, activeFirst.Next)
		r.equal(entryIDs(activeSecond.Items), []durable.ID{grandchildHead})
		if len(activeSecond.Next) != 0 {
			r.fail("unexpected continuation %s", activeSecond.Next)
		}
	}
	if historicalMarker != nil {
		bounded := r.scanEntries(durable.EntryQuery{ConversationID: durable.ConversationID(grandchildID), MinEntryID: historicalMarker.Head, MaxEntryID: ptr(durable.EntryID(childForkPoint))}, 10, nil)
		r.equal(entryIDs(bounded.Items), []durable.ID{childForkPoint, rootForkPoint, rootFirst})
	}

	r.equal(r.entry(durable.EntryID(rootFirst)), &durable.StoredEntry{Entry: durable.EntryRecord{ID: durable.EntryID(rootFirst), ConversationID: durable.ConversationID(rootID), Kind: "message"}, CommitSeq: rootEntriesSeq})
	rootForkEntry := r.entry(durable.EntryID(rootForkPoint))
	if rootForkEntry != nil && rootForkEntry.CommitSeq != rootEntriesSeq {
		r.fail("root fork commit seq = %d", rootForkEntry.CommitSeq)
	}
	grandchildHeadEntry := r.entry(durable.EntryID(grandchildHead))
	if grandchildHeadEntry != nil && grandchildHeadEntry.CommitSeq != grandchildEntriesSeq {
		r.fail("grandchild head commit seq = %d", grandchildHeadEntry.CommitSeq)
	}
	r.nilValue(r.entry(durable.EntryID(999999)))

	inherited, err := r.storage.VisibleEntry(r.ctx, durable.ConversationID(grandchildID), durable.EntryID(rootFirst))
	if err != nil {
		r.fail("visible inherited: %v", err)
	}
	r.equal(inherited, &durable.StoredEntry{Entry: durable.EntryRecord{ID: durable.EntryID(rootFirst), ConversationID: durable.ConversationID(rootID), Kind: "message"}, CommitSeq: rootEntriesSeq})
	childForkEntry, err := r.storage.VisibleEntry(r.ctx, durable.ConversationID(grandchildID), durable.EntryID(childForkPoint))
	if err != nil {
		r.fail("visible child fork: %v", err)
	} else if childForkEntry.Entry.ConversationID != durable.ConversationID(childID) {
		r.fail("child fork conversation = %d", childForkEntry.Entry.ConversationID)
	}
	for _, id := range []durable.ID{rootExcludedSameCommit, rootExcludedLater, childExcluded, childExcludedLater} {
		hidden, err := r.storage.VisibleEntry(r.ctx, durable.ConversationID(grandchildID), durable.EntryID(id))
		if err != nil {
			r.fail("visible %d: %v", id, err)
			continue
		}
		r.nilValue(hidden)
	}
	reverse, err := r.storage.VisibleEntry(r.ctx, durable.ConversationID(rootID), durable.EntryID(grandchildHead))
	if err != nil {
		r.fail("reverse visible: %v", err)
	}
	r.nilValue(reverse)
	if _, err := r.storage.VisibleEntry(r.ctx, 999999, durable.EntryID(rootFirst)); err == nil {
		r.fail("visible entry of unknown conversation was accepted")
	}
	if _, err := r.storage.ScanEntries(r.ctx, durable.EntryQuery{ConversationID: 999999}, 10, nil); err == nil {
		r.fail("entry scan of unknown conversation was accepted")
	}
}

func caseTaskReplacement(r *conformanceRun) {
	rootID := r.createRoot()
	firstID := r.mint()
	secondID := r.mint()
	thirdID := r.mint()
	first := pendingTaskRecord(firstID, rootID, "ready")
	first.Memos = map[string]json.RawMessage{"winner": json.RawMessage(`"first"`)}
	second := pendingTaskRecord(secondID, rootID, "ready")
	second.Background = true
	third := pendingTaskRecord(thirdID, rootID, "ready")
	third.AbortRequested = true
	r.commit(
		durable.StorageWrite{Type: durable.WriteTask, Task: &first},
		durable.StorageWrite{Type: durable.WriteTask, Task: &second},
		durable.StorageWrite{Type: durable.WriteTask, Task: &third},
	)
	running := first
	running.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: json.RawMessage(`{"phase":"effect","attempt":1}`)}
	running.AbortRequested = true
	r.commit(durable.StorageWrite{Type: durable.WriteTask, Task: &running})
	r.equal(r.task(durable.TaskID(firstID)), &running)

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
	r.commit(durable.StorageWrite{Type: durable.WriteTask, Task: &terminal})
	r.equal(r.task(durable.TaskID(firstID)), &terminal)

	pendingPage := r.scanTasks(durable.TaskQuery{Status: durable.TaskStatusPending}, 1, nil)
	r.equal(taskIDs(pendingPage.Items), []durable.ID{secondID})
	if len(pendingPage.Next) == 0 {
		r.fail("pending page has no continuation")
	}
	pendingRest := r.scanTasks(durable.TaskQuery{Status: durable.TaskStatusPending}, 1, pendingPage.Next)
	r.equal(taskIDs(pendingRest.Items), []durable.ID{thirdID})
	terminalPage := r.scanTasks(durable.TaskQuery{Status: durable.TaskStatusTerminal, AbortRequested: ptr(true)}, 10, nil)
	r.equal(terminalPage.Items, []durable.TaskRecord{terminal})
	backgroundPage := r.scanTasks(durable.TaskQuery{Background: ptr(true)}, 10, nil)
	r.equal(taskIDs(backgroundPage.Items), []durable.ID{secondID})
}

func caseTaskStatuses(r *conformanceRun) {
	rootID := r.createRoot()
	ownerID := r.mint()
	waitingID := r.mint()
	completingID := r.mint()
	owner := pendingTaskRecord(ownerID, rootID, "ready")
	waiting := pendingTaskRecord(waitingID, rootID, "next")
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
	r.commit(
		durable.StorageWrite{Type: durable.WriteTask, Task: &owner},
		durable.StorageWrite{Type: durable.WriteTask, Task: &waiting},
		durable.StorageWrite{Type: durable.WriteTask, Task: &completing},
	)
	r.equal(r.task(durable.TaskID(waitingID)), &waiting)
	r.equal(r.task(durable.TaskID(completingID)), &completing)

	scan := func(status string) []durable.TaskRecord {
		return r.scanTasks(durable.TaskQuery{Status: status}, 10, nil).Items
	}
	r.equal(scan(durable.TaskStatusWaiting), []durable.TaskRecord{waiting})
	r.equal(scan(durable.TaskStatusCompleting), []durable.TaskRecord{completing})
	r.equal(taskIDs(scan(durable.TaskStatusPending)), []durable.ID{ownerID})

	terminal := completing
	terminal.State = durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: completing.State.Outcome}
	r.commit(durable.StorageWrite{Type: durable.WriteTask, Task: &terminal})
	r.equal(scan(durable.TaskStatusCompleting), []durable.TaskRecord{})
	r.equal(scan(durable.TaskStatusTerminal), []durable.TaskRecord{terminal})
}

func caseRequestIDs(r *conformanceRun) {
	rootID := r.createRoot()
	secondConversationID := r.mint()
	r.commit(durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(secondConversationID)}})
	firstID := r.mint()
	secondID := r.mint()
	otherConversationID := r.mint()
	first := durable.SubmissionRecord{ID: durable.SubmissionID(firstID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("same"), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}
	second := durable.SubmissionRecord{ID: durable.SubmissionID(secondID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("other"), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}
	otherConversation := durable.SubmissionRecord{ID: durable.SubmissionID(otherConversationID), ConversationID: durable.ConversationID(secondConversationID), RequestID: ptr("same"), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}
	r.commit(
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &first},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &second},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &otherConversation},
	)
	r.equal(r.submissionByRequest(durable.ConversationID(rootID), "same"), &first)
	r.equal(r.submissionByRequest(durable.ConversationID(secondConversationID), "same"), &otherConversation)

	placedSecond := second
	placedSecond.Status = durable.SubmissionStatusPlaced
	placedSecond.Entry = ptr(durable.EntryID(r.mint()))
	r.commit(durable.StorageWrite{Type: durable.WriteSubmission, Submission: &placedSecond})
	r.equal(r.submission(durable.SubmissionID(secondID)), &placedSecond)
	r.equal(r.submissionByRequest(durable.ConversationID(rootID), "other"), &placedSecond)

	ids := func(query durable.SubmissionQuery) []durable.ID {
		found := []durable.ID{}
		var cursor durable.Cursor
		for {
			page := r.scanSubmissions(query, 1, cursor)
			if r.err != nil {
				return found
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
	r.equal(ids(durable.SubmissionQuery{}), []durable.ID{firstID, secondID, otherConversationID})
	r.equal(ids(durable.SubmissionQuery{ConversationID: ptr(durable.ConversationID(rootID))}), []durable.ID{firstID, secondID})
	r.equal(ids(durable.SubmissionQuery{Status: durable.SubmissionStatusQueued}), []durable.ID{firstID, otherConversationID})
	r.equal(ids(durable.SubmissionQuery{Status: durable.SubmissionStatusPlaced}), []durable.ID{secondID})
	r.equal(ids(durable.SubmissionQuery{ConversationID: ptr(durable.ConversationID(secondConversationID)), Status: durable.SubmissionStatusQueued}), []durable.ID{otherConversationID})
	r.equal(ids(durable.SubmissionQuery{ConversationID: ptr(durable.ConversationID(secondConversationID)), Status: durable.SubmissionStatusPlaced}), []durable.ID{})
	placedPage := r.scanSubmissions(durable.SubmissionQuery{Status: durable.SubmissionStatusPlaced}, 10, nil)
	r.equal(placedPage.Items, []durable.SubmissionRecord{placedSecond})
}

func casePassiveWrites(r *conformanceRun) {
	rootID := r.createRoot()
	doneID := r.mint()
	failedID := r.mint()
	queuedDone := durable.SubmissionRecord{ID: durable.SubmissionID(doneID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("passive-done"), Type: durable.SubmissionTypeWrite, Status: durable.SubmissionStatusQueued}
	queuedFailed := durable.SubmissionRecord{ID: durable.SubmissionID(failedID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("passive-failed"), Type: durable.SubmissionTypeWrite, Status: durable.SubmissionStatusQueued}
	r.commit(
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &queuedDone},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &queuedFailed},
	)
	done := queuedDone
	done.Status = durable.SubmissionStatusDone
	done.Entry = ptr(durable.EntryID(r.mint()))
	unanswered := queuedFailed
	unanswered.Status = durable.SubmissionStatusUnanswered
	unanswered.Reason = "closed"
	unanswered.Detail = json.RawMessage(`{"retryable":false}`)
	r.commit(
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &done},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &unanswered},
	)
	r.equal(r.submission(durable.SubmissionID(doneID)), &done)
	r.equal(r.submissionByRequest(durable.ConversationID(rootID), "passive-done"), &done)
	r.equal(r.submission(durable.SubmissionID(failedID)), &unanswered)
	r.equal(r.submissionByRequest(durable.ConversationID(rootID), "passive-failed"), &unanswered)
}

func caseRewindableDocuments(r *conformanceRun) {
	rootID := r.createRoot()
	firstID := r.mint()
	scope := durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}
	firstRecord := durable.DocumentCreate{ID: durable.DocumentID(firstID), Kind: "conversation.notes", Scope: scope, History: durable.HistoryRewindable, Fork: durable.ForkAsOf}
	initial := durable.JsonObject{"items": []any{"a"}, "nested": durable.JsonObject{"count": 1}}
	createdAt := r.commit(durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &firstRecord, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: initial}})
	ops := []chord.Op{
		{"p", []any{"items"}, 1, 0, []any{"b"}},
		{"s", []any{"nested", "count"}, 2},
	}
	changedAt := r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: ops}})

	initial["items"] = append(initial["items"].([]any), "caller mutation")
	r.partial(r.document(durable.DocumentID(firstID), durable.AtSeq(createdAt)), map[string]any{"version": 1, "value": map[string]any{"items": []any{"a"}, "nested": map[string]any{"count": 1}}, "deltasSinceBase": 0})
	changed := r.document(durable.DocumentID(firstID), durable.AtSeq(changedAt))
	if changed != nil {
		r.equal(changed.Value, durable.JsonObject{"items": []any{"a", "b"}, "nested": durable.JsonObject{"count": 2}})
		if changed.DeltasSinceBase != 1 {
			r.fail("deltas since base = %d", changed.DeltasSinceBase)
		}
		changed.Value["items"] = append(changed.Value["items"].([]any), "read mutation")
	}
	current := r.document(durable.DocumentID(firstID), durable.CurrentDocument())
	if current != nil {
		r.equal(current.Value, durable.JsonObject{"items": []any{"a", "b"}, "nested": durable.JsonObject{"count": 2}})
	}

	checkpointAt := r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 2, Value: durable.JsonObject{"items": []any{"checkpoint"}, "nested": durable.JsonObject{"count": 3}}}})
	replacedAt := r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 2, Ops: []chord.Op{{"r", durable.JsonObject{"items": []any{"replacement"}, "nested": durable.JsonObject{"count": 4}}}}}})
	r.partial(r.document(durable.DocumentID(firstID), durable.AtSeq(changedAt)), map[string]any{"version": 1, "value": map[string]any{"items": []any{"a", "b"}, "nested": map[string]any{"count": 2}}})
	r.partial(r.document(durable.DocumentID(firstID), durable.AtSeq(checkpointAt)), map[string]any{"version": 2, "value": map[string]any{"items": []any{"checkpoint"}, "nested": map[string]any{"count": 3}}, "deltasSinceBase": 0})
	r.partial(r.document(durable.DocumentID(firstID), durable.AtSeq(replacedAt)), map[string]any{"value": map[string]any{"items": []any{"replacement"}, "nested": map[string]any{"count": 4}}, "deltasSinceBase": 1})
	currentNow := r.document(durable.DocumentID(firstID), durable.CurrentDocument())
	if currentNow != nil && currentNow.DeltasSinceBase != 1 {
		r.fail("current deltas since base = %d", currentNow.DeltasSinceBase)
	}

	secondID := r.mint()
	secondRecord := firstRecord
	secondRecord.ID = durable.DocumentID(secondID)
	retiredAt := r.commit(
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &secondRecord, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"items": []any{"new"}}}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(firstID)},
		durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 2, Ops: []chord.Op{{"s", []any{"retiring"}, true}}}},
	)
	address := durable.DocumentAddress{Kind: firstRecord.Kind, Scope: firstRecord.Scope}
	found := r.findDocument(address, durable.AtSeq(changedAt))
	if found == nil || found.ID != durable.DocumentID(firstID) {
		if r.err == nil {
			r.fail("historic address = %+v", found)
		}
	}
	r.partial(r.findDocument(address, durable.AtSeq(retiredAt)), map[string]any{"id": secondID, "createdAt": retiredAt})
	r.equal(documentIDs(r.scanDocuments(durable.DocumentQuery{Scope: firstRecord.Scope, At: durable.AtSeq(changedAt)}, 10, nil).Items), []durable.ID{firstID})
	r.equal(documentIDs(r.scanDocuments(durable.DocumentQuery{Scope: firstRecord.Scope, At: durable.AtSeq(retiredAt)}, 10, nil).Items), []durable.ID{secondID})
	r.nilValue(r.document(durable.DocumentID(firstID), durable.AtSeq(retiredAt)))
	newValue := r.document(durable.DocumentID(secondID), durable.CurrentDocument())
	if newValue != nil {
		r.equal(newValue.Value, durable.JsonObject{"items": []any{"new"}})
	}
}

func caseLongTails(r *conformanceRun) {
	rootID := r.createRoot()
	id := r.mint()
	record := durable.DocumentCreate{ID: durable.DocumentID(id), Kind: "conversation.long-tail", Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryRewindable, Fork: durable.ForkAsOf}
	rows := make([]any, 512)
	for index := range rows {
		rows[index] = durable.JsonObject{"value": index, "stable": fmt.Sprintf("row-%d", index)}
	}
	initial := durable.JsonObject{"revision": 0, "rows": rows}
	createdAt := r.commit(durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: initial}})

	beforeRows, err := cloneRows(rows)
	if err != nil {
		r.fail("clone rows: %v", err)
		return
	}
	beforeReplacement := durable.JsonObject{"revision": 0, "rows": beforeRows}
	beforeReplacementAt := createdAt
	for revision := 1; revision <= 24; revision++ {
		index := (revision * 17) % 512
		beforeReplacement["rows"].([]any)[index].(durable.JsonObject)["value"] = -revision
		beforeReplacement["revision"] = revision
		beforeReplacementAt = r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{
			{"s", []any{"rows", index, "value"}, -revision},
			{"s", []any{"revision"}, revision},
		}}})
	}

	newRows := make([]any, 512)
	for index := range newRows {
		newRows[index] = durable.JsonObject{"value": 10000 + index, "stable": fmt.Sprintf("new-%d", index)}
	}
	replacement := durable.JsonObject{"revision": 100, "rows": newRows}
	replacementSnapshotRows, err := cloneRows(newRows)
	if err != nil {
		r.fail("clone new rows: %v", err)
		return
	}
	replacementSnapshot := durable.JsonObject{"revision": 100, "rows": replacementSnapshotRows}
	replacementAt := r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"r", replacement}}}})
	replacement["rows"].([]any)[0].(durable.JsonObject)["value"] = -999

	current, err := cloneObject(replacementSnapshot)
	if err != nil {
		r.fail("clone snapshot: %v", err)
		return
	}
	for revision := 101; revision <= 124; revision++ {
		index := (revision * 19) % 512
		current["rows"].([]any)[index].(durable.JsonObject)["value"] = -revision
		current["revision"] = revision
		r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{
			{"s", []any{"rows", index, "value"}, -revision},
			{"s", []any{"revision"}, revision},
		}}})
	}

	atCreate := r.document(durable.DocumentID(id), durable.AtSeq(createdAt))
	if atCreate != nil {
		r.equal(atCreate.Value, initial)
	}
	atBefore := r.document(durable.DocumentID(id), durable.AtSeq(beforeReplacementAt))
	if atBefore != nil {
		r.equal(atBefore.Value, beforeReplacement)
	}
	atReplacement := r.document(durable.DocumentID(id), durable.AtSeq(replacementAt))
	if atReplacement != nil {
		r.equal(atReplacement.Value, replacementSnapshot)
	}
	read := r.document(durable.DocumentID(id), durable.CurrentDocument())
	if read != nil {
		r.equal(read.Value, current)
		read.Value["rows"].([]any)[0].(durable.JsonObject)["value"] = -1000
	}
	again := r.document(durable.DocumentID(id), durable.CurrentDocument())
	if again != nil {
		r.equal(again.Value, current)
	}
}

func caseDocumentCopies(r *conformanceRun) {
	rootID := r.createRoot()
	childID := r.mint()
	secondChildID := r.mint()
	r.commit(
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(childID)}},
		durable.StorageWrite{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.ConversationID(secondChildID)}},
	)
	sourceID := r.mint()
	sourceRecord := durable.DocumentCreate{ID: durable.DocumentID(sourceID), Kind: "copy.source", Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryRewindable, Fork: durable.ForkAsOf}
	createdAt := r.commit(durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &sourceRecord, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 2, Value: durable.JsonObject{"count": 1, "rows": []any{durable.JsonObject{"value": "base"}}}}})
	r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(sourceID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 2, Ops: []chord.Op{
		{"s", []any{"count"}, 2},
		{"p", []any{"rows"}, 1, 0, []any{durable.JsonObject{"value": "current"}}},
	}}})
	historicalCopyID := r.mint()
	currentCopyID := r.mint()
	retiredCopyID := r.mint()
	childRecord := func(id, conversationID durable.ID) durable.DocumentCreate {
		return durable.DocumentCreate{ID: durable.DocumentID(id), Kind: sourceRecord.Kind, Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(conversationID)}, History: durable.HistoryRewindable, Fork: durable.ForkAsOf}
	}
	r.commit(
		durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: ptr(childRecord(historicalCopyID, childID)), Source: &durable.DocumentCopySource{ID: durable.DocumentID(sourceID), At: durable.AtSeq(createdAt)}},
		durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: ptr(childRecord(currentCopyID, secondChildID)), Source: &durable.DocumentCopySource{ID: durable.DocumentID(sourceID), At: durable.CurrentDocument()}},
		durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: ptr(childRecord(retiredCopyID, rootID)), Source: &durable.DocumentCopySource{ID: durable.DocumentID(sourceID), At: durable.CurrentDocument()}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(retiredCopyID)},
	)
	r.partial(r.document(durable.DocumentID(historicalCopyID), durable.CurrentDocument()), map[string]any{"version": 2, "value": map[string]any{"count": 1, "rows": []any{map[string]any{"value": "base"}}}})
	r.partial(r.document(durable.DocumentID(currentCopyID), durable.CurrentDocument()), map[string]any{"version": 2, "value": map[string]any{"count": 2, "rows": []any{map[string]any{"value": "base"}, map[string]any{"value": "current"}}}})
	r.nilValue(r.document(durable.DocumentID(retiredCopyID), durable.CurrentDocument()))

	r.commit(
		durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(sourceID), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 2, Value: durable.JsonObject{"count": 99, "rows": []any{}}}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(sourceID)},
	)
	copied := r.document(durable.DocumentID(currentCopyID), durable.CurrentDocument())
	if copied != nil {
		r.equal(copied.Value, durable.JsonObject{"count": 2, "rows": []any{durable.JsonObject{"value": "base"}, durable.JsonObject{"value": "current"}}})
	}

	latestSourceID := r.mint()
	latestCopyID := r.mint()
	latestSource := durable.DocumentCreate{ID: durable.DocumentID(latestSourceID), Kind: "copy.latest", Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryLatest, Fork: durable.ForkCurrent}
	r.commit(durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &latestSource, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 4, Value: durable.JsonObject{"retained": "copy"}}})
	latestCopy := latestSource
	latestCopy.ID = durable.DocumentID(latestCopyID)
	latestCopy.Scope = durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(childID)}
	r.commit(durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: &latestCopy, Source: &durable.DocumentCopySource{ID: durable.DocumentID(latestSourceID), At: durable.CurrentDocument()}})
	r.commit(
		durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(latestSourceID), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 4, Value: durable.JsonObject{"retained": "source-only"}}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(latestSourceID)},
	)
	r.partial(r.document(durable.DocumentID(latestCopyID), durable.CurrentDocument()), map[string]any{"version": 4, "value": map[string]any{"retained": "copy"}})

	// Ambiguous source: the source is retired in the same batch as the copy. The
	// Go contract (like upstream) rejects a changed source as a definite
	// StorageRejected and publishes nothing.
	conflictID := r.mint()
	conflictRecord := childRecord(conflictID, childID)
	r.expectError("changed in the copy batch", func() error {
		_, err := r.storage.Commit(r.ctx, []durable.StorageWrite{
			{Type: durable.WriteDocumentCopy, Record: &conflictRecord, Source: &durable.DocumentCopySource{ID: durable.DocumentID(currentCopyID), At: durable.CurrentDocument()}},
			{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(currentCopyID)},
		})
		return err
	})
	r.nilValue(r.document(durable.DocumentID(conflictID), durable.CurrentDocument()))
	preserved := r.document(durable.DocumentID(currentCopyID), durable.CurrentDocument())
	if preserved != nil {
		r.equal(preserved.Value, durable.JsonObject{"count": 2, "rows": []any{durable.JsonObject{"value": "base"}, durable.JsonObject{"value": "current"}}})
	}

	// Go adaptation: document copy is definition-free, so a different target
	// kind is legal and copies the stored base and version unchanged.
	defFreeID := r.mint()
	defFree := childRecord(defFreeID, childID)
	defFree.Kind = "copy.mismatch"
	r.commit(durable.StorageWrite{Type: durable.WriteDocumentCopy, Record: &defFree, Source: &durable.DocumentCopySource{ID: durable.DocumentID(currentCopyID), At: durable.CurrentDocument()}})
	r.partial(r.document(durable.DocumentID(defFreeID), durable.CurrentDocument()), map[string]any{"version": 2, "value": map[string]any{"count": 2}})
}

func caseVersionTransitions(r *conformanceRun) {
	r.createRoot()
	id := r.mint()
	record := durable.DocumentCreate{ID: durable.DocumentID(id), Kind: "session.settings", Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
	r.commit(durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 1}}})
	r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 2}}}})
	migratedAt := r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 2, Value: durable.JsonObject{"count": 3}}})
	r.partial(r.document(durable.DocumentID(id), durable.CurrentDocument()), map[string]any{"version": 2, "value": map[string]any{"count": 3}})
	r.expectError("", func() error {
		_, err := r.storage.Document(r.ctx, durable.DocumentID(id), durable.AtSeq(migratedAt))
		return err
	})
	r.expectError("version transition requires a base", func() error {
		_, err := r.storage.Commit(r.ctx, []durable.StorageWrite{{Type: durable.WriteDocumentChange, ID: durable.DocumentID(id), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 4}}}}})
		return err
	})
	current := r.document(durable.DocumentID(id), durable.CurrentDocument())
	if current != nil {
		r.equal(current.Value, durable.JsonObject{"count": 3})
	}
	r.commit(durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(id)})
	r.nilValue(r.document(durable.DocumentID(id), durable.CurrentDocument()))
}

func caseAddressIndexes(r *conformanceRun) {
	rootID := r.createRoot()
	firstID := r.mint()
	secondID := r.mint()
	conversationDocumentID := r.mint()
	taskID := r.mint()
	taskSingletonID := r.mint()
	taskFamilyID := r.mint()
	taskOtherKindID := r.mint()
	createdAt := r.commit(
		durable.StorageWrite{Type: durable.WriteTask, Task: ptr(pendingTaskRecord(taskID, rootID, "ready"))},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(firstID), Kind: "cache", Scope: durable.DocumentScope{Kind: durable.ScopeSession}, Key: ptr("__proto__")}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "first"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(secondID), Kind: "cache", Scope: durable.DocumentScope{Kind: durable.ScopeSession}, Key: ptr("constructor")}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "second"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(conversationDocumentID), Kind: "cache", Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryLatest, Fork: durable.ForkCurrent, Key: ptr("__proto__")}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "conversation"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(taskSingletonID), Kind: "task.cache", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "singleton"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(taskFamilyID), Kind: "task.cache", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}, Key: ptr("member")}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "family"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(taskOtherKindID), Kind: "task.other", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"owner": "other"}}},
	)

	found := r.findDocument(durable.DocumentAddress{Kind: "cache", Scope: durable.DocumentScope{Kind: durable.ScopeSession}, Key: ptr("__proto__")}, durable.CurrentDocument())
	if found == nil || found.ID != durable.DocumentID(firstID) {
		if r.err == nil {
			r.fail("session family member = %+v", found)
		}
	}
	first := r.scanDocuments(durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeSession}, At: durable.CurrentDocument()}, 1, nil)
	if len(first.Items) != 1 {
		if r.err == nil {
			r.fail("first session page = %+v", first.Items)
		}
	}
	second := r.scanDocuments(durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeSession}, At: durable.CurrentDocument()}, 1, first.Next)
	combined := append(append([]durable.DocumentRecord{}, first.Items...), second.Items...)
	r.equal(documentIDs(combined), []durable.ID{firstID, secondID})
	r.equal(documentIDs(r.scanDocuments(durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, At: durable.CurrentDocument()}, 10, nil).Items), []durable.ID{conversationDocumentID})
	found = r.findDocument(durable.DocumentAddress{Kind: "task.cache", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}}, durable.CurrentDocument())
	if found == nil || found.ID != durable.DocumentID(taskSingletonID) {
		if r.err == nil {
			r.fail("task singleton = %+v", found)
		}
	}
	found = r.findDocument(durable.DocumentAddress{Kind: "task.cache", Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}, Key: ptr("member")}, durable.CurrentDocument())
	if found == nil || found.ID != durable.DocumentID(taskFamilyID) {
		if r.err == nil {
			r.fail("task family = %+v", found)
		}
	}
	r.equal(documentIDs(r.scanDocuments(durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeTask, TaskID: durable.TaskID(taskID)}, At: durable.CurrentDocument(), Kind: "task.cache"}, 10, nil).Items), []durable.ID{taskSingletonID, taskFamilyID})
	r.expectError("", func() error {
		_, err := r.storage.Document(r.ctx, durable.DocumentID(taskSingletonID), durable.AtSeq(createdAt))
		return err
	})
}

func caseDocumentLifecycle(r *conformanceRun) {
	rootID := r.createRoot()
	firstID := r.mint()
	secondID := r.mint()
	record := durable.DocumentCreate{ID: durable.DocumentID(firstID), Kind: "singleton", Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
	r.commit(durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"value": 1}}})
	r.expectError("already has a current incarnation", func() error {
		_, err := r.storage.Commit(r.ctx, []durable.StorageWrite{
			{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(secondID), Kind: record.Kind, Scope: record.Scope}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"value": 2}}},
			{Type: durable.WriteDocumentChange, ID: durable.DocumentID(firstID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{}}},
		})
		return err
	})
	first := r.document(durable.DocumentID(firstID), durable.CurrentDocument())
	if first != nil {
		r.equal(first.Value, durable.JsonObject{"value": 1})
	}
	r.nilValue(r.document(durable.DocumentID(secondID), durable.CurrentDocument()))

	emptyID := r.mint()
	emptyAt := r.commit(
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(emptyID), Kind: record.Kind, Key: ptr("empty"), Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, History: durable.HistoryRewindable, Fork: durable.ForkInitial}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{}}},
		durable.StorageWrite{Type: durable.WriteDocumentRetire, ID: durable.DocumentID(emptyID)},
	)
	r.nilValue(r.document(durable.DocumentID(emptyID), durable.CurrentDocument()))
	r.nilValue(r.document(durable.DocumentID(emptyID), durable.AtSeq(emptyAt)))
	r.nilValue(r.findDocument(durable.DocumentAddress{Kind: record.Kind, Scope: durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: durable.ConversationID(rootID)}, Key: ptr("empty")}, durable.AtSeq(emptyAt)))
}

func caseDocumentRollback(r *conformanceRun) {
	rootID := r.createRoot()
	taskID := r.mint()
	submissionID := r.mint()
	documentID := r.mint()
	task := pendingTaskRecord(taskID, rootID, "ready")
	submission := durable.SubmissionRecord{ID: durable.SubmissionID(submissionID), ConversationID: durable.ConversationID(rootID), RequestID: ptr("atomic"), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}
	record := durable.DocumentCreate{ID: durable.DocumentID(documentID), Kind: "atomic", Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
	baselineSeq := r.commit(
		durable.StorageWrite{Type: durable.WriteTask, Task: &task},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &submission},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &record, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 1}}},
	)

	entryID := r.mint()
	conflictingDocumentID := r.mint()
	running := task
	running.State = durable.TaskState{Status: durable.TaskStatusRunning, Checkpoint: json.RawMessage(`{"phase":"effect"}`)}
	unanswered := submission
	unanswered.Status = durable.SubmissionStatusUnanswered
	unanswered.Reason = "failed"
	r.expectError("already has a current incarnation", func() error {
		_, err := r.storage.Commit(r.ctx, []durable.StorageWrite{
			{Type: durable.WriteTask, Task: &running},
			{Type: durable.WriteSubmission, Submission: &unanswered},
			{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(entryID), ConversationID: durable.ConversationID(rootID), Kind: "transient"}},
			{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(conflictingDocumentID), Kind: record.Kind, Scope: record.Scope}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 2}}},
		})
		return err
	})
	r.equal(r.task(durable.TaskID(taskID)), &task)
	r.equal(r.scanTasks(durable.TaskQuery{Status: durable.TaskStatusPending}, 10, nil).Items, []durable.TaskRecord{task})
	r.equal(r.submissionByRequest(durable.ConversationID(rootID), "atomic"), &submission)
	r.nilValue(r.entry(durable.EntryID(entryID)))
	r.nilValue(r.document(durable.DocumentID(conflictingDocumentID), durable.CurrentDocument()))
	existing := r.findDocument(durable.DocumentAddress{Kind: record.Kind, Scope: record.Scope}, durable.CurrentDocument())
	if existing == nil || existing.ID != durable.DocumentID(documentID) {
		if r.err == nil {
			r.fail("existing document = %+v", existing)
		}
	}
	afterRollbackSeq := r.commit(durable.StorageWrite{Type: durable.WriteDocumentChange, ID: durable.DocumentID(documentID), Content: &durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 3}}}})
	r.greaterThan(int64(afterRollbackSeq), int64(baselineSeq))
}

// caseStringIdentity ports the upstream "keeps indexed string identities
// lossless" case with a documented Go adaptation: isolated UTF-16 surrogates are
// not representable as valid Go strings, so distinct supplementary code points
// exercise the same multi-byte identity boundary.
func caseStringIdentity(r *conformanceRun) {
	rootID := r.createRoot()
	first := "\U0001F600"
	second := "\U0001F601"
	firstTaskID := r.mint()
	secondTaskID := r.mint()
	firstSubmissionID := r.mint()
	secondSubmissionID := r.mint()
	firstKindDocumentID := r.mint()
	secondKindDocumentID := r.mint()
	firstKeyDocumentID := r.mint()
	secondKeyDocumentID := r.mint()
	firstTask := pendingTaskRecord(firstTaskID, rootID, "ready")
	firstTask.Kind = first
	secondTask := pendingTaskRecord(secondTaskID, rootID, "ready")
	secondTask.Kind = second
	r.commit(
		durable.StorageWrite{Type: durable.WriteTask, Task: &firstTask},
		durable.StorageWrite{Type: durable.WriteTask, Task: &secondTask},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &durable.SubmissionRecord{ID: durable.SubmissionID(firstSubmissionID), ConversationID: durable.ConversationID(rootID), RequestID: ptr(first), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}},
		durable.StorageWrite{Type: durable.WriteSubmission, Submission: &durable.SubmissionRecord{ID: durable.SubmissionID(secondSubmissionID), ConversationID: durable.ConversationID(rootID), RequestID: ptr(second), Type: durable.SubmissionTypeInput, Status: durable.SubmissionStatusQueued}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(firstKindDocumentID), Kind: first, Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"identity": "first kind"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(secondKindDocumentID), Kind: second, Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"identity": "second kind"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(firstKeyDocumentID), Kind: "family", Key: ptr(first), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"identity": "first key"}}},
		durable.StorageWrite{Type: durable.WriteDocumentCreate, Record: &durable.DocumentCreate{ID: durable.DocumentID(secondKeyDocumentID), Kind: "family", Key: ptr(second), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"identity": "second key"}}},
	)

	r.equal(taskIDs(r.scanTasks(durable.TaskQuery{Kind: first}, 10, nil).Items), []durable.ID{firstTaskID})
	r.equal(taskIDs(r.scanTasks(durable.TaskQuery{Kind: second}, 10, nil).Items), []durable.ID{secondTaskID})
	storedFirstTask := r.task(durable.TaskID(firstTaskID))
	if storedFirstTask != nil && storedFirstTask.Kind != first {
		r.fail("first task kind = %q", storedFirstTask.Kind)
	}
	storedSecondTask := r.task(durable.TaskID(secondTaskID))
	if storedSecondTask != nil && storedSecondTask.Kind != second {
		r.fail("second task kind = %q", storedSecondTask.Kind)
	}
	firstRequest := r.submissionByRequest(durable.ConversationID(rootID), first)
	if firstRequest == nil || firstRequest.ID != durable.SubmissionID(firstSubmissionID) {
		if r.err == nil {
			r.fail("first request = %+v", firstRequest)
		}
	}
	secondRequest := r.submissionByRequest(durable.ConversationID(rootID), second)
	if secondRequest == nil || secondRequest.ID != durable.SubmissionID(secondSubmissionID) {
		if r.err == nil {
			r.fail("second request = %+v", secondRequest)
		}
	}
	firstKind := r.findDocument(durable.DocumentAddress{Kind: first, Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
	if firstKind == nil || firstKind.ID != durable.DocumentID(firstKindDocumentID) {
		if r.err == nil {
			r.fail("first kind document = %+v", firstKind)
		}
	}
	secondKind := r.findDocument(durable.DocumentAddress{Kind: second, Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
	if secondKind == nil || secondKind.ID != durable.DocumentID(secondKindDocumentID) {
		if r.err == nil {
			r.fail("second kind document = %+v", secondKind)
		}
	}
	firstKey := r.findDocument(durable.DocumentAddress{Kind: "family", Key: ptr(first), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
	if firstKey == nil || firstKey.ID != durable.DocumentID(firstKeyDocumentID) {
		if r.err == nil {
			r.fail("first key document = %+v", firstKey)
		}
	}
	secondKey := r.findDocument(durable.DocumentAddress{Kind: "family", Key: ptr(second), Scope: durable.DocumentScope{Kind: durable.ScopeSession}}, durable.CurrentDocument())
	if secondKey == nil || secondKey.ID != durable.DocumentID(secondKeyDocumentID) {
		if r.err == nil {
			r.fail("second key document = %+v", secondKey)
		}
	}
	r.equal(documentIDs(r.scanDocuments(durable.DocumentQuery{Scope: durable.DocumentScope{Kind: durable.ScopeSession}, At: durable.CurrentDocument(), Kind: first}, 10, nil).Items), []durable.ID{firstKindDocumentID})
}

func caseGlobalNamespace(r *conformanceRun) {
	rootID := r.createRoot()
	explicitEntryID := durable.ID(100)
	r.commit(durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(explicitEntryID), ConversationID: durable.ConversationID(rootID)}})
	if id := r.mint(); id != 101 {
		r.fail("minted ID = %d", id)
	}
	collision := pendingTaskRecord(explicitEntryID, rootID, "ready")
	r.expectError(fmt.Sprintf("ID %d already belongs to entry", explicitEntryID), func() error {
		_, err := r.storage.Commit(r.ctx, []durable.StorageWrite{{Type: durable.WriteTask, Task: &collision}})
		return err
	})

	r.commit(durable.StorageWrite{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: durable.EntryID(durable.MaxSafeInteger), ConversationID: durable.ConversationID(rootID), Kind: "last-id"}})
	if _, err := r.storage.MintID(r.ctx); err == nil {
		r.fail("minting after the ID space is exhausted was accepted")
	}
	if _, err := r.storage.MintID(r.ctx); err == nil {
		r.fail("minting after the ID space is exhausted was accepted twice")
	}
}

func caseAfterClose(r *conformanceRun) {
	r.createRoot()
	if err := r.storage.Close(r.ctx); err != nil {
		r.fail("close: %v", err)
	}
	if _, err := r.storage.Conversation(r.ctx, durable.RootConversationID); err == nil {
		r.fail("read after close was accepted")
	}
	if _, err := r.storage.Commit(r.ctx, []durable.StorageWrite{}); err == nil {
		r.fail("commit after close was accepted")
	}
	if _, err := r.storage.MintID(r.ctx); err == nil {
		r.fail("mint after close was accepted")
	}
}
