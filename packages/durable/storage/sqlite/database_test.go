package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/minifish-org/pith/packages/durable"
)

// This file ports packages/durable/test/sqlite-facade.test.ts: the replaceable
// database facade's transaction, rollback, serialization, settlement, and
// close behavior.

func TestDatabaseTransactionCommitAndRollback(t *testing.T) {
	ctx := context.Background()
	db, err := openDatabase(":memory:", defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "CREATE TABLE probe (value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(ctx, func(tx Executor) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO probe (value) VALUES (?)", 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(ctx, func(tx Executor) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO probe (value) VALUES (?)", 2); err != nil {
			return err
		}
		return errors.New("roll back")
	}); err == nil || err.Error() != "roll back" {
		t.Fatalf("rollback err=%v", err)
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count=%d want 1", count)
	}
}

func TestDatabaseStaleTransactionHandleRejects(t *testing.T) {
	ctx := context.Background()
	db, err := openDatabase(":memory:", defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "CREATE TABLE stale_probe (value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	var handle Executor
	if err := db.Transaction(ctx, func(tx Executor) error {
		handle = tx
		_, err := tx.ExecContext(ctx, "INSERT INTO stale_probe (value) VALUES (?)", 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.ExecContext(ctx, "INSERT INTO stale_probe (value) VALUES (?)", 2); err == nil {
		t.Fatal("stale transaction handle accepted a write")
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM stale_probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("stale handle changed %d rows", count)
	}
}

func TestDatabaseSerializesTransactions(t *testing.T) {
	ctx := context.Background()
	db, err := openDatabase(":memory:", defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "CREATE TABLE queue (value INTEGER)"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- db.Transaction(ctx, func(tx Executor) error {
			if _, err := tx.ExecContext(ctx, "INSERT INTO queue (value) VALUES (?)", 1); err != nil {
				return err
			}
			close(started)
			<-release
			return nil
		})
	}()
	<-started

	secondStarted := make(chan struct{})
	second := make(chan error, 1)
	go func() {
		second <- db.Transaction(ctx, func(tx Executor) error {
			close(secondStarted)
			_, err := tx.ExecContext(ctx, "INSERT INTO queue (value) VALUES (?)", 2)
			return err
		})
	}()
	select {
	case <-secondStarted:
		t.Fatal("second transaction started before the first settled")
	default:
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	rows, err := db.QueryContext(ctx, "SELECT value FROM queue ORDER BY value")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := []int64{}
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if len(values) != 2 || values[0] != 1 || values[1] != 2 {
		t.Fatalf("values=%v", values)
	}
}

func TestDatabaseCloseIsIdempotent(t *testing.T) {
	ctx := context.Background()
	db, err := openDatabase(":memory:", defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE after_close (value INTEGER)"); err == nil {
		t.Fatal("closed database accepted a statement")
	}
}

type settlementMode int

const (
	settlementImmediate settlementMode = iota
	settlementDelay
	settlementReject
)

// controlledDatabase delays or rejects one transaction settlement so tests can
// observe that storage adopts IDs only after a successful commit.
type controlledDatabase struct {
	delegate Database

	mu      sync.Mutex
	mode    settlementMode
	release chan struct{}
	settled chan struct{}
}

func (c *controlledDatabase) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.delegate.ExecContext(ctx, query, args...)
}

func (c *controlledDatabase) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.delegate.QueryContext(ctx, query, args...)
}

func (c *controlledDatabase) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return c.delegate.QueryRowContext(ctx, query, args...)
}

func (c *controlledDatabase) Close() error { return c.delegate.Close() }

func (c *controlledDatabase) control(mode settlementMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mode = mode
	c.release = make(chan struct{})
	c.settled = make(chan struct{})
}

func (c *controlledDatabase) waitSettled() {
	c.mu.Lock()
	settled := c.settled
	c.mu.Unlock()
	<-settled
}

func (c *controlledDatabase) settle() {
	c.mu.Lock()
	release := c.release
	c.mu.Unlock()
	close(release)
}

func (c *controlledDatabase) Transaction(ctx context.Context, fn func(tx Executor) error) error {
	c.mu.Lock()
	mode := c.mode
	c.mode = settlementImmediate
	release, settled := c.release, c.settled
	c.mu.Unlock()
	if mode == settlementImmediate {
		return c.delegate.Transaction(ctx, fn)
	}
	err := c.delegate.Transaction(ctx, func(tx Executor) error {
		if innerErr := fn(tx); innerErr != nil {
			return innerErr
		}
		if mode == settlementReject {
			return errors.New("controlled settlement rejection")
		}
		return nil
	})
	close(settled)
	<-release
	return err
}

func newControlledStorage(t *testing.T, db Database) *Storage {
	t.Helper()
	ctx := context.Background()
	if err := applySqliteMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	var nextIDText string
	var nextSeq int64
	if err := db.QueryRowContext(ctx, "SELECT next_id, next_seq FROM durable_metadata WHERE singleton = 1").Scan(&nextIDText, &nextSeq); err != nil {
		t.Fatal(err)
	}
	nextID, err := strconv.ParseInt(nextIDText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return &Storage{db: db, nextID: nextID}
}

func TestStorageAdoptsIDsOnlyAfterSettlement(t *testing.T) {
	ctx := context.Background()
	raw, err := openDatabase(filepath.Join(t.TempDir(), "storage.sqlite"), defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	controlled := &controlledDatabase{delegate: raw}
	storage := newControlledStorage(t, controlled)
	defer storage.Close(ctx)

	controlled.control(settlementDelay)
	committing := make(chan error, 1)
	go func() {
		_, err := storage.Commit(ctx, []durable.StorageWrite{testEntry(100, durable.RootConversationID, "settled", "{}")})
		committing <- err
	}()
	controlled.waitSettled()
	if id := mustMint(t, storage); id != 2 {
		t.Fatalf("pre-settlement mint=%d", id)
	}
	controlled.settle()
	if err := <-committing; err != nil {
		t.Fatalf("commit: %v", err)
	}
	if id := mustMint(t, storage); id != 101 {
		t.Fatalf("post-settlement mint=%d", id)
	}

	controlled.control(settlementReject)
	go func() {
		_, err := storage.Commit(ctx, []durable.StorageWrite{testEntry(200, durable.RootConversationID, "rejected", "{}")})
		committing <- err
	}()
	controlled.waitSettled()
	if id := mustMint(t, storage); id != 102 {
		t.Fatalf("pre-rejection mint=%d", id)
	}
	controlled.settle()
	if err := <-committing; err == nil || err.Error() != "controlled settlement rejection" {
		t.Fatalf("rejected commit err=%v", err)
	}
	if id := mustMint(t, storage); id != 103 {
		t.Fatalf("post-rejection mint=%d", id)
	}
	if entry, err := storage.Entry(ctx, 200); err != nil || entry != nil {
		t.Fatalf("rejected write visible %+v %v", entry, err)
	}
}

func TestStorageDocumentReadIsSnapshotConsistent(t *testing.T) {
	ctx := context.Background()
	for yields := 0; yields < 16; yields++ {
		storage, _ := openTestStorage(t)
		commitRoot(t, storage)
		if _, err := storage.Commit(ctx, []durable.StorageWrite{{
			Type:    durable.WriteDocumentCreate,
			Record:  &durable.DocumentCreate{ID: 5, Kind: "replaced", Scope: durable.DocumentScope{Kind: durable.ScopeSession}},
			Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"value": 1}},
		}}); err != nil {
			t.Fatal(err)
		}
		read := make(chan *durable.StoredDocument, 1)
		readErr := make(chan error, 1)
		go func() {
			stored, err := storage.Document(ctx, 5, durable.CurrentDocument())
			read <- stored
			readErr <- err
		}()
		for index := 0; index < yields; index++ {
			// Yield so the commit can start at different points.
			_ = index
		}
		if _, err := storage.Commit(ctx, []durable.StorageWrite{{
			Type:    durable.WriteDocumentChange,
			ID:      5,
			Content: &durable.DocumentContent{Kind: durable.ContentBase, Version: 1, Value: durable.JsonObject{"value": 2}},
		}}); err != nil {
			t.Fatal(err)
		}
		stored := <-read
		if err := <-readErr; err != nil {
			t.Fatal(err)
		}
		if stored == nil {
			t.Fatal("document disappeared")
		}
		value, ok := stored.Value["value"].(float64)
		if !ok || (value != 1 && value != 2) {
			t.Fatalf("inconsistent value %v", stored.Value["value"])
		}
	}
}
