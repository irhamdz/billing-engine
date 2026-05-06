package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// openTestDB creates a fresh on-disk sqlite (WAL needs a real file).
func openTestDB(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := OpenDB(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestOpenDB_AppliesPragmas(t *testing.T) {
	db := openTestDB(t)
	row := db.Underlying().QueryRow("PRAGMA journal_mode")
	var mode string
	if err := row.Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode=%q want wal", mode)
	}
	row = db.Underlying().QueryRow("PRAGMA foreign_keys")
	var fk int
	if err := row.Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatalf("foreign_keys=%d want 1", fk)
	}
	row = db.Underlying().QueryRow("PRAGMA busy_timeout")
	var bt int
	if err := row.Scan(&bt); err != nil {
		t.Fatal(err)
	}
	if bt != 5000 {
		t.Fatalf("busy_timeout=%d want 5000", bt)
	}
}

func TestOpenDB_RunsMigration(t *testing.T) {
	db := openTestDB(t)
	for _, tbl := range []string{"loans", "installments", "payments"} {
		row := db.Underlying().QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, tbl)
		var n int
		if err := row.Scan(&n); err != nil {
			t.Fatalf("missing table %q: %v", tbl, err)
		}
	}
}
