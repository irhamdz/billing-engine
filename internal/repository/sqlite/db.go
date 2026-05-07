// Package sqlite is the concrete repository implementation backed by
// modernc.org/sqlite (pure-Go, no CGo). PRD section 6.1 / section 10.5.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"

	"github.com/irhamdz/billing-engine/internal/repository"

	_ "modernc.org/sqlite"
)

//go:embed migrations/001_init.sql
var initMigration string

// DB wraps *sql.DB and exposes BEGIN IMMEDIATE for the write path.
type DB struct {
	sqlDB *sql.DB
}

// OpenDB opens (or creates) a sqlite database at path with the PRAGMAs
// required by PRD section 6.1 and runs the embedded init migration.
//
// modernc.org/sqlite applies DSN-supplied `_pragma=...` parameters on every
// connection checkout, which is what we need: foreign_keys, busy_timeout, and
// synchronous are per-connection settings. journal_mode=WAL is per-database
// and is set once after open.
func OpenDB(ctx context.Context, path string) (*DB, error) {
	// Build a DSN that re-applies the per-connection PRAGMAs whenever the
	// pool opens a new conn.
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	dsn := path + separator +
		"_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(1)"
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}

	// journal_mode=WAL persists in the database file once set; running it on
	// any single connection is enough.
	if _, err := sdb.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("pragma journal_mode: %w", err)
	}

	if _, err := sdb.ExecContext(ctx, initMigration); err != nil {
		_ = sdb.Close()
		return nil, fmt.Errorf("migration: %w", err)
	}
	return &DB{sqlDB: sdb}, nil
}

// Close terminates the connection pool.
func (d *DB) Close() error { return d.sqlDB.Close() }

// Underlying returns the wrapped *sql.DB.
func (d *DB) Underlying() *sql.DB { return d.sqlDB }

// BeginImmediate opens a write transaction with the SQLite reserved lock
// acquired up front. PRD section 6.1 — eliminates the "two readers both decide to
// write" race.
func (d *DB) BeginImmediate(ctx context.Context) (repository.Tx, error) {
	// modernc.org/sqlite supports BEGIN IMMEDIATE via raw exec on a tx.
	// database/sql's BeginTx does not let us choose lock mode, so we use Conn.
	conn, err := d.sqlDB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("Conn: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("BEGIN IMMEDIATE: %w", err)
	}
	return &connTx{ctx: ctx, conn: conn}, nil
}

// connTx adapts a *sql.Conn holding an explicit BEGIN IMMEDIATE.
//
// We can't use *sql.Tx because database/sql does not expose lock-mode
// selection; using a Conn keeps the connection pinned for the duration of the
// transaction, mirroring what *sql.Tx does internally.
type connTx struct {
	ctx  context.Context
	conn *sql.Conn
	done bool
}

func (t *connTx) Commit() error {
	if t.done {
		return sql.ErrTxDone
	}
	t.done = true
	_, err := t.conn.ExecContext(t.ctx, "COMMIT")
	cerr := t.conn.Close()
	if err != nil {
		return err
	}
	return cerr
}

func (t *connTx) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	_, err := t.conn.ExecContext(t.ctx, "ROLLBACK")
	cerr := t.conn.Close()
	if err != nil {
		return err
	}
	return cerr
}

// exec, query, queryRow run the given SQL against the pinned connection.
func (t *connTx) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.conn.ExecContext(ctx, q, args...)
}

func (t *connTx) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.conn.QueryContext(ctx, q, args...)
}

func (t *connTx) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return t.conn.QueryRowContext(ctx, q, args...)
}
