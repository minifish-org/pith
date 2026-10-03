package durable_session_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/minifish-org/pith/packages/chord"
	d "github.com/minifish-org/pith/packages/durable"
	"github.com/minifish-org/pith/packages/durable/storage/memory"
)

func sessionFixture(t *testing.T) (*d.Session, d.Storage) {
	t.Helper()
	s := memory.New()
	if _, err := s.Commit(context.Background(), []d.StorageWrite{{Type: "conversation", Conversation: &d.ConversationRecord{ID: 1}}}); err != nil {
		t.Fatal(err)
	}
	session := d.NewSession(s)
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session, s
}
func def(kind, history, fork string) d.DocumentDefinition {
	return d.DocumentDefinition{Kind: kind, Version: 1, Scope: "conversation", History: history, Fork: fork, Initial: func(json.RawMessage) (d.JsonObject, error) { return d.JsonObject{"count": 7}, nil }}
}
func address(kind string, conversation d.ID) d.DocumentAddress {
	return d.DocumentAddress{Kind: kind, Scope: d.DocumentScope{Kind: "conversation", ConversationID: conversation}}
}
func count(t *testing.T, value d.JsonObject) int {
	t.Helper()
	data, err := json.Marshal(value["count"])
	if err != nil {
		t.Fatal(err)
	}
	var result int
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func setDoc(ctx context.Context, tx d.Tx, definition d.DocumentDefinition, addr d.DocumentAddress, n int) error {
	draft, err := tx.Doc(ctx, definition, addr, nil)
	if err != nil {
		return err
	}
	value, err := draft.Value()
	if err != nil {
		return err
	}
	value["count"] = n
	return nil
}

func TestPortsmithJudgeDurableSessionRollbackAndDraft(t *testing.T) {
	session, s := sessionFixture(t)
	ctx := context.Background()
	definition := def("counter", "rewindable", "asOf")
	addr := address("counter", 1)
	before, err := session.Snapshot(ctx, definition, addr)
	if err != nil || before != nil {
		t.Fatalf("lookup created document %+v %v", before, err)
	}
	sentinel := errors.New("callback rollback")
	var ghost d.ID
	err = session.Commit(ctx, func(tx d.Tx) error {
		if e := setDoc(ctx, tx, definition, addr, 1); e != nil {
			return e
		}
		entry, e := tx.AppendEntry(ctx, 1, d.EntryDraft{Kind: "rollback"})
		if e != nil {
			return e
		}
		ghost = entry.ID
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("lost callback error %v", err)
	}
	value, err := session.Snapshot(ctx, definition, addr)
	if err != nil || value != nil {
		t.Fatalf("rollback document leaked %+v %v", value, err)
	}
	entry, err := s.Entry(ctx, ghost)
	if err != nil || entry != nil {
		t.Fatalf("rollback entry leaked %+v %v", entry, err)
	}
	var escaped *d.DocumentDraft
	var alias d.JsonObject
	if err = session.Commit(ctx, func(tx d.Tx) error {
		var e error
		escaped, e = tx.Doc(ctx, definition, addr, nil)
		if e != nil {
			return e
		}
		alias, e = escaped.Value()
		if e != nil {
			return e
		}
		alias["count"] = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = escaped.Value(); err == nil {
		t.Fatal("settled draft handle remained mutable")
	}
	alias["count"] = 99
	value, err = session.Snapshot(ctx, definition, addr)
	if err != nil || count(t, value) != 1 {
		t.Fatalf("escaped alias corrupted adopted state %+v %v", value, err)
	}
	value["count"] = 88
	value, err = session.Snapshot(ctx, definition, addr)
	if err != nil || count(t, value) != 1 {
		t.Fatalf("snapshot alias corrupted state %+v %v", value, err)
	}
}

func TestPortsmithJudgeDurableSessionConcurrentMutationLine(t *testing.T) {
	session, _ := sessionFixture(t)
	ctx := context.Background()
	definition := def("counter", "rewindable", "asOf")
	addr := address("counter", 1)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setDoc(ctx, tx, definition, addr, 0) }); err != nil {
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
				draft, e := tx.Doc(ctx, definition, addr, nil)
				if e != nil {
					return e
				}
				value, e := draft.Value()
				if e != nil {
					return e
				}
				b, e := json.Marshal(value["count"])
				if e != nil {
					return e
				}
				var n int
				if e = json.Unmarshal(b, &n); e != nil {
					return e
				}
				value["count"] = n + 1
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
	value, err := session.Snapshot(ctx, definition, addr)
	if err != nil || count(t, value) != workers {
		t.Fatalf("lost concurrent updates %+v %v", value, err)
	}
}

type admittedStorage struct {
	d.Storage
	entered chan context.Context
	release chan struct{}
}

type failureStorage struct {
	d.Storage
	next error
}

func (s *failureStorage) Commit(ctx context.Context, writes []d.StorageWrite) (d.Seq, error) {
	if s.next != nil {
		err := s.next
		s.next = nil
		return 0, err
	}
	return s.Storage.Commit(ctx, writes)
}

func TestPortsmithJudgeDurableSessionStorageRejectionVsPoison(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "known-rejection", false: "uncertain-failure"}[known], func(t *testing.T) {
			ctx := context.Background()
			base := memory.New()
			if _, err := base.Commit(ctx, []d.StorageWrite{{Type: "conversation", Conversation: &d.ConversationRecord{ID: 1}}}); err != nil {
				t.Fatal(err)
			}
			var failure error = errors.New("uncertain fixture failure")
			if known {
				failure = &d.StorageRejected{Message: "definitely no effects"}
			}
			storage := &failureStorage{Storage: base, next: failure}
			session := d.NewSession(storage)
			defer session.Close(ctx)
			definition := def("rejected", "rewindable", "asOf")
			addr := address("rejected", 1)
			if err := session.Commit(ctx, func(tx d.Tx) error { return setDoc(ctx, tx, definition, addr, 1) }); err == nil {
				t.Fatal("storage failure was hidden")
			}
			err := session.Commit(ctx, func(tx d.Tx) error { return setDoc(ctx, tx, definition, addr, 2) })
			if known && err != nil {
				t.Fatalf("known rejection poisoned Session: %v", err)
			}
			if !known && err == nil {
				t.Fatal("uncertain storage error allowed a later commit")
			}
		})
	}
}

