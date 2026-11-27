//go:build unit || integration

package parse_test

import (
	"cmp"
	"context"
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// resourceRows runs st, a statement over r's Columns, on r's provider and
// scans its rows through r's row contract (Resource.Query).
func resourceRows[T any](t testing.TB, r *parse.Resource[T], st parse.Statement) []T {
	t.Helper()
	rows, err := r.Query(t.Context(), st)
	require.NoError(t, err, st.SQL)
	return rows
}

// joinColumns is a SELECT list.
func joinColumns(columns []string) string { return strings.Join(columns, ", ") }

// sqlQueryer runs a query: *sql.DB, *sql.Conn and *sql.Tx.
type sqlQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// queryRows runs query on db and reads every row as a T: its one column
// scanned into a T, or, for T []any, every column as the driver returns it,
// or, for T map[string]any, every column by name. Every suite reads a
// statement's rows through it.
func queryRows[T any](t testing.TB, db sqlQueryer, query string, args ...any) []T {
	t.Helper()
	return queryRowsContext[T](t.Context(), t, db, query, args...)
}

// queryRowsContext is queryRows under ctx, for a lookup that parse calls
// with a context of its own.
func queryRowsContext[T any](ctx context.Context, t testing.TB, db sqlQueryer, query string, args ...any) []T {
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

// itemIDs is each item's id, in the items' order, or nil for none.
func itemIDs[T, K any](items []T, id func(T) K) []K {
	var out []K
	for _, item := range items {
		out = append(out, id(item))
	}
	return out
}

// sortedIDs is itemIDs sorted: the rows a statement returned, compared as a
// set whatever order they came back in.
func sortedIDs[T any, K cmp.Ordered](items []T, id func(T) K) []K {
	out := itemIDs(items, id)
	slices.Sort(out)
	return out
}

// execStatements runs a fixture's statements on db, in order.
func execStatements(t testing.TB, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, stmt := range stmts {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err, stmt)
	}
}

// listPage lists req under s on r's provider (Resource.List).
func listPage[T any](t testing.TB, r *parse.Resource[T], req *parse.ListRequest, s parse.Scope) *filter.Page[T] {
	t.Helper()
	page, err := r.List(t.Context(), req, s)
	require.NoError(t, err)
	return page
}

// applyUpdate applies p to target under s on r's provider (Resource.Update),
// or with inTx in a transaction it rolls back after the call, the update
// under a savepoint of it (Resource.In).
func applyUpdate[T any](t testing.TB, r *parse.Resource[T], inTx bool, p *patch.ParsedPatch, target parse.Target, s parse.Scope, opts ...parse.UpdateOption) (parse.UpdateResult[T], error) {
	t.Helper()
	if !inTx {
		return r.Update(t.Context(), p, target, s, opts...)
	}
	tx, err := r.Provider().Begin(t.Context())
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(t.Context())) }()
	scoped, err := r.In(tx)
	require.NoError(t, err)
	return scoped.Update(t.Context(), p, target, s, opts...)
}
