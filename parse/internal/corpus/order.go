//go:build integration

package corpus

import (
	"database/sql"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/ordertest"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// Every ordertest case's values, each held in the column type its case
// names for the engine, order, seek and facet in the case's order.
func canonicalOrder(t *testing.T, engine Engine) {
	runOn(t, engine, func(t *testing.T, b sqlBackend) {
		if b.engine == Memory {
			runCanonicalOrderMemory(t, b, ordertest.Cases())
			return
		}
		columnType := func(c ordertest.Case) string { return c.Postgres }
		if b.engine == SQLite {
			columnType = func(c ordertest.Case) string { return c.SQLite }
		}
		runCanonicalOrderSQL(t, b.db(t), b.dialect, ordertest.Cases(), columnType)
	})
}

// The SQLite driver scans a column declared DATE, DATETIME or TIMESTAMP
// into a time.Time; such a column holds its type's order and cursors as
// a TEXT column does.
func canonicalOrderSQLiteTimeColumns(t *testing.T, engine Engine) {
	declared := map[string]string{"date": "DATE", "timestamp": "TIMESTAMP"}
	var cases []ordertest.Case
	for _, c := range ordertest.Cases() {
		_, ok := declared[c.Name]
		if ok {
			cases = append(cases, c)
		}
	}
	b := sqlBackendOf(engine)
	runCanonicalOrderSQL(t, b.db(t), b.dialect, cases, func(c ordertest.Case) string { return declared[c.Name] })
}

// runCanonicalOrderSQL stores each case's values in a column of its type
// (columnType) and holds the database to the case's order: a key column's ORDER BY
// (filter.KeyOrder), a sort key's (filter.OrderByKeys), NULL last, its
// seeks (filter.SeekPredicate, runSeeks), and a terms facet's keys, which
// are the canonical texts of the values as the database holds them
// (Dialect.StoredKeyValue), ties in the type's order, as Go counts them
// (filter.FacetKeys, filter.FacetBuckets).
func runCanonicalOrderSQL(t *testing.T, db *sql.DB, d filter.Dialect, cases []ordertest.Case, columnType func(ordertest.Case) string) {
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			ctx := t.Context()
			table := "ordered_" + c.Name
			_, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER NOT NULL, v "+columnType(c)+")")
			require.NoError(t, err)
			insert := "INSERT INTO " + table + " (id, v) VALUES (" + d.Placeholder(1) + ", " + d.Placeholder(2) + ")"
			// Stored last to first, so no order is the insertion order.
			_, err = db.ExecContext(ctx, insert, len(c.Values), nil)
			require.NoError(t, err)
			ascending := make([]int, len(c.Values))
			held := make([]any, len(c.Values))
			for i := len(c.Values) - 1; i >= 0; i-- {
				stored, err := c.Stored(c.Values[i])
				require.NoError(t, err)
				_, err = db.ExecContext(ctx, insert, i, stored)
				require.NoError(t, err, "storing %#v", stored)
				ascending[i] = i
				held[i], err = d.StoredKeyValue(c.Type, stored)
				require.NoError(t, err)
			}

			column := d.QuoteIdent(table) + "." + d.QuoteIdent("v")
			got := orderedIDs(t, db, "SELECT id FROM "+table+" WHERE v IS NOT NULL ORDER BY "+filter.KeyOrder(d, column, c.Type)+", id")
			assert.Equal(t, ascending, got, "a key column's order")

			// Each row's value as the driver scans the column the way a
			// resource reads it mints a cursor the next request accepts,
			// holding the value the database holds.
			key := filter.CursorKey{Name: "v", Table: table, Column: "v", DataType: c.Type.Type, NumberKind: c.Type.NumberKind, Nullable: true, Direction: filter.SortAsc}
			for id, raw := range scannedValues(t, db, "SELECT id, "+d.ReadColumn(column, readType(c.Type))+" FROM "+table+" WHERE v IS NOT NULL") {
				resumed := parsetest.ResumeKeyValue(t, key, raw)
				got, err := filter.CompareKeyValues(key, resumed, held[id])
				require.NoError(t, err, "%#v resumed as %#v", raw, resumed)
				assert.Zero(t, got, "%#v resumed as %#v", raw, resumed)
			}

			if c.Sortable {
				got = orderedIDs(t, db, "SELECT id FROM "+table+" "+filter.OrderByKeys(d, []filter.CursorKey{key}, false))
				assert.Equal(t, append(slices.Clone(ascending), len(c.Values)), got, "a sort key's order, NULL last")
				runSeeks(t, db, d, table, c, key)
			}

			if c.Faceted {
				field := &filter.FieldSchema{ExternalName: "v", Table: table, Column: "v", DataType: c.Type.Type, NumberKind: c.Type.NumberKind, Facetable: true}
				schema := &filter.ResourceSchema{Name: table, Fields: map[string]*filter.FieldSchema{"v": field}}
				q, err := filter.BuildFacetQuery(schema, d, "1=1", nil, []filter.FacetRequest{{Field: "v", Limit: len(c.Values)}})
				require.NoError(t, err)
				results := queryFacets(t, db, q.SQL, q.Args...)
				require.Len(t, results, 1)

				counts := map[string]int64{}
				want := make([]filter.FacetBucket, len(c.Values))
				for i, v := range held {
					text, err := core.CanonicalText(c.Type, v)
					require.NoError(t, err)
					want[i] = filter.FacetBucket{Key: text, Count: 1}
					keys, err := filter.FacetKeys(v, field)
					require.NoError(t, err)
					for _, k := range keys {
						counts[k]++
					}
				}
				assert.Equal(t, want, results[0].Buckets, "the statement's facet")
				assert.Equal(t, want, filter.FacetBuckets(field, counts, len(c.Values)), "the facet counted in Go")
			}
		})
	}
}

