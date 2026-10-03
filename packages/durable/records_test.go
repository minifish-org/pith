package durable_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/minifish-org/pith/packages/ai/types"
	"github.com/minifish-org/pith/packages/chord"
	"github.com/minifish-org/pith/packages/durable"
)

// TestIDAndSequenceBrandErasure ports the runtime-visible half of upstream
// types.test.ts "brands numeric IDs by record kind and carries task result
// types". Go cannot express the erased nominal brand at compile time, so the
// documented adaptation is that IDs are int64 aliases produced by IDFromNumber
// and commit sequences by SeqFromNumber.
func TestIDAndSequenceBrandErasure(t *testing.T) {
	conversationID := durable.IDFromNumber(1)
	taskID := durable.IDFromNumber(4)
	entryID := durable.IDFromNumber(2)
	seq := durable.SeqFromNumber(1)

	if conversationID != durable.ConversationID(1) {
		t.Fatalf("conversation id = %d", conversationID)
	}
	if taskID != durable.TaskID(4) {
		t.Fatalf("task id = %d", taskID)
	}
	if entryID != durable.EntryID(2) {
		t.Fatalf("entry id = %d", entryID)
	}
	if seq != durable.Seq(1) {
		t.Fatalf("seq = %d", seq)
	}
	encoded, err := json.Marshal(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "4" {
		t.Fatalf("task id JSON = %s", encoded)
	}
	if durable.RootConversationID != durable.ConversationID(1) {
		t.Fatalf("root id = %d", durable.RootConversationID)
	}
	if durable.MaxSafeInteger != 9007199254740991 {
		t.Fatalf("max safe integer = %d", durable.MaxSafeInteger)
	}
}

func ptr[T any](value T) *T { return &value }

// TestRecordJSONFieldNames ports the runtime half of upstream types.test.ts
// "encodes discriminator-dependent fields": records serialize with lower-camel
// upstream field names and absent optionals are omitted.
func TestRecordJSONFieldNames(t *testing.T) {
	entry := durable.EntryRecord{
		ID:             2,
		ConversationID: 1,
		Kind:           "note",
		Data:           json.RawMessage(`{"a":1}`),
		Head:           ptr(durable.EntryID(5)),
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "conversationId", "kind", "data", "head"} {
		if _, present := fields[key]; !present {
			t.Fatalf("entry JSON %s is missing %q", encoded, key)
		}
	}
	for _, key := range []string{"model", "edits", "byTaskId"} {
		if _, present := fields[key]; present {
			t.Fatalf("entry JSON %s unexpectedly carries %q", encoded, key)
		}
	}

	task := durable.TaskRecord{
		ID:             3,
		ConversationID: 1,
		Kind:           "test",
		Version:        1,
		Input:          json.RawMessage(`{"value":3}`),
		State:          durable.TaskState{Status: durable.TaskStatusPending, Checkpoint: json.RawMessage(`{"phase":"ready"}`)},
	}
	encoded, err = json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, encoded), decodeJSON(t, []byte(`{"id":3,"conversationId":1,"kind":"test","version":1,"input":{"value":3},"background":false,"abortRequested":false,"state":{"status":"pending","checkpoint":{"phase":"ready"}}}`))) {
		t.Fatalf("task JSON = %s", encoded)
	}

	terminal := durable.TaskRecord{
		ID:             3,
		ConversationID: 1,
		Kind:           "test",
		Version:        1,
		Input:          json.RawMessage(`{"value":3}`),
		State:          durable.TaskState{Status: durable.TaskStatusTerminal, Outcome: &durable.TaskOutcome{Status: durable.OutcomeCompleted, Result: json.RawMessage(`1`)}},
	}
	encoded, err = json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	var terminalFields map[string]any
	if err := json.Unmarshal(encoded, &terminalFields); err != nil {
		t.Fatal(err)
	}
	if _, present := terminalFields["memos"]; present {
		t.Fatalf("terminal task JSON %s unexpectedly carries memos", encoded)
	}
	state := terminalFields["state"].(map[string]any)
	if _, present := state["checkpoint"]; present {
		t.Fatalf("terminal task state %s unexpectedly carries a checkpoint", encoded)
	}

	doneInput := durable.SubmissionRecord{
		ID:             5,
		ConversationID: 1,
		Type:           durable.SubmissionTypeInput,
		Status:         durable.SubmissionStatusDone,
		Entry:          ptr(durable.EntryID(2)),
		Answer:         ptr(durable.EntryID(3)),
	}
	encoded, err = json.Marshal(doneInput)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, encoded), decodeJSON(t, []byte(`{"id":5,"conversationId":1,"type":"input","status":"done","entry":2,"answer":3}`))) {
		t.Fatalf("done input JSON = %s", encoded)
	}
	if doneInput.Answer == nil || *doneInput.Answer != durable.EntryID(3) {
		t.Fatalf("done input answer = %v", doneInput.Answer)
	}

	doneWrite := durable.SubmissionRecord{
		ID:             5,
		ConversationID: 1,
		Type:           durable.SubmissionTypeWrite,
		Status:         durable.SubmissionStatusDone,
		Entry:          ptr(durable.EntryID(2)),
	}
	encoded, err = json.Marshal(doneWrite)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, encoded), decodeJSON(t, []byte(`{"id":5,"conversationId":1,"type":"write","status":"done","entry":2}`))) {
		t.Fatalf("done write JSON = %s", encoded)
	}

	base := durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"count": 1}}
	encoded, err = json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, encoded), decodeJSON(t, []byte(`{"kind":"base","version":1,"value":{"count":1}}`))) {
		t.Fatalf("base content JSON = %s", encoded)
	}
	delta := durable.DocumentContent{Kind: durable.ContentDelta, Version: 1, Ops: []chord.Op{{"s", []any{"count"}, 2}}}
	encoded, err = json.Marshal(delta)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, encoded), decodeJSON(t, []byte(`{"kind":"delta","version":1,"ops":[["s",["count"],2]]}`))) {
		t.Fatalf("delta content JSON = %s", encoded)
	}

	conversation := durable.DocumentCreate{
		ID:      6,
		Kind:    "test",
		Scope:   durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: 1},
		History: durable.HistoryRewindable,
		Fork:    durable.ForkAsOf,
	}
	encoded, err = json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, encoded), decodeJSON(t, []byte(`{"id":6,"kind":"test","scope":{"kind":"conversation","conversationId":1},"history":"rewindable","fork":"asOf"}`))) {
		t.Fatalf("conversation document JSON = %s", encoded)
	}
	if conversation.Fork != durable.ForkAsOf {
		t.Fatalf("conversation fork = %s", conversation.Fork)
	}
}