func TestPortsmithJudgeDurableSessionReadAfterWriteAndCheckpointRollback(t *testing.T) {
	session, _ := sessionFixture(t)
	ctx := context.Background()
	err := session.Commit(ctx, func(tx d.Tx) error {
		if _, e := tx.AppendEntry(ctx, 1, d.EntryDraft{Kind: "staged"}); e != nil {
			return e
		}
		_, e := tx.Conversation(ctx, 1)
		return e
	})
	var ordering *d.ReadAfterWrite
	if !errors.As(err, &ordering) {
		t.Fatalf("table read after first write accepted: %v", err)
	}
	definition := def("checkpoint", "rewindable", "asOf")
	addr := address("checkpoint", 1)
	if err = session.Commit(ctx, func(tx d.Tx) error { return setDoc(ctx, tx, definition, addr, 0) }); err != nil {
		t.Fatal(err)
	}
	calls := 0
	sentinel := errors.New("checkpoint rejected")
	definition.CheckpointWhen = func(d.JsonObject, []chord.Op, d.CheckpointInfo) (bool, error) { calls++; return false, sentinel }
	err = session.Commit(ctx, func(tx d.Tx) error { return setDoc(ctx, tx, definition, addr, 1) })
	if !errors.Is(err, sentinel) || calls != 1 {
		t.Fatalf("checkpoint policy calls=%d err=%v", calls, err)
	}
	value, err := session.Snapshot(ctx, definition, addr)
	if err != nil || count(t, value) != 0 {
		t.Fatalf("checkpoint failure leaked %+v %v", value, err)
	}
}

func (s *admittedStorage) Commit(ctx context.Context, w []d.StorageWrite) (d.Seq, error) {
	s.entered <- ctx
	<-s.release
	return s.Storage.Commit(ctx, w)
}

func TestPortsmithJudgeDurableSessionCancellationAfterAdmission(t *testing.T) {
	base := memory.New()
	ctx := context.Background()
	if _, err := base.Commit(ctx, []d.StorageWrite{{Type: "conversation", Conversation: &d.ConversationRecord{ID: 1}}}); err != nil {
		t.Fatal(err)
	}
	storage := &admittedStorage{Storage: base, entered: make(chan context.Context, 1), release: make(chan struct{})}
	session := d.NewSession(storage)
	defer session.Close(ctx)
	caller, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- session.Commit(caller, func(tx d.Tx) error { _, err := tx.AppendEntry(caller, 1, d.EntryDraft{Kind: "admitted"}); return err })
	}()
	storageContext := <-storage.entered
	cancel()
	if err := storageContext.Err(); err != nil {
		close(storage.release)
		<-done
		t.Fatalf("storage settlement inherited caller cancellation: %v", err)
	}
	close(storage.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	page, err := base.ScanEntries(ctx, d.EntryQuery{ConversationID: 1}, 10, nil)
	if err != nil || len(page.Items) != 1 || page.Items[0].Kind != "admitted" {
		t.Fatalf("admitted commit undone %+v %v", page, err)
	}
	cancelled, cancel2 := context.WithCancel(ctx)
	cancel2()
	called := false
	if err = session.Commit(cancelled, func(d.Tx) error { called = true; return nil }); err == nil || called {
		t.Fatalf("pre-admission cancellation admitted callback: %v called=%v", err, called)
	}
}

