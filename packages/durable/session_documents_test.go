package durable_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/minifish-org/pith/packages/chord"
	d "github.com/minifish-org/pith/packages/durable"
)

func TestDocumentCreateSnapshotAndDetachment(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("doc", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("doc", d.RootConversationID)

	absent, err := session.Snapshot(ctx, definition, address)
	if err != nil || absent != nil {
		t.Fatalf("snapshot created a document: %+v %v", absent, err)
	}
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 1) }); err != nil {
		t.Fatal(err)
	}
	value, err := session.Snapshot(ctx, definition, address)
	if err != nil || countOf(t, value) != 1 {
		t.Fatalf("created value %+v %v", value, err)
	}
	// Snapshots are detached: mutating one never changes committed state.
	value["count"] = 99
	again, err := session.Snapshot(ctx, definition, address)
	if err != nil || countOf(t, again) != 1 {
		t.Fatalf("snapshot alias corrupted committed state: %+v %v", again, err)
	}
}

func TestDocumentDraftIsSealedAndDetachedAtSettlement(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("seal", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("seal", d.RootConversationID)
	var escaped *d.DocumentDraft
	var alias d.JsonObject
	if err := session.Commit(ctx, func(tx d.Tx) error {
		var err error
		escaped, err = tx.Doc(ctx, definition, address, nil)
		if err != nil {
			return err
		}
		alias, err = escaped.Value()
		if err != nil {
			return err
		}
		alias["count"] = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.Value(); err == nil {
		t.Fatal("settled draft handle remained readable")
	}
	// Mutating the escaped draft alias cannot reach the committed revision.
	alias["count"] = 77
	value, err := session.Snapshot(ctx, definition, address)
	if err != nil || countOf(t, value) != 1 {
		t.Fatalf("escaped alias corrupted committed state: %+v %v", value, err)
	}
}

func TestDocumentVersionTransitionWritesRequiredBase(t *testing.T) {
	session, storage := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("versioned", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("versioned", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 4) }); err != nil {
		t.Fatal(err)
	}
	record, err := storage.FindDocument(ctx, address, d.CurrentDocument())
	if err != nil || record == nil {
		t.Fatalf("document missing: %v", err)
	}

	migrated := definition
	migrated.Version = 2
	migrated.Migrate = func(value d.JsonObject, from int) (d.JsonObject, error) {
		if from != 1 {
			return nil, errors.New("unexpected source version")
		}
		value["count"] = countOf(t, value) + 10
		return value, nil
	}
	// A read-only snapshot migrates in memory and writes nothing.
	value, err := session.Snapshot(ctx, migrated, address)
	if err != nil || countOf(t, value) != 14 {
		t.Fatalf("read-only migration %+v %v", value, err)
	}
	stored, err := storage.Document(ctx, record.ID, d.CurrentDocument())
	if err != nil || stored == nil || stored.Version != 1 {
		t.Fatalf("read-only migration wrote storage: %+v %v", stored, err)
	}
	// The first successful mutable access persists the new-version base even
	// without content edits.
	if err := session.Commit(ctx, func(tx d.Tx) error {
		_, err := tx.Doc(ctx, migrated, address, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	stored, err = storage.Document(ctx, record.ID, d.CurrentDocument())
	if err != nil || stored == nil || stored.Version != 2 || stored.DeltasSinceBase != 0 {
		t.Fatalf("migration did not persist a base: %+v %v", stored, err)
	}
}

func TestDocumentRejectsUnavailableMigration(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("newer", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("newer", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 1) }); err != nil {
		t.Fatal(err)
	}
	// A newer definition without Migrate cannot consume the older stored
	// version.
	mismatched := definition
	mismatched.Version = 2
	if _, err := session.Snapshot(ctx, mismatched, address); err == nil {
		t.Fatal("older stored version without migration was accepted")
	}
	if err := session.Commit(ctx, func(tx d.Tx) error {
		_, err := tx.Doc(ctx, mismatched, address, nil)
		return err
	}); err == nil {
		t.Fatal("mutable access without a migration was accepted")
	}
}

func TestDocumentCheckpointSelectionAndRollback(t *testing.T) {
	session, storage := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("checkpoint", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("checkpoint", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	record, err := storage.FindDocument(ctx, address, d.CurrentDocument())
	if err != nil || record == nil {
		t.Fatal(err)
	}

	calls := 0
	selecting := definition
	selecting.CheckpointWhen = func(_ d.JsonObject, ops []chord.Op, info d.CheckpointInfo) (bool, error) {
		calls++
		if len(ops) == 0 {
			t.Fatal("checkpoint predicate ran for an empty operation batch")
		}
		return true, nil
	}
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, selecting, address, 1) }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("checkpoint predicate ran %d times", calls)
	}
	stored, err := storage.Document(ctx, record.ID, d.CurrentDocument())
	if err != nil || stored == nil || stored.DeltasSinceBase != 0 {
		t.Fatalf("selecting base was not stored: %+v %v", stored, err)
	}

	sentinel := errors.New("checkpoint rejected")
	rejecting := definition
	rejecting.CheckpointWhen = func(d.JsonObject, []chord.Op, d.CheckpointInfo) (bool, error) {
		return false, sentinel
	}
	err = session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, rejecting, address, 2) })
	if !errors.Is(err, sentinel) {
		t.Fatalf("checkpoint error lost: %v", err)
	}
	value, err := session.Snapshot(ctx, definition, address)
	if err != nil || countOf(t, value) != 1 {
		t.Fatalf("checkpoint failure leaked: %+v %v", value, err)
	}
}

