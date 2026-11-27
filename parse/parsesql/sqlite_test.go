//go:build unit || integration

package parsesql

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// MemoryDB is a fresh in-memory database with parse's functions, closed at
// test cleanup, for parsesql's tests in both their packages. One
// connection: every statement sees the same in-memory database.
// Transactions begin IMMEDIATE, as Open begins them.
func MemoryDB(t testing.TB) *sql.DB {
	t.Helper()
	db := registeredHandle(t)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// HookedMemoryDB is MemoryDB through connections that report each statement
// to the hook dbtest.OnStatement puts in its context (dbtest.Hooked).
func HookedMemoryDB(t testing.TB) *sql.DB {
	t.Helper()
	// The handle only names the registered driver; it never connects.
	handle := registeredHandle(t)
	db := dbtest.Hooked(t, handle.Driver(), dbtest.SQLiteDSN)
	err := handle.Close()
	if err != nil {
		t.Fatalf("closing the sqlite handle: %v", err)
	}
	return db
}

// HookedFileDB is the SQLite database dsn names, a file, with parse's
// functions, on up to conns connections that report each statement to the
// hook dbtest.OnStatement puts in its context (dbtest.HookedPool), closed at
// test cleanup. Its transactions begin as dsn's _txlock says.
func HookedFileDB(t testing.TB, dsn string, conns int) *sql.DB {
	t.Helper()
	handle := registeredHandle(t)
	db := dbtest.HookedPool(t, handle.Driver(), dsn, conns)
	err := handle.Close()
	if err != nil {
		t.Fatalf("closing the sqlite handle: %v", err)
	}
	return db
}

// registeredHandle opens, without connecting, a handle on dbtest.SQLiteDSN
// through the sqlite driver, parse's functions registered on it first.
func registeredHandle(t testing.TB) *sql.DB {
	t.Helper()
	err := Register()
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbtest.SQLiteDSN)
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	return db
}
