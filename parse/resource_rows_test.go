//go:build unit || integration

package parse_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/parsepgx"
	"github.com/ashtonian/pkg/parse/parsesql"
)

// resourceRows runs st, a statement r compiled for d, on db and scans its
// rows through r's row contract: parsesql on SQLite, and on PostgreSQL
// parsepgx on the pgx connection under db, a database/sql pool over pgx's
// stdlib driver.
func resourceRows[T any](t testing.TB, db parsesql.Queryer, r *parse.Resource[T], d filter.Dialect, st parse.Statement) []T {
	t.Helper()
	var (
		rows []T
		err  error
	)
	switch d.(type) {
	case filter.SQLiteDialect:
		rows, err = parsesql.Query(t.Context(), db, r, st)
	case filter.PostgresDialect:
		pool, ok := db.(*sql.DB)
		require.True(t, ok, "PostgreSQL rows are read through pgx, from a *sql.DB; got %T", db)
		err = withPgx(t, pool, func(conn *pgx.Conn) error {
			var err error
			rows, err = parsepgx.Query(t.Context(), conn, r, st)
			return err
		})
	default:
		t.Fatalf("no executor for %T", d)
	}
	require.NoError(t, err, st.SQL)
	return rows
}

// parsesqlQuery is parsesql.Query, for a test that expects it to fail.
func parsesqlQuery[T any](t testing.TB, db parsesql.Queryer, r *parse.Resource[T], st parse.Statement) ([]T, error) {
	return parsesql.Query(t.Context(), db, r, st)
}

// joinColumns is a SELECT list.
func joinColumns(columns []string) string { return strings.Join(columns, ", ") }

// listPage runs plan, a list r compiled for d, on db and returns its page:
// parsepgx.QueryPage on PostgreSQL; on SQLite the page statement's rows
// through r.Page.
func listPage[T any](t testing.TB, db *sql.DB, r *parse.Resource[T], d filter.Dialect, plan *parse.ListPlan) *filter.Page[T] {
	t.Helper()
	if _, ok := d.(filter.PostgresDialect); ok {
		var page *filter.Page[T]
		require.NoError(t, withPgx(t, db, func(conn *pgx.Conn) error {
			var err error
			page, err = parsepgx.QueryPage(t.Context(), conn, r, plan)
			return err
		}))
		return page
	}
	page, err := r.Page(resourceRows(t, db, r, d, plan.Page), plan)
	require.NoError(t, err)
	return page
}

// withPgx runs f on the pgx connection of one of db's connections.
func withPgx(t testing.TB, db *sql.DB, f func(*pgx.Conn) error) error {
	t.Helper()
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close()) }()
	return conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*stdlib.Conn)
		require.True(t, ok, "db is not a pgx stdlib pool: %T", driverConn)
		return f(c.Conn())
	})
}
