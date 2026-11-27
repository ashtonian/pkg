//go:build unit || integration

// Package dbtest opens real databases for parse's result-based tests and
// reads their rows back (QueryRows, Exec): SQL the builders emit is
// executed and the returned rows / stored values are asserted, never the
// SQL text alone.
package dbtest

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/parsesqlite"
)

// sqliteDSN is one in-memory database whose transactions begin IMMEDIATE,
// as parsesql documents for its updates.
const sqliteDSN = ":memory:?_txlock=immediate"

// SQLite returns a fresh in-memory SQLite database with parse's functions
// (parsesqlite), closed at test cleanup. One connection: every statement
// sees the same in-memory database. Transactions begin IMMEDIATE, as
// parsesql documents for its updates.
func SQLite(t testing.TB) *sql.DB {
	t.Helper()
	db := openSQLite(t)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// openSQLite opens, without connecting, a handle on sqliteDSN through the
// sqlite driver, parse's functions registered on it first.
func openSQLite(t testing.TB) *sql.DB {
	t.Helper()
	err := parsesqlite.Register()
	if err != nil {
		t.Fatalf("dbtest: %v", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN)
	if err != nil {
		t.Fatalf("dbtest: open sqlite: %v", err)
	}
	return db
}