func TestPortsmithJudgeDurableSessionCheckpointMigrationAndFork(t *testing.T) {
	session, s := sessionFixture(t)
	ctx := context.Background()
	definitions := []d.DocumentDefinition{def("past", "rewindable", "asOf"), def("present", "latest", "current"), def("initial", "latest", "initial")}
	if err := session.Commit(ctx, func(tx d.Tx) error {
		for _, definition := range definitions {
			if e := setDoc(ctx, tx, definition, address(definition.Kind, 1), 0); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var cut d.ID
	if err := session.Commit(ctx, func(tx d.Tx) error {
		for _, definition := range definitions {
			if e := setDoc(ctx, tx, definition, address(definition.Kind, 1), 1); e != nil {
				return e
			}
		}
		entry, e := tx.AppendEntry(ctx, 1, d.EntryDraft{Kind: "cut"})
		if e != nil {
			return e
		}
		cut = entry.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := session.Commit(ctx, func(tx d.Tx) error {
		for _, definition := range definitions {
			if e := setDoc(ctx, tx, definition, address(definition.Kind, 1), 2); e != nil {
				return e
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var child d.ID
	if err := session.Commit(ctx, func(tx d.Tx) error {
		conversation, e := tx.ForkConversation(ctx, 1, cut, d.ConversationOwnership{Kind: "ownerless"})
		if e != nil {
			return e
		}
		child = conversation.ID
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 2} {
		definition := definitions[i]
		value, err := session.Snapshot(ctx, definition, address(definition.Kind, child))
		if err != nil || count(t, value) != want {
			t.Fatalf("fork %s value %+v err=%v", definition.Fork, value, err)
		}
	}
	absent, err := session.Snapshot(ctx, definitions[2], address("initial", child))
	if err != nil || absent != nil {
		t.Fatalf("initial fork eagerly created %+v %v", absent, err)
	}
	if err = session.Commit(ctx, func(tx d.Tx) error { _, e := tx.Doc(ctx, definitions[2], address("initial", child), nil); return e }); err != nil {
		t.Fatal(err)
	}
	initial, err := session.Snapshot(ctx, definitions[2], address("initial", child))
	if err != nil || count(t, initial) != 7 {
		t.Fatalf("initial policy %+v %v", initial, err)
	}
	old, err := session.SnapshotAsOf(ctx, definitions[0], address("past", 1), cut)
	if err != nil || count(t, old) != 1 {
		t.Fatalf("as-of entry commit %+v %v", old, err)
	}
	migrated := definitions[0]
	migrated.Version = 2
	migrated.Migrate = func(value d.JsonObject, from int) (d.JsonObject, error) {
		if from != 1 {
			return nil, errors.New("unexpected old version")
		}
		value["count"] = count(t, value) + 10
		return value, nil
	}
	before, err := s.FindDocument(ctx, address("past", 1), d.CurrentDocument())
	if err != nil || before == nil {
		t.Fatal(err)
	}
	value, err := session.Snapshot(ctx, migrated, address("past", 1))
	if err != nil || count(t, value) != 12 {
		t.Fatalf("lazy read migration %+v %v", value, err)
	}
	stored, err := s.Document(ctx, before.ID, d.CurrentDocument())
	if err != nil || stored == nil || stored.Version != 1 {
		t.Fatalf("read-only migration wrote storage %+v %v", stored, err)
	}
	if err = session.Commit(ctx, func(tx d.Tx) error { _, e := tx.Doc(ctx, migrated, address("past", 1), nil); return e }); err != nil {
		t.Fatal(err)
	}
	stored, err = s.Document(ctx, before.ID, d.CurrentDocument())
	if err != nil || stored == nil || stored.Version != 2 || stored.DeltasSinceBase != 0 {
		t.Fatalf("migration did not persist required base %+v %v", stored, err)
	}
}

func TestPortsmithJudgeDurableSessionWatchDoesNotBlockMutation(t *testing.T) {
	session, _ := sessionFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	definition := def("watch", "rewindable", "asOf")
	addr := address("watch", 1)
	if err := session.Commit(ctx, func(tx d.Tx) error { return setDoc(ctx, tx, definition, addr, 0) }); err != nil {
		t.Fatal(err)
	}
	watch, err := session.WatchDoc(ctx, definition, addr)
	if err != nil || watch == nil {
		t.Fatalf("watch %+v %v", watch, err)
	}
	entered := make(chan d.JsonObject, 1)
	release := make(chan struct{})
	defer close(release)
	if err = watch.Start(func(delivery context.Context, value d.JsonObject, ops []chord.Op) error {
		if len(ops) == 0 {
			return errors.New("empty changed frame")
		}
		entered <- value
		select {
		case <-release:
			return nil
		case <-delivery.Done():
			return delivery.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- session.Commit(ctx, func(tx d.Tx) error { return setDoc(ctx, tx, definition, addr, 1) })
	}()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("watch callback blocked Session storage/adoption")
	}
	select {
	case value := <-entered:
		if count(t, value) != 1 {
			t.Fatalf("watch did not receive committed frame %+v", value)
		}
	case <-ctx.Done():
		t.Fatal("watch lost post-acquisition commit")
	}
	if err = watch.Stop(); err != nil {
		t.Fatal(err)
	}
}