// runCanonicalOrderMemory holds the Memory engine to each case's order as
// runCanonicalOrderSQL holds a database: a table keyed by the value holds
// its rows in the case's order; each row's value, read as a resource reads
// it, mints a cursor holding the value stored; a sort key walks the rows
// in that order, NULL last, and seeks past each row's cursor forwards and
// backwards, with the id tiebreaker too; and a terms facet keys each value
// by its canonical text, ties in the type's order.
func runCanonicalOrderMemory(t *testing.T, b sqlBackend, cases []ordertest.Case) {
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			db := b.db(t)
			table := "ordered_" + c.Name
			v := sqlColumn{name: "v", kind: c.Type}
			id := column[int64]("id").required()
			null := len(c.Values) // the NULL row's id
			stored := make([]any, null)
			// Stored last to first, so no order is the insertion order.
			rows := [][]any{{null, nil}}
			var keyed [][]any
			ascending := make([]int, null)
			for i := null - 1; i >= 0; i-- {
				s, err := c.Stored(c.Values[i])
				require.NoError(t, err)
				stored[i], ascending[i] = s, i
				rows = append(rows, []any{i, s})
				keyed = append(keyed, []any{s, i})
			}
			b.load(t, db, sqlTable{name: table, key: []string{"id"}, columns: []sqlColumn{id, v}}, rows)
			b.load(t, db, sqlTable{name: table + "_by_v", key: []string{"v"}, columns: []sqlColumn{v.required(), id}}, keyed)
			var byKey []int
			for _, row := range b.tableRows(t, db, table+"_by_v", "id") {
				byKey = append(byKey, int(asInt64(t, row[0])))
			}
			assert.Equal(t, ascending, byKey, "a key column's order")

			key := filter.CursorKey{Name: "v", Table: table, Column: "v", DataType: c.Type.Type, NumberKind: c.Type.NumberKind, Nullable: true, Direction: filter.SortAsc}
			scanned := map[int]any{}
			for _, row := range b.tableRows(t, db, table, "id", "v") {
				scanned[int(asInt64(t, row[0]))] = row[1]
			}
			held := make([]any, null)
			for i, s := range stored {
				var err error
				held[i], err = filter.KeyValue(key, s)
				require.NoError(t, err, "%#v", s)
				resumed := parsetest.ResumeKeyValue(t, key, scanned[i])
				got, err := filter.CompareKeyValues(key, resumed, held[i])
				require.NoError(t, err, "%#v resumed as %#v", scanned[i], resumed)
				assert.Zero(t, got, "%#v resumed as %#v", scanned[i], resumed)
			}

			field := &filter.FieldSchema{ExternalName: "v", Table: table, Column: "v", DataType: c.Type.Type, NumberKind: c.Type.NumberKind, Facetable: true}
			schema := &filter.ResourceSchema{Name: table, Fields: map[string]*filter.FieldSchema{"v": field}}
			store := storeOf(t, db)
			walk := func(t *testing.T, f *filter.ParsedFilter, keys []filter.CursorKey, token *filter.PageToken) []int {
				t.Helper()
				texts, err := store.Walk(t.Context(), schema, f, keys, token)
				require.NoError(t, err)
				var ids []int
				for _, text := range texts {
					n, err := strconv.Atoi(text)
					require.NoError(t, err)
					ids = append(ids, n)
				}
				return ids
			}
			if c.Sortable {
				assert.Equal(t, append(slices.Clone(ascending), null), walk(t, nil, []filter.CursorKey{key}, nil), "a sort key's order, NULL last")
				present := filter.NewParsedFilter(&filter.FilterAST{Root: &filter.FilterNode{Field: field, Op: filter.OpNotNull}})
				nonNull := key
				nonNull.Nullable = false
				rowID := filter.CursorKey{Name: "id", Table: table, Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64, Direction: filter.SortAsc}
				for _, reverse := range []bool{false, true} {
					for i := 0; i <= null; i++ {
						resumed := parsetest.ResumeKeyValue(t, key, scanned[i])
						got := walk(t, nil, []filter.CursorKey{key}, &filter.PageToken{
							Fields:  []filter.CursorField{{Name: key.Name, Value: resumed, Direction: key.Direction}},
							Reverse: reverse,
						})
						assert.Equal(t, idsAfter(i, null, reverse), got, "the rows after %#v, reverse %t", resumed, reverse)
						if i == null {
							continue
						}
						got = walk(t, present, []filter.CursorKey{nonNull, rowID}, &filter.PageToken{
							Fields:  []filter.CursorField{{Name: key.Name, Value: resumed, Direction: key.Direction}, {Name: "id", Value: int64(i), Direction: filter.SortAsc}},
							Reverse: reverse,
						})
						assert.Equal(t, idsAfter(i, null-1, reverse), got, "the rows after (%#v, %d), reverse %t", resumed, i, reverse)
					}
				}
			}

			if c.Faceted {
				results, err := store.Facets(t.Context(), schema, nil, []filter.FacetRequest{{Field: "v", Limit: null}}, filter.RelationScope{})
				require.NoError(t, err)
				require.Len(t, results, 1)
				counts := map[string]int64{}
				want := make([]filter.FacetBucket, null)
				for i, v := range held {
					text, err := core.CanonicalText(c.Type, v)
					require.NoError(t, err)
					want[i] = filter.FacetBucket{Key: text, Count: 1}
					keys, err := filter.FacetKeys(v, field)
					require.NoError(t, err)
					for _, k := range keys {
						counts[k]++
					}
				}
				assert.Equal(t, want, results[0].Buckets, "the list's facet")
				assert.Equal(t, want, filter.FacetBuckets(field, counts, null), "the facet counted in Go")
			}
		})
	}
}

