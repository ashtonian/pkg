//go:build integration

package pgtest

import (
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

// Provider is the PostgreSQL provider on a pool on a fresh schema of the
// Postgres server (PostgresPool), returned with a database/sql handle on
// the same pool for the test's own statements. It skips as Postgres does.
func Provider(t testing.TB, opts ...parsepgx.Option) (*parse.Provider, *sql.DB) {
	t.Helper()
	return ProviderOn(t, PostgresPool(t), opts...)
}

// PostGISProvider is Provider on the PostGIS server (PostGISPool).
func PostGISProvider(t testing.TB, opts ...parsepgx.Option) (*parse.Provider, *sql.DB) {
	t.Helper()
	return ProviderOn(t, PostGISPool(t), opts...)
}

// ProviderOn is the PostgreSQL provider on pool (parsepgx.New), with a
// database/sql handle on pool, closed at cleanup.
func ProviderOn(t testing.TB, pool *pgxpool.Pool, opts ...parsepgx.Option) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, err := parsepgx.New(Context(t), pool, opts...)
	if err != nil {
		t.Fatalf("pgtest: %v", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	return p, db
}
