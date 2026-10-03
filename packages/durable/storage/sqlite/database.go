package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite" // CGO-free SQLite database/sql driver.
)

// Executor is the SQL execution surface shared by a database and one
// transaction. It is a small subset of database/sql so adapters can be wrapped
// for fault injection or controlled settlement, mirroring the upstream
// SqliteExecutor facade.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Database is the replaceable SQLite facade used by Storage. A transaction
// executes fn against one connection; when fn returns an error the transaction
// is rolled back before the same error is returned, and the handle is invalid
// afterwards.
//
// Database is safe for concurrent use. The bundled implementation serializes
// every operation on a single connection, so a transaction and the ordinary
// operations around it run in call order and never observe uncommitted rows.
type Database interface {
	Executor
	// Transaction runs fn inside one SQL transaction. All work inside fn must
	// use the supplied executor. When fn panics the transaction is rolled back
	// before the panic continues.
	Transaction(ctx context.Context, fn func(tx Executor) error) error
	// Close releases the underlying connection. It is idempotent.
	Close() error
}

// sqlDatabase adapts *sql.DB to Database. Every operation is serialized by a
// single pooled connection.
type sqlDatabase struct {
	db *sql.DB
}

func (d *sqlDatabase) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.db.ExecContext(ctx, query, args...)
}

func (d *sqlDatabase) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.db.QueryContext(ctx, query, args...)
}

func (d *sqlDatabase) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.db.QueryRowContext(ctx, query, args...)
}

func (d *sqlDatabase) Transaction(ctx context.Context, fn func(tx Executor) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rollbackErr := tx.Rollback(); rollbackErr != nil {
			// Preserve both failures so callers cannot mistake the callback
			// error for a guaranteed rollback.
			return errors.Join(err, fmt.Errorf("SQLite transaction rollback failed: %w", rollbackErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (d *sqlDatabase) Close() error {
	if _, err := d.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		// A checkpoint failure must not prevent the connection from closing.
		_ = err
	}
	return d.db.Close()
}

// openDatabase opens and configures one SQLite database facade using the
// CGO-free driver. The path is created with its parent directories when it is an
// ordinary filesystem path.
func openDatabase(path string, checkpointPages, busyTimeoutMS int) (Database, error) {
	if path != ":memory:" && path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection serializes transactions and ordinary operations in call
	// order, matching the upstream serial-operation queue.
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if err := execPragma(ctx, db, "PRAGMA busy_timeout = "+strconv.Itoa(busyTimeoutMS)); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := execPragma(ctx, db, "PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := execPragma(ctx, db, "PRAGMA synchronous = NORMAL"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := execPragma(ctx, db, "PRAGMA wal_autocheckpoint = "+strconv.Itoa(checkpointPages)); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &sqlDatabase{db: db}, nil
}

// execPragma runs a PRAGMA statement that may return a row, discarding the
// result. Some pragmas (for example synchronous) report success without a row.
func execPragma(ctx context.Context, db *sql.DB, statement string) error {
	var value any
	err := db.QueryRowContext(ctx, statement).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
