//go:build integration

package corpus

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Store is the Memory engine's database, which holds its own tables. The
// resources a case builds declare the tables they read; a fixture declares
// the table no resource has declared when it loads, as a SQL engine's
// CREATE TABLE does, and its rows are written through the store, as the
// engine's INSERT statements write them.
type Store interface {
	// Declare declares table, unless a resource or a fixture has: columns,
	// each typed, keyed by key; a table without a key numbers its rows, as
	// a SQL table without a primary key holds duplicates and NULLs.
	Declare(table string, columns []StoreColumn, key []string) error
	// Put writes rows to table, each a Go value per column by name.
	Put(ctx context.Context, table string, rows ...map[string]any) error
	// Select is column, as text, of each row of schema's table that f
	// selects, evaluated as the provider's lists evaluate a filter, its
	// relations held to scope.
	Select(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, scope filter.RelationScope, column string) ([]string, error)
	// Facets counts reqs over the rows of schema's table f selects, every
	// row for a nil f, as the provider's lists count their facets, their
	// relations held to scope.
	Facets(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, reqs []filter.FacetRequest, scope filter.RelationScope) ([]filter.FacetResult, error)
	// Walk is the id column, as text, of the rows of schema's table f
	// selects, every row for a nil f, in the walk order of keys past token
	// (from the first for a nil token), as the provider's lists read them
	// before a page is presented: a reverse token's rows walk backward.
	Walk(ctx context.Context, schema *filter.ResourceSchema, f *filter.ParsedFilter, keys []filter.CursorKey, token *filter.PageToken) ([]string, error)
	// Rows is every row of table in key order, a value per column by name
	// as a scan reads it, NULL as nil.
	Rows(ctx context.Context, table string) ([]map[string]any, error)
	// Include is the targets field's relation pairs owners with, as the
	// provider's lists include them (filter.ResolveInclude).
	Include(ctx context.Context, field *filter.FieldSchema, owners []core.Key, opts ...filter.IncludeQueryOption) (filter.IncludeRows, error)
	// Begin begins a transaction of the application's own.
	Begin(ctx context.Context) (StoreTx, error)
}

// StoreTx is a transaction of the application's own on a Store: its Put
// writes in it and its Rows read in it, as the Store's do, and Tx is it as
// resources run in it (parse.Resource.In). Settling Tx settles it.
type StoreTx interface {
	Tx() *parse.Tx
	Put(ctx context.Context, table string, rows ...map[string]any) error
	Rows(ctx context.Context, table string) ([]map[string]any, error)
}

// StoreColumn is a column a Store declares: its name and the type of the
// values it holds.
type StoreColumn struct {
	Name string
	Type core.TypeInfo
}

// errNoSQL is a statement sent to the Memory engine's handle.
var errNoSQL = errors.New("corpus: the memory engine runs no SQL: the case needs a SQL engine (sqlCase.sqlOnly)")

// storeDB is store as the *sql.DB handle a case holds beside its provider,
// so a case's fixtures load through the same calls on every engine
// (sqlBackend.load, create, insert, filterIDs). It runs no statement: a
// case that sends it one fails with errNoSQL, and needs SQL.
func storeDB(t *testing.T, store Store) *sql.DB {
	t.Helper()
	db := sql.OpenDB(noSQL{store: store})
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// noSQL is the connector and driver of a storeDB, holding its store.
type noSQL struct{ store Store }

func (noSQL) Connect(context.Context) (driver.Conn, error) { return nil, errNoSQL }

func (c noSQL) Driver() driver.Driver { return c }

func (noSQL) Open(string) (driver.Conn, error) { return nil, errNoSQL }

// storeOf is the Store a storeDB holds.
func storeOf(t testing.TB, db *sql.DB) Store {
	t.Helper()
	store, ok := asStore(db)
	require.True(t, ok, "the memory engine's handle is a storeDB")
	return store
}

// asStore is the Store db holds, when it is the Memory engine's handle.
func asStore(db *sql.DB) (Store, bool) {
	c, ok := db.Driver().(noSQL)
	return c.store, ok
}

// sqlOnly skips t on the Memory engine, which runs no SQL, naming what the
// case asks of a SQL database.
func (b sqlBackend) sqlOnly(t *testing.T, reason string) {
	t.Helper()
	if b.engine == Memory {
		t.Skip("needs SQL: " + reason)
	}
}

// tableRows are the named columns of each of table's rows in db, in the
// order of the columns: on a SQL engine, as a SELECT of them reads them.
// Their order is the engine's: a caller compares them as a set.
func (b sqlBackend) tableRows(t *testing.T, db *sql.DB, table string, columns ...string) [][]any {
	t.Helper()
	if b.engine != Memory {
		return dbtest.QueryRows[[]any](t, db, "SELECT "+b.quoteIdents(columns)+" FROM "+b.dialect.QuoteIdent(table))
	}
	rows, err := storeOf(t, db).Rows(t.Context(), table)
	require.NoError(t, err)
	out := make([][]any, len(rows))
	for i, row := range rows {
		out[i] = make([]any, len(columns))
		for j, c := range columns {
			v, ok := row[c]
			require.True(t, ok, "table %s has a column %s", table, c)
			out[i][j] = v
		}
	}
	return out
}

// storeColumns are table's columns as a Store declares them.
func storeColumns(table sqlTable) []StoreColumn {
	columns := make([]StoreColumn, len(table.columns))
	for i, c := range table.columns {
		columns[i] = StoreColumn{Name: c.name, Type: c.kind}
	}
	return columns
}

// storeRows are rows of table as a Store writes them: a value per column by
// name, a nil pointer, slice or map as NULL, and a document's text, which
// a fixture writes as its JSON, as that JSON.
func storeRows(t testing.TB, table sqlTable, rows [][]any) []map[string]any {
	t.Helper()
	out := make([]map[string]any, len(rows))
	for r, row := range rows {
		require.Len(t, row, len(table.columns), "a row of %s", table.name)
		m := make(map[string]any, len(row))
		for i, v := range row {
			v, err := core.Deref(v)
			require.NoError(t, err)
			rv := reflect.ValueOf(v)
			if (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Map) && rv.IsNil() {
				v = nil
			}
			kind := table.columns[i].kind.Type
			text, isText := v.(string)
			if isText && (core.IsDocument(kind) || kind == core.FieldTypeGeo) {
				v = json.RawMessage(text)
			}
			m[table.columns[i].name] = v
		}
		out[r] = m
	}
	return out
}
