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
	switch {
	case !isPostgres(d):
		rows, err = parsesql.Query(t.Context(), db, r, st)
	default:
		pool, ok := db.(*sql.DB)
		require.True(t, ok, "PostgreSQL rows are read through pgx, from a *sql.DB; got %T", db)
		err = withPgx(t, pool, func(conn *pgx.Conn) error {
			var err error
			rows, err = parsepgx.Query(t.Context(), conn, r, st)
			return err
		})
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

// listPage runs plan, a list r compiled for d, on db through the dialect's
// executor: parsesql.QueryPage on SQLite, parsepgx.QueryPage on PostgreSQL.
func listPage[T any](t testing.TB, db *sql.DB, r *parse.Resource[T], d filter.Dialect, plan *parse.ListPlan) *filter.Page[T] {
	t.Helper()
	page, err := queryPage(t, db, r, d, plan)
	require.NoError(t, err)
	return page
}

func queryPage[T any](t testing.TB, db *sql.DB, r *parse.Resource[T], d filter.Dialect, plan *parse.ListPlan) (*filter.Page[T], error) {
	t.Helper()
	if !isPostgres(d) {
		return parsesql.QueryPage(t.Context(), db, r, plan)
	}
	var page *filter.Page[T]
	err := withPgx(t, db, func(conn *pgx.Conn) error {
		var err error
		page, err = parsepgx.QueryPage(t.Context(), conn, r, plan)
		return err
	})
	return page, err
}

// applyUpdate runs plan on db through the dialect's executor; inTx runs it
// in a transaction the test then rolls back.
func applyUpdate[T any](t testing.TB, db *sql.DB, r *parse.Resource[T], d filter.Dialect, plan *parse.UpdatePlan, inTx bool) (parse.UpdateResult[T], error) {
	t.Helper()
	var res parse.UpdateResult[T]
	err := onExecutor(t, db, d, inTx, func(sqlDB parsesql.DB) error {
		var err error
		res, err = parsesql.Update(t.Context(), sqlDB, r, plan)
		return err
	}, func(pgxDB parsepgx.DB) error {
		var err error
		res, err = parsepgx.Update(t.Context(), pgxDB, r, plan)
		return err
	})
	return res, err
}

// applyUpdateBatch is applyUpdate for a batch plan.
func applyUpdateBatch[T any](t testing.TB, db *sql.DB, r *parse.Resource[T], d filter.Dialect, plan *parse.UpdateBatchPlan) (parse.UpdateResult[T], error) {
	t.Helper()
	var res parse.UpdateResult[T]
	err := onExecutor(t, db, d, false, func(sqlDB parsesql.DB) error {
		var err error
		res, err = parsesql.UpdateBatch(t.Context(), sqlDB, r, plan)
		return err
	}, func(pgxDB parsepgx.DB) error {
		var err error
		res, err = parsepgx.UpdateBatch(t.Context(), pgxDB, r, plan)
		return err
	})
	return res, err
}

// onExecutor runs the SQLite or the PostgreSQL call for d on db, or with
// inTx in a transaction it rolls back after the call.
func onExecutor(t testing.TB, db *sql.DB, d filter.Dialect, inTx bool, onSQL func(parsesql.DB) error, onPgx func(parsepgx.DB) error) error {
	t.Helper()
	if !isPostgres(d) {
		if !inTx {
			return onSQL(db)
		}
		tx, err := db.BeginTx(t.Context(), nil)
		require.NoError(t, err)
		defer func() { require.NoError(t, tx.Rollback()) }()
		return onSQL(tx)
	}
	return withPgx(t, db, func(conn *pgx.Conn) error {
		if !inTx {
			return onPgx(conn)
		}
		tx, err := conn.Begin(t.Context())
		require.NoError(t, err)
		defer func() { require.NoError(t, tx.Rollback(t.Context())) }()
		return onPgx(tx)
	})
}

func isPostgres(d filter.Dialect) bool { return d.Name() == (filter.PostgresDialect{}).Name() }

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
