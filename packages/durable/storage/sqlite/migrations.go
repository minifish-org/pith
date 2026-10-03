package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Migration is one immutable, ordered schema migration. Versions must be
// contiguous starting at 1.
type Migration struct {
	Version    int
	Statements []string
}

// initialSchema is the persisted format-1 layout. next_id is TEXT because
// SQLite INTEGER results outside the JavaScript safe-integer range cannot be
// represented exactly; durable_metadata stores it in decimal instead.
var initialSchema = []string{
	`CREATE TABLE durable_metadata (
		singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
		next_id TEXT NOT NULL,
		next_seq INTEGER NOT NULL
	) STRICT`,
	`INSERT INTO durable_metadata (singleton, next_id, next_seq) VALUES (1, '2', 1)`,
	`CREATE TABLE record_ids (
		id INTEGER PRIMARY KEY,
		record_type TEXT NOT NULL CHECK (record_type IN ('conversation', 'entry', 'task', 'submission', 'document'))
	) STRICT`,
	`CREATE TABLE conversations (
		id INTEGER PRIMARY KEY,
		owner_conversation_id INTEGER,
		owner_task_id INTEGER,
		record TEXT NOT NULL CHECK (json_valid(record))
	) STRICT`,
	"CREATE INDEX conversations_by_owner_conversation ON conversations (owner_conversation_id, id)",
	"CREATE INDEX conversations_by_owner_task ON conversations (owner_task_id, id)",
	`CREATE TABLE entries (
		id INTEGER PRIMARY KEY,
		conversation_id INTEGER NOT NULL,
		head INTEGER,
		commit_seq INTEGER NOT NULL,
		record TEXT NOT NULL CHECK (json_valid(record))
	) STRICT`,
	"CREATE INDEX entries_by_conversation ON entries (conversation_id, id DESC)",
	"CREATE INDEX entry_heads_by_conversation ON entries (conversation_id, id DESC) WHERE head IS NOT NULL",
	`CREATE TABLE tasks (
		id INTEGER PRIMARY KEY,
		conversation_id INTEGER NOT NULL,
		kind TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'waiting', 'completing', 'terminal')),
		abort_requested INTEGER NOT NULL CHECK (abort_requested IN (0, 1)),
		background INTEGER NOT NULL CHECK (background IN (0, 1)),
		record TEXT NOT NULL CHECK (json_valid(record))
	) STRICT`,
	"CREATE INDEX tasks_by_status ON tasks (status, id)",
	"CREATE INDEX tasks_by_conversation ON tasks (conversation_id, id)",
	"CREATE INDEX tasks_by_kind ON tasks (kind, id)",
	"CREATE INDEX tasks_by_abort_requested ON tasks (abort_requested, id)",
	"CREATE INDEX tasks_by_background ON tasks (background, id)",
	`CREATE TABLE submissions (
		id INTEGER PRIMARY KEY,
		conversation_id INTEGER NOT NULL,
		request_id TEXT,
		status TEXT NOT NULL CHECK (status IN ('queued', 'placed', 'done', 'unanswered')),
		record TEXT NOT NULL CHECK (json_valid(record))
	) STRICT`,
	"CREATE INDEX submissions_by_request ON submissions (conversation_id, request_id)",
	"CREATE INDEX submissions_by_conversation ON submissions (conversation_id, id)",
	"CREATE INDEX submissions_by_status ON submissions (status, id)",
	`CREATE TABLE documents (
		id INTEGER PRIMARY KEY,
		kind TEXT NOT NULL,
		family INTEGER NOT NULL CHECK (family IN (0, 1)),
		key_value TEXT NOT NULL,
		scope_kind TEXT NOT NULL CHECK (scope_kind IN ('session', 'conversation', 'task')),
		owner_id INTEGER NOT NULL,
		created_at INTEGER NOT NULL,
		retired_at INTEGER,
		record TEXT NOT NULL CHECK (json_valid(record))
	) STRICT`,
	`CREATE INDEX documents_by_address
		ON documents (kind, scope_kind, owner_id, family, key_value, created_at DESC, retired_at)`,
	"CREATE INDEX documents_by_scope ON documents (scope_kind, owner_id, id)",
	"CREATE INDEX documents_by_scope_kind ON documents (scope_kind, owner_id, kind, id)",
	`CREATE TABLE document_revisions (
		document_id INTEGER NOT NULL,
		seq INTEGER NOT NULL,
		kind TEXT NOT NULL CHECK (kind IN ('base', 'delta')),
		version INTEGER NOT NULL,
		content TEXT NOT NULL CHECK (json_valid(content)),
		PRIMARY KEY (document_id, seq)
	) STRICT`,
	"CREATE INDEX document_revisions_by_kind ON document_revisions (document_id, kind, seq DESC)",
}

// Migrations is the immutable, ordered schema history. Append new migrations
// after the initial schema ships.
var Migrations = []Migration{{Version: 1, Statements: initialSchema}}

// CurrentSchemaVersion is the newest known schema version.
var CurrentSchemaVersion = Migrations[len(Migrations)-1].Version

// ApplyMigrations applies every pending migration atomically. Reapplying the
// current schema is a no-op. A database whose stored version is newer than the
// supplied history is rejected without rewriting it.
func ApplyMigrations(ctx context.Context, db Database, migrations []Migration) error {
	for index, migration := range migrations {
		if migration.Version != index+1 {
			return errors.New("Durable SQLite migrations must have contiguous versions starting at 1")
		}
	}
	current := 0
	if len(migrations) > 0 {
		current = migrations[len(migrations)-1].Version
	}
	return db.Transaction(ctx, func(tx Executor) error {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS durable_schema (
			singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
			version INTEGER NOT NULL CHECK (version >= 0)
		) STRICT`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO durable_schema (singleton, version) VALUES (1, 0)"); err != nil {
			return err
		}
		var version int64
		if err := tx.QueryRowContext(ctx, "SELECT version FROM durable_schema WHERE singleton = 1").Scan(&version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("Durable SQLite schema metadata is missing")
			}
			return err
		}
		if version > int64(current) {
			return fmt.Errorf("Durable SQLite schema version %d is newer than supported version %d", version, current)
		}
		for _, migration := range migrations {
			if int64(migration.Version) <= version {
				continue
			}
			for _, statement := range migration.Statements {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(ctx, "UPDATE durable_schema SET version = ? WHERE singleton = 1", migration.Version); err != nil {
				return err
			}
		}
		return nil
	})
}

// applySqliteMigrations applies the bundled history.
func applySqliteMigrations(ctx context.Context, db Database) error {
	return ApplyMigrations(ctx, db, Migrations)
}
