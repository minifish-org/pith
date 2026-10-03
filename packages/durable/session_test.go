package durable_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	d "github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/storage/memory"
)

// newTestSession opens a Session over a fresh memory storage that already owns
// the reserved root conversation.
func newTestSession(t *testing.T) (*d.Session, *memory.Storage) {
	t.Helper()
	storage := memory.New()
	if _, err := storage.Commit(context.Background(), []d.StorageWrite{{Type: d.WriteConversation, Conversation: &d.ConversationRecord{ID: d.RootConversationID}}}); err != nil {
		t.Fatal(err)
	}
	session := d.NewSession(storage)
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session, storage
}

func counterDefinition(kind, history, fork string) d.DocumentDefinition {
	return d.DocumentDefinition{
		Kind:    kind,
		Version: 1,
		Scope:   d.ScopeConversation,
		History: history,
		Fork:    fork,
		Initial: func(json.RawMessage) (d.JsonObject, error) { return d.JsonObject{"count": 0}, nil },
	}
}

func conversationAddress(kind string, id d.ConversationID) d.DocumentAddress {
	return d.DocumentAddress{Kind: kind, Scope: d.DocumentScope{Kind: d.ScopeConversation, ConversationID: id}}
}

func editDoc(ctx context.Context, tx d.Tx, definition d.DocumentDefinition, address d.DocumentAddress, edit func(d.JsonObject)) error {
	draft, err := tx.Doc(ctx, definition, address, nil)
	if err != nil {
		return err
	}
	value, err := draft.Value()
	if err != nil {
		return err
	}
	edit(value)
	return nil
}

func setCount(ctx context.Context, tx d.Tx, definition d.DocumentDefinition, address d.DocumentAddress, count int) error {
	return editDoc(ctx, tx, definition, address, func(value d.JsonObject) { value["count"] = count })
}

func countOf(t *testing.T, value d.JsonObject) int {
	t.Helper()
	if value == nil {
		t.Fatal("document value is nil")
	}
	data, err := json.Marshal(value["count"])
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := json.Unmarshal(data, &count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestSessionCallbackRollsBackEverything(t *testing.T) {
	session, storage := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("counter", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("counter", d.RootConversationID)
	sentinel := errors.New("callback rollback")
	var ghost d.EntryID
	err := session.Commit(ctx, func(tx d.Tx) error {
		if e := setCount(ctx, tx, definition, address, 1); e != nil {
			return e
		}
		entry, e := tx.AppendEntry(ctx, d.RootConversationID, d.EntryDraft{Kind: "rollback"})
		if e != nil {
			return e
		}
		ghost = entry.ID
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("lost callback error: %v", err)
	}
	value, err := session.Snapshot(ctx, definition, address)
	if err != nil || value != nil {
		t.Fatalf("rollback document leaked %+v %v", value, err)
	}
	entry, err := storage.Entry(ctx, ghost)
	if err != nil || entry != nil {
		t.Fatalf("rollback entry leaked %+v %v", entry, err)
	}
}

func TestSessionMutationLineSerializesConcurrentCommits(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("counter", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("counter", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	const workers = 32
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results <- session.Commit(ctx, func(tx d.Tx) error {
				draft, err := tx.Doc(ctx, definition, address, nil)
				if err != nil {
					return err
				}
				value, err := draft.Value()
				if err != nil {
					return err
				}
				value["count"] = countOf(t, value) + 1
				return nil
			})
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	value, err := session.Snapshot(ctx, definition, address)
	if err != nil || countOf(t, value) != workers {
		t.Fatalf("lost concurrent updates %+v %v", value, err)
	}
}

type failingCommitStorage struct {
	d.Storage
	next error
}

func (s *failingCommitStorage) Commit(ctx context.Context, writes []d.StorageWrite) (d.Seq, error) {
	if s.next != nil {
		err := s.next
		s.next = nil
		return 0, err
	}
	return s.Storage.Commit(ctx, writes)
}

func TestSessionKnownRejectionContinuesUncertainFailurePoisons(t *testing.T) {
	for _, known := range []bool{true, false} {
		name := "uncertain-failure"
		if known {
			name = "known-rejection"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			base := memory.New()
			if _, err := base.Commit(ctx, []d.StorageWrite{{Type: d.WriteConversation, Conversation: &d.ConversationRecord{ID: d.RootConversationID}}}); err != nil {
				t.Fatal(err)
			}
			var failure error = errors.New("uncertain fixture failure")
			if known {
				failure = &d.StorageRejected{Message: "definitely no effect"}
			}
			session := d.NewSession(&failingCommitStorage{Storage: base, next: failure})
			defer session.Close(ctx)
			definition := counterDefinition("rejected", d.HistoryRewindable, d.ForkAsOf)
			address := conversationAddress("rejected", d.RootConversationID)
			if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 1) }); err == nil {
				t.Fatal("storage failure was hidden")
			}
			err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 2) })
			if known && err != nil {
				t.Fatalf("known rejection poisoned the Session: %v", err)
			}
			if !known && err == nil {
				t.Fatal("uncertain failure allowed a later commit")
			}
		})
	}
}

