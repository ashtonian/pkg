//go:build unit || integration

package providertest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsesql"
)

// SQLite is a provider on a fresh in-memory SQLite database (dbtest.SQLite),
// returned with the database for the test's own statements.
func SQLite(t testing.TB, opts ...parsesql.Option) (*parse.Provider, *sql.DB) {
	t.Helper()
	db := dbtest.SQLite(t)
	return SQLiteOn(t, db, opts...), db
}

// SQLiteOn is the SQLite provider on db (parsesql.New).
func SQLiteOn(t testing.TB, db *sql.DB, opts ...parsesql.Option) *parse.Provider {
	t.Helper()
	p, err := parsesql.New(context.Background(), db, opts...)
	if err != nil {
		t.Fatalf("providertest: %v", err)
	}
	return p
}