// runSeeks holds every row's cursor to the rows after it in the walk order:
// SeekPredicate, from the value the row's cursor resumes from, selects
// exactly the rows ORDER BY puts after it, forwards and backwards. The
// nullable key alone seeks from every row, the NULL row's included; the key
// with the id tiebreaker seeks over the rows that hold a value, as a native
// row-value comparison where the dialect has one.
func runSeeks(t *testing.T, db *sql.DB, d filter.Dialect, table string, c ordertest.Case, key filter.CursorKey) {
	t.Helper()
	column := d.QuoteIdent(table) + "." + d.QuoteIdent("v")
	scanned := scannedValues(t, db, "SELECT id, "+d.ReadColumn(column, readType(c.Type))+" FROM "+table)
	null := len(c.Values) // the NULL row's id
	held := key
	held.Nullable = false
	id := filter.CursorKey{Name: "id", Table: table, Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64, Direction: filter.SortAsc}
	for _, reverse := range []bool{false, true} {
		for i := 0; i <= null; i++ {
			v := parsetest.ResumeKeyValue(t, key, scanned[i])
			got := seekIDs(t, db, d, table, "", []filter.CursorKey{key}, &filter.PageToken{
				Fields:  []filter.CursorField{{Name: key.Name, Value: v, Direction: key.Direction}},
				Reverse: reverse,
			})
			assert.Equal(t, idsAfter(i, null, reverse), got, "the rows after %#v, reverse %t", v, reverse)
			if i == null {
				continue
			}
			got = seekIDs(t, db, d, table, column+" IS NOT NULL AND ", []filter.CursorKey{held, id}, &filter.PageToken{
				Fields:  []filter.CursorField{{Name: key.Name, Value: v, Direction: key.Direction}, {Name: "id", Value: int64(i), Direction: filter.SortAsc}},
				Reverse: reverse,
			})
			assert.Equal(t, idsAfter(i, null-1, reverse), got, "the rows after (%#v, %d), reverse %t", v, i, reverse)
		}
	}
}