func TestDocumentRetireAndRecreateIncarnation(t *testing.T) {
	session, storage := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("retire", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("retire", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 1) }); err != nil {
		t.Fatal(err)
	}
	first, err := storage.FindDocument(ctx, address, d.CurrentDocument())
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if err := session.Commit(ctx, func(tx d.Tx) error { return tx.RetireDoc(ctx, definition, address) }); err != nil {
		t.Fatal(err)
	}
	absent, err := session.Snapshot(ctx, definition, address)
	if err != nil || absent != nil {
		t.Fatalf("retired document still readable: %+v %v", absent, err)
	}
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 5) }); err != nil {
		t.Fatal(err)
	}
	second, err := storage.FindDocument(ctx, address, d.CurrentDocument())
	if err != nil || second == nil || second.ID == first.ID {
		t.Fatalf("recreation reused the incarnation: %+v %v", second, err)
	}
	value, err := session.Snapshot(ctx, definition, address)
	if err != nil || countOf(t, value) != 5 {
		t.Fatalf("recreated value %+v %v", value, err)
	}
}

func TestDocumentFamilySeedUsedOncePerAbsentAddress(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	kind := "family"
	key := "member"
	family := d.DocumentDefinition{
		Kind:    kind,
		Version: 1,
		Scope:   d.ScopeConversation,
		History: d.HistoryRewindable,
		Fork:    d.ForkAsOf,
		Family:  true,
		Initial: func(seed json.RawMessage) (d.JsonObject, error) {
			return d.JsonObject{"seed": string(seed)}, nil
		},
	}
	address := d.DocumentAddress{Kind: kind, Scope: d.DocumentScope{Kind: d.ScopeConversation, ConversationID: d.RootConversationID}, Key: &key}
	if err := session.Commit(ctx, func(tx d.Tx) error {
		if _, err := tx.Doc(ctx, family, address, json.RawMessage(`"first"`)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A second seed for an existing member is ignored.
	if err := session.Commit(ctx, func(tx d.Tx) error {
		if _, err := tx.Doc(ctx, family, address, json.RawMessage(`"second"`)); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	value, err := session.Snapshot(ctx, family, address)
	if err != nil || value["seed"] != string(json.RawMessage(`"first"`)) {
		t.Fatalf("family seed %+v %v", value, err)
	}
}
