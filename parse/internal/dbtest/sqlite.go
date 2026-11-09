//go:build unit || integration

// Package dbtest opens real databases for parse's result-based tests: SQL
// the builders emit is executed and the returned rows / stored values are
// asserted, never the SQL text alone.
package dbtest

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/parsesqlite"
)

// SQLite returns a fresh in-memory SQLite database with parse's functions
// (parsesqlite), closed at test cleanup. One connection: every statement
// sees the same in-memory database.
func SQLite(t testing.TB) *sql.DB {
	t.Helper()
	if err := parsesqlite.Register(); err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("dbtest: open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}