func TestContextEditDiscriminator(t *testing.T) {
	omit := durable.ContextEdit{Target: 2, Action: "omit"}
	encoded, err := json.Marshal(omit)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, encoded), decodeJSON(t, []byte(`{"target":2,"action":"omit"}`))) {
		t.Fatalf("omit edit JSON = %s", encoded)
	}
	// Go adaptation: an empty messages slice is omitted on the wire; only
	// non-empty replacement messages are serialized.
	replace := durable.ContextEdit{Target: 2, Action: "replace", Messages: []types.Message{}}
	encoded, err = json.Marshal(replace)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decodeJSON(t, encoded), decodeJSON(t, []byte(`{"target":2,"action":"replace"}`))) {
		t.Fatalf("replace edit JSON = %s", encoded)
	}
	if replace.Action != "replace" {
		t.Fatalf("replace action = %s", replace.Action)
	}
}

func TestDocumentPointJSON(t *testing.T) {
	current, err := json.Marshal(durable.CurrentDocument())
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != `"current"` {
		t.Fatalf("current point = %s", current)
	}
	at, err := json.Marshal(durable.AtSeq(7))
	if err != nil {
		t.Fatal(err)
	}
	if string(at) != "7" {
		t.Fatalf("at point = %s", at)
	}
	var decoded durable.DocumentPoint
	if err := json.Unmarshal([]byte(`"current"`), &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.Current {
		t.Fatalf("decoded current = %+v", decoded)
	}
	if err := json.Unmarshal([]byte("9"), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Current || decoded.Seq != 9 {
		t.Fatalf("decoded at = %+v", decoded)
	}
}

func TestDefinitionAndEntryValidation(t *testing.T) {
	if _, err := durable.DefineDoc(durable.DocumentDefinition{Kind: "bad", Version: 0}); err == nil {
		t.Fatal("non-positive document version accepted")
	}
	definition, err := durable.DefineDoc(durable.DocumentDefinition{
		Kind:    "notes",
		Version: 1,
		Scope:   durable.ScopeSession,
	})
	if err != nil {
		t.Fatal(err)
	}
	if definition == nil || definition.Family {
		t.Fatalf("singleton definition = %+v", definition)
	}
	family, err := durable.DefineDocFamily(durable.DocumentDefinition{
		Kind:    "family",
		Version: 1,
		Scope:   durable.ScopeConversation,
		History: durable.HistoryRewindable,
		Fork:    durable.ForkAsOf,
	})
	if err != nil {
		t.Fatal(err)
	}
	if family == nil || !family.Family {
		t.Fatalf("family definition = %+v", family)
	}

	if _, err := durable.DefineEntry(""); err == nil {
		t.Fatal("empty entry kind accepted")
	}
	kind, err := durable.DefineEntry("app.note")
	if err != nil {
		t.Fatal(err)
	}
	record := &durable.EntryRecord{Kind: "app.note"}
	if !kind.Is(record) {
		t.Fatal("entry kind did not match")
	}
	if kind.Is(&durable.EntryRecord{Kind: "other"}) || kind.Is(nil) {
		t.Fatal("entry kind matched incorrectly")
	}
	if durable.UserEntry.Kind != "pi.user" || durable.ToolResultEntry.Kind != "pi.tool-result" {
		t.Fatalf("built-in entry kinds = %s %s", durable.UserEntry.Kind, durable.ToolResultEntry.Kind)
	}
}

func TestAddressIdentityDistinguishesAbsentAndEmptyKey(t *testing.T) {
	singleton := durable.DocumentAddress{Kind: "family", Scope: durable.DocumentScope{Kind: durable.ScopeSession}}
	emptyKey := durable.DocumentAddress{Kind: "family", Scope: durable.DocumentScope{Kind: durable.ScopeSession}, Key: ptr("")}
	if durable.AddressID(singleton) == durable.AddressID(emptyKey) {
		t.Fatal("absent and empty document keys share an address identity")
	}
	if durable.ScopeKey(durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: 3}) == durable.ScopeKey(durable.DocumentScope{Kind: durable.ScopeConversation, ConversationID: 4}) {
		t.Fatal("distinct conversation scopes share an identity")
	}
}

func decodeJSON(t *testing.T, data []byte) any {
	t.Helper()
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return out
}
