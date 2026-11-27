//go:build integration

package providertest

import (
	"database/sql"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/parsepgx"
)

// Postgres is a provider on a pool on a fresh schema of the Postgres
// server (dbtest.PostgresPool), returned with a database/sql handle on the
// same pool for the test's own statements. It skips as dbtest.Postgres
// does.
func Postgres(t testing.TB, opts ...parsepgx.Option) (*parse.Provider, *sql.DB) {
	t.Helper()
	return PostgresOn(t, dbtest.PostgresPool(t), opts...)
}

// PostGIS is Postgres on the PostGIS server (dbtest.PostGISPool).
func PostGIS(t testing.TB, opts ...parsepgx.Option) (*parse.Provider, *sql.DB) {
	t.Helper()
	return PostgresOn(t, dbtest.PostGISPool(t), opts...)
}

// PostgresOn is the PostgreSQL provider on pool (parsepgx.New), with a
// database/sql handle on pool, closed at cleanup.
func PostgresOn(t testing.TB, pool *pgxpool.Pool, opts ...parsepgx.Option) (*parse.Provider, *sql.DB) {
	t.Helper()
	p, err := parsepgx.New(dbtest.Context(t), pool, opts...)
	if err != nil {
		t.Fatalf("providertest: %v", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	return p, db
}