// idsAfter is the ids 0 to last after i in ascending order, or before it
// in descending order when reverse is set.
func idsAfter(i, last int, reverse bool) []int {
	var ids []int
	if reverse {
		for j := i - 1; j >= 0; j-- {
			ids = append(ids, j)
		}
		return ids
	}
	for j := i + 1; j <= last; j++ {
		ids = append(ids, j)
	}
	return ids
}

// seekIDs is the ids of table's rows strictly after token in the walk
// order of keys, filtered by where (a condition ending in AND, or "").
func seekIDs(t *testing.T, db *sql.DB, d filter.Dialect, table, where string, keys []filter.CursorKey, token *filter.PageToken) []int {
	t.Helper()
	var args []any
	bind := func(v any) string {
		args = append(args, v)
		return d.Placeholder(len(args))
	}
	seek, err := filter.SeekPredicate(d, keys, token, bind)
	require.NoError(t, err)
	query := "SELECT id FROM " + table + " WHERE " + where + seek + " " + filter.OrderByKeys(d, keys, token.Reverse)
	return dbtest.QueryRows[int](t, db, query, args...)
}

// orderedIDs is the ids query selects, in its order: one row at least.
func orderedIDs(t *testing.T, db *sql.DB, query string) []int {
	t.Helper()
	ids := dbtest.QueryRows[int](t, db, query)
	require.NotEmpty(t, ids, query)
	return ids
}

// scannedValues is each row's second column by its first, an id, as the
// driver scans it into an any.
func scannedValues(t *testing.T, db *sql.DB, query string) map[int]any {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), query)
	require.NoError(t, err, query)
	defer func() { _ = rows.Close() }()
	values := map[int]any{}
	for rows.Next() {
		var (
			id  int
			raw any
		)
		require.NoError(t, rows.Scan(&id, &raw))
		values[id] = raw
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, values, query)
	return values
}

// readType is the SQL type a resource reads a column of type t through
// (Dialect.ReadColumn): its declared one, or the one its type implies.
func readType(t core.TypeInfo) core.SQLType {
	if t.SQLType != "" {
		return t.SQLType
	}
	implied, _ := core.ImpliedSQLType(t)
	return implied
}