func TestSessionReadAfterWrite(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	err := session.Commit(ctx, func(tx d.Tx) error {
		if _, e := tx.AppendEntry(ctx, d.RootConversationID, d.EntryDraft{Kind: "staged"}); e != nil {
			return e
		}
		_, e := tx.Conversation(ctx, d.RootConversationID)
		return e
	})
	var ordering *d.ReadAfterWrite
	if !errors.As(err, &ordering) {
		t.Fatalf("table read after the first write was accepted: %v", err)
	}
	// Document read-your-writes remains legal after a table write.
	definition := counterDefinition("rw", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("rw", d.RootConversationID)
	committedValue := d.JsonObject(nil)
	if err := session.Commit(ctx, func(tx d.Tx) error {
		if _, e := tx.AppendEntry(ctx, d.RootConversationID, d.EntryDraft{Kind: "write"}); e != nil {
			return e
		}
		if e := setCount(ctx, tx, definition, address, 3); e != nil {
			return e
		}
		value, e := tx.Doc(ctx, definition, address, nil)
		if e != nil {
			return e
		}
		committedValue, e = value.Value()
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if countOf(t, committedValue) != 3 {
		t.Fatalf("document read-your-writes lost the draft: %+v", committedValue)
	}
}

type gatedCommitStorage struct {
	d.Storage
	entered chan context.Context
	release chan struct{}
}

func (s *gatedCommitStorage) Commit(ctx context.Context, writes []d.StorageWrite) (d.Seq, error) {
	s.entered <- ctx
	<-s.release
	return s.Storage.Commit(ctx, writes)
}

func TestSessionCancellationAfterAdmissionStillCommits(t *testing.T) {
	base := memory.New()
	ctx := context.Background()
	if _, err := base.Commit(ctx, []d.StorageWrite{{Type: d.WriteConversation, Conversation: &d.ConversationRecord{ID: d.RootConversationID}}}); err != nil {
		t.Fatal(err)
	}
	storage := &gatedCommitStorage{Storage: base, entered: make(chan context.Context, 1), release: make(chan struct{})}
	session := d.NewSession(storage)
	defer session.Close(ctx)
	caller, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- session.Commit(caller, func(tx d.Tx) error {
			_, err := tx.AppendEntry(caller, d.RootConversationID, d.EntryDraft{Kind: "admitted"})
			return err
		})
	}()
	settlementContext := <-storage.entered
	cancel()
	if err := settlementContext.Err(); err != nil {
		close(storage.release)
		<-done
		t.Fatalf("storage settlement inherited caller cancellation: %v", err)
	}
	close(storage.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	page, err := base.ScanEntries(ctx, d.EntryQuery{ConversationID: d.RootConversationID}, 10, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].Kind != "admitted" {
		t.Fatalf("admitted commit was undone: %+v %v", page, err)
	}

	preCancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	called := false
	if err := session.Commit(preCancelled, func(d.Tx) error { called = true; return nil }); err == nil || called {
		t.Fatalf("pre-admission cancellation admitted the callback: %v called=%v", err, called)
	}
}

func TestSessionPublishesCommittedRecords(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	definitions := []d.DocumentDefinition{counterDefinition("a", d.HistoryRewindable, d.ForkAsOf)}
	address := conversationAddress("a", d.RootConversationID)
	publications := []d.CommitPublication{}
	unsubscribe := session.SubscribeCommits(func(publication d.CommitPublication) { publications = append(publications, publication) })
	defer unsubscribe()
	if err := session.Commit(ctx, func(tx d.Tx) error {
		if e := setCount(ctx, tx, definitions[0], address, 1); e != nil {
			return e
		}
		_, e := tx.AppendEntry(ctx, d.RootConversationID, d.EntryDraft{Kind: "published"})
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if len(publications) != 1 {
		t.Fatalf("expected one publication, got %d", len(publications))
	}
	publication := publications[0]
	if publication.Seq < 1 {
		t.Fatalf("publication sequence not positive: %d", publication.Seq)
	}
	sawEntry := false
	sawDocument := false
	for _, change := range publication.Changes {
		switch change.Type {
		case d.WriteEntry:
			sawEntry = change.Write != nil && change.Write.Entry != nil && change.Write.Entry.Kind == "published"
		case "document":
			sawDocument = change.Record != nil && change.Value != nil
		}
	}
	if !sawEntry || !sawDocument {
		t.Fatalf("publication omitted entry=%v document=%v: %+v", sawEntry, sawDocument, publication.Changes)
	}
}

func TestSessionNoOpCommitStillAdoptsWithoutStorageWrite(t *testing.T) {
	session, storage := newTestSession(t)
	ctx := context.Background()
	definition := counterDefinition("counter", d.HistoryRewindable, d.ForkAsOf)
	address := conversationAddress("counter", d.RootConversationID)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setCount(ctx, tx, definition, address, 0) }); err != nil {
		t.Fatal(err)
	}
	before, err := storage.FindDocument(ctx, address, d.CurrentDocument())
	if err != nil || before == nil {
		t.Fatalf("document missing: %v", err)
	}
	// An unchanged draft preparation emits an empty batch: no storage commit.
	if err := session.Commit(ctx, func(tx d.Tx) error {
		if _, e := tx.Doc(ctx, definition, address, nil); e != nil {
			return e
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	value, err := session.Snapshot(ctx, definition, address)
	if err != nil || countOf(t, value) != 0 {
		t.Fatalf("no-op commit changed the value: %+v %v", value, err)
	}
}

func TestSessionCloseSealsAdmissionAndFiresListeners(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	closed := make(chan struct{})
	session.SubscribeClose(func() { close(closed) })
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close listener did not fire")
	}
	if err := session.Commit(ctx, func(d.Tx) error { return nil }); err == nil {
		t.Fatal("commit after close was admitted")
	}
	// Close is idempotent.
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSessionCreatesTablesAndValidatesConversationOwner(t *testing.T) {
	session, _ := newTestSession(t)
	ctx := context.Background()
	var child d.ConversationID
	if err := session.Commit(ctx, func(tx d.Tx) error {
		conversation, err := tx.CreateConversation(ctx, d.ConversationOwnership{Kind: "ownerless"})
		if err != nil {
			return err
		}
		entry, err := tx.AppendEntry(ctx, conversation.ID, d.EntryDraft{Kind: "hello"})
		if err != nil {
			return err
		}
		if entry.ConversationID != conversation.ID {
			t.Fatalf("entry landed in the wrong conversation")
		}
		child = conversation.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if child == 0 {
		t.Fatal("conversation was not created")
	}
	if err := session.Commit(ctx, func(tx d.Tx) error {
		_, err := tx.CreateConversation(ctx, d.ConversationOwnership{Kind: "task", TaskID: 999})
		return err
	}); err == nil {
		t.Fatal("missing conversation owner was accepted")
	}
}
