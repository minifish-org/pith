package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minifish-org/pith/packages/durable"
)

// This file ports packages/durable/test/sqlite-migrations.test.ts. It verifies
// the atomic, append-only, contiguous migration history and the stored
// format/metadata layout.

func TestMigrationsCreateAndReapply(t *testing.T) {
	ctx := context.Background()
	db, err := openDatabase(filepath.Join(t.TempDir(), "storage.sqlite"), defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ApplyMigrations(ctx, db, Migrations); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db, Migrations); err != nil {
		t.Fatalf("reapply: %v", err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, "SELECT version FROM durable_schema WHERE singleton = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != int64(CurrentSchemaVersion) {
		t.Fatalf("version=%d want %d", version, CurrentSchemaVersion)
	}
	var nextID string
	var nextSeq int64
	if err := db.QueryRowContext(ctx, "SELECT next_id, next_seq FROM durable_metadata WHERE singleton = 1").Scan(&nextID, &nextSeq); err != nil {
		t.Fatal(err)
	}
	if nextID != "2" || nextSeq != 1 {
		t.Fatalf("metadata next_id=%q next_seq=%d", nextID, nextSeq)
	}
}

func TestMigrationsRejectNewer(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "storage.sqlite")
	storage, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db := openRaw(t, path)
	if _, err := db.Exec("UPDATE durable_schema SET version = ? WHERE singleton = 1", int64(CurrentSchemaVersion)+1); err != nil {
		t.Fatal(err)
	}
	db.Close()
	reopened, err := Open(ctx, path, Options{})
	if reopened != nil {
		reopened.Close(ctx)
	}
	if err == nil || !strings.Contains(err.Error(), "is newer than supported version") {
		t.Fatalf("err=%v", err)
	}
	// The newer file must not have been rewritten or downgraded.
	check := openRaw(t, path)
	defer check.Close()
	if version := scalar(t, check, "SELECT version FROM durable_schema WHERE singleton = 1"); version != int64(CurrentSchemaVersion)+1 {
		t.Fatalf("newer schema rewritten: %d", version)
	}
}

func TestMigrationsRollBackBootstrapAndPending(t *testing.T) {
	ctx := context.Background()
	db, err := openDatabase(filepath.Join(t.TempDir(), "storage.sqlite"), defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	failed := []Migration{
		{Version: 1, Statements: []string{
			"CREATE TABLE migration_first (value TEXT) STRICT",
			"INSERT INTO migration_first (value) VALUES ('retained')",
		}},
		{Version: 2, Statements: []string{"CREATE TABLE migration_second (value TEXT) STRICT", "THIS IS NOT SQL"}},
	}
	if err := ApplyMigrations(ctx, db, failed); err == nil {
		t.Fatal("failed history accepted")
	}
	var count int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name IN ('durable_schema', 'migration_first', 'migration_second')").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial schema persisted: %d", count)
	}
	recovered := []Migration{failed[0], {Version: 2, Statements: []string{"CREATE TABLE migration_second (value TEXT) STRICT"}}}
	if err := ApplyMigrations(ctx, db, recovered); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := db.QueryRowContext(ctx, "SELECT version FROM durable_schema WHERE singleton = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 2 {
		t.Fatalf("version=%d", version)
	}
	var value string
	if err := db.QueryRowContext(ctx, "SELECT value FROM migration_first").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "retained" {
		t.Fatalf("value=%q", value)
	}
}

func TestMigrationsPreserveDataAcrossFailedRetry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "storage.sqlite")
	storage, err := Open(ctx, path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Commit(ctx, []durable.StorageWrite{
		{Type: durable.WriteConversation, Conversation: &durable.ConversationRecord{ID: durable.RootConversationID}},
		{Type: durable.WriteEntry, Entry: &durable.EntryRecord{ID: 2, ConversationID: durable.RootConversationID, Kind: "retained", Data: json.RawMessage(`{"retained":true}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(ctx); err != nil {
		t.Fatal(err)
	}

	db, err := openDatabase(path, defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	nextVersion := CurrentSchemaVersion + 1
	failed := append(append([]Migration{}, Migrations...), Migration{Version: nextVersion, Statements: []string{"CREATE TABLE migration_probe (value TEXT) STRICT", "THIS IS NOT SQL"}})
	if err := ApplyMigrations(ctx, db, failed); err == nil {
		t.Fatal("failed pending migration accepted")
	}
	var version int64
	if err := db.QueryRowContext(ctx, "SELECT version FROM durable_schema WHERE singleton = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != int64(CurrentSchemaVersion) {
		t.Fatalf("version rolled to %d", version)
	}
	var probe int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type = 'table' AND name = 'migration_probe'").Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if probe != 0 {
		t.Fatal("failed migration left a table")
	}

	successful := append(append([]Migration{}, Migrations...), Migration{Version: nextVersion, Statements: []string{"CREATE TABLE migration_probe (value TEXT) STRICT"}})
	if err := ApplyMigrations(ctx, db, successful); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT version FROM durable_schema WHERE singleton = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != int64(nextVersion) {
		t.Fatalf("version=%d", version)
	}
	var record string
	var commitSeq int64
	if err := db.QueryRowContext(ctx, "SELECT record, commit_seq FROM entries WHERE id = 2").Scan(&record, &commitSeq); err != nil {
		t.Fatal(err)
	}
	if commitSeq != 1 {
		t.Fatalf("commit_seq=%d", commitSeq)
	}
	assertJSONEqual(t, json.RawMessage(record), `{"id":2,"conversationId":1,"kind":"retained","data":{"retained":true}}`)
	var nextID string
	var nextSeq int64
	if err := db.QueryRowContext(ctx, "SELECT next_id, next_seq FROM durable_metadata WHERE singleton = 1").Scan(&nextID, &nextSeq); err != nil {
		t.Fatal(err)
	}
	if nextID != "3" || nextSeq != 2 {
		t.Fatalf("metadata next_id=%q next_seq=%d", nextID, nextSeq)
	}
}

func TestMigrationsRejectNonContiguousHistory(t *testing.T) {
	ctx := context.Background()
	db, err := openDatabase(":memory:", defaultWALAutoCheckpointPages, defaultBusyTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	err = ApplyMigrations(ctx, db, []Migration{{Version: 2, Statements: []string{"CREATE TABLE x (value TEXT)"}}})
	if err == nil || !strings.Contains(err.Error(), "contiguous") {
		t.Fatalf("err=%v", err)
	}
}
