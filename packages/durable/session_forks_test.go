package durable_test

import (
	"context"
	"testing"

	d "github.com/minifish-org/pith/packages/durable"
)

func appendTestEntry(t *testing.T, session *d.Session, conversation d.ConversationID, kind string) d.EntryID {
	t.Helper()
	var id d.EntryID
	if err := session.Commit(context.Background(), func(tx d.Tx) error {
		entry, err := tx.AppendEntry(context.Background(), conversation, d.EntryDraft{Kind: kind})
		if err != nil {
			return err
		}
		id = entry.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestForkCopiesAsOfCurrentAndSkipsInitial(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	asOf := counterDefinition("past", d.HistoryRewindable, d.ForkAsOf)
	current := counterDefinition("present", d.HistoryLatest, d.ForkCurrent)
	initial := counterDefinition("initial", d.HistoryLatest, d.ForkInitial)
	definitions := []d.DocumentDefinition{asOf, current, initial}
	if err := session.Commit(ctx, func(tx d.Tx) error {
		for _, definition := range definitions {
			if err := setCount(ctx, tx, definition, conversationAddress(definition.Kind, d.RootConversationID), 0); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var cut d.EntryID
	if err := session.Commit(ctx, func(tx d.Tx) error {
		for _, definition := range definitions {
			if err := setCount(ctx, tx, definition, conversationAddress(definition.Kind, d.RootConversationID), 1); err != nil {
				return err
			}
		}
		entry, err := tx.AppendEntry(ctx, d.RootConversationID, d.EntryDraft{Kind: "cut"})
		if err != nil {
			return err
		}
		cut = entry.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.Commit(ctx, func(tx d.Tx) error {
		for _, definition := range definitions {
			if err := setCount(ctx, tx, definition, conversationAddress(definition.Kind, d.RootConversationID), 2); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var child d.ConversationID
	if err := session.Commit(ctx, func(tx d.Tx) error {
		conversation, err := tx.ForkConversation(ctx, d.RootConversationID, cut, d.ConversationOwnership{Kind: "ownerless"})
		if err != nil {
			return err
		}
		child = conversation.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	asOfValue, err := session.Snapshot(ctx, asOf, conversationAddress("past", child))
	if err != nil || countOf(t, asOfValue) != 1 {
		t.Fatalf("as-of fork %+v %v", asOfValue, err)
	}
	currentValue, err := session.Snapshot(ctx, current, conversationAddress("present", child))
	if err != nil || countOf(t, currentValue) != 2 {
		t.Fatalf("current fork %+v %v", currentValue, err)
	}
	initialValue, err := session.Snapshot(ctx, initial, conversationAddress("initial", child))
	if err != nil || initialValue != nil {
		t.Fatalf("initial fork eagerly created a copy: %+v %v", initialValue, err)
	}
	// A commit that touches the initial address initializes it lazily.
	if err := session.Commit(ctx, func(tx d.Tx) error {
		_, err := tx.Doc(ctx, initial, conversationAddress("initial", child), nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	initialValue, err = session.Snapshot(ctx, initial, conversationAddress("initial", child))
	if err != nil || countOf(t, initialValue) != 0 {
		t.Fatalf("initialized fork %+v %v", initialValue, err)
	}
	// The parent's as-of history remains readable.
	historic, err := session.SnapshotAsOf(ctx, asOf, conversationAddress("past", d.RootConversationID), cut)
	if err != nil || countOf(t, historic) != 1 {
		t.Fatalf("as-of parent %+v %v", historic, err)
	}
}

func TestForkRejectsInvisibleEntryAndRemainsUsable(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	err := session.Commit(ctx, func(tx d.Tx) error {
		_, e := tx.ForkConversation(ctx, d.RootConversationID, 999, d.ConversationOwnership{Kind: "ownerless"})
		return e
	})
	if err == nil {
		t.Fatal("fork at an invisible entry was accepted")
	}
	if err := session.Commit(ctx, func(tx d.Tx) error {
		_, e := tx.CreateConversation(ctx, d.ConversationOwnership{Kind: "ownerless"})
		return e
	}); err != nil {
		t.Fatalf("Session unusable after rejection: %v", err)
	}
}

func TestForkRejectsCurrentSourceWriteInForkTransaction(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	current := counterDefinition("present", d.HistoryLatest, d.ForkCurrent)
	address := conversationAddress("present", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, current, address, 0) }); err != nil {
		t.Fatal(err)
	}
	cut := appendTestEntry(t, session, d.RootConversationID, "cut")
	err := session.Commit(ctx, func(tx d.Tx) error {
		if _, e := tx.ForkConversation(ctx, d.RootConversationID, cut, d.ConversationOwnership{Kind: "ownerless"}); e != nil {
			return e
		}
		return setCount(ctx, tx, current, address, 1)
	})
	if err == nil {
		t.Fatal("changing a current-policy fork source in the fork transaction was accepted")
	}
	value, err := session.Snapshot(ctx, current, address)
	if err != nil || countOf(t, value) != 0 {
		t.Fatalf("rejected fork transaction leaked a write: %+v %v", value, err)
	}
}

func TestForkLeavesNonConversationDocumentsUncopied(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	sessionDoc := counterDefinition("settings", d.HistoryRewindable, d.ForkAsOf)
	sessionDoc.Scope = d.ScopeSession
	sessionAddress := d.DocumentAddress{Kind: "settings", Scope: d.DocumentScope{Kind: d.ScopeSession}}
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, sessionDoc, sessionAddress, 3) }); err != nil {
		t.Fatal(err)
	}
	cut := appendTestEntry(t, session, d.RootConversationID, "cut")
	if err := session.Commit(ctx, func(tx d.Tx) error {
		_, err := tx.ForkConversation(ctx, d.RootConversationID, cut, d.ConversationOwnership{Kind: "ownerless"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// The session-scoped document still lives only in its original scope.
	value, err := session.Snapshot(ctx, sessionDoc, sessionAddress)
	if err != nil || countOf(t, value) != 3 {
		t.Fatalf("session document changed by fork: %+v %v", value, err)
	}
}
