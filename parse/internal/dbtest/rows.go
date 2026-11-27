//go:build unit || integration

// Package dbtest reads rows back from the databases parse's result-based
// tests run on (QueryRows, Exec), so the SQL the builders emit is executed
// and the rows returned and stored are asserted, never the SQL text alone,
// and reports each statement a hooked connection runs (Hooked,
// OnStatement). It imports no driver: each provider's tests open their own
// databases.
package dbtest

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// Queryer runs a query: *sql.DB, *sql.Conn and *sql.Tx.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// QueryRows runs query on db and reads every row as a T: its one column
// scanned into a T, or, for T []any, every column as the driver returns it,
// or, for T map[string]any, every column by name. Every suite reads a
// statement's rows through it.
func QueryRows[T any](t testing.TB, db Queryer, query string, args ...any) []T {
	t.Helper()
	return QueryRowsContext[T](t.Context(), t, db, query, args...)
}

// QueryRowsContext is QueryRows under ctx, for a lookup that parse calls
// with a context of its own.
func QueryRowsContext[T any](ctx context.Context, t testing.TB, db Queryer, query string, args ...any) []T {
	t.Helper()
	rows, err := db.QueryContext(ctx, query, args...)
	require.NoError(t, err, query)
	defer func() { require.NoError(t, rows.Close()) }()
	columns, err := rows.Columns()
	require.NoError(t, err, query)
	var out []T
	for rows.Next() {
		var row T
		require.NoError(t, scanRow(rows, columns, &row), query)
		out = append(out, row)
	}
	require.NoError(t, rows.Err(), query)
	return out
}

func scanRow(rows *sql.Rows, columns []string, dest any) error {
	values := make([]any, len(columns))
	ptrs := make([]any, len(columns))
	for i := range values {
		ptrs[i] = &values[i]
	}
	switch dest := dest.(type) {
	case *[]any:
		*dest = values
		return rows.Scan(ptrs...)
	case *map[string]any:
		err := rows.Scan(ptrs...)
		if err != nil {
			return err
		}
		row := make(map[string]any, len(columns))
		for i, c := range columns {
			row[c] = values[i]
		}
		*dest = row
		return nil
	default:
		return rows.Scan(dest)
	}
}

// Exec runs a fixture's statements on db, in order.
func Exec(t testing.TB, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
}
