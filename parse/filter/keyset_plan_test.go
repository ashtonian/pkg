//go:build unit

package filter_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// A keyset seek over several keys walking one direction is a row-value
// comparison on SQLite, as on PostgreSQL, its text keys collated on the
// operand, so SQLite's planner seeks the sort's index to the cursor instead
// of scanning it from the start: the plan SEARCHes the index and needs no
// sort of its own.
func TestKeysetSeek_SQLiteSearchesTheSortIndex(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, ddl := range []string{
		`CREATE TABLE walk_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE INDEX walk_items_name_id ON walk_items (name, id)`,
		`CREATE TABLE stock_items (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty INTEGER NOT NULL, PRIMARY KEY (warehouse_id, sku))`,
		`CREATE INDEX stock_items_qty_key ON stock_items (qty, warehouse_id, sku)`,
	} {
		_, err := db.ExecContext(t.Context(), ddl)
		require.NoError(t, err)
	}
	g := core.NewSchemaGenerator()
	walkFilters, err := filter.GenerateFilterSchema[walkItem](g)
	require.NoError(t, err)
	walkFilters.PrimaryKey = []string{"id"}
	walkSorts, err := filter.GenerateSortSchema[walkItem](g)
	require.NoError(t, err)
	stockFilters, err := filter.GenerateFilterSchema[stockItem](g)
	require.NoError(t, err)
	stockFilters.PrimaryKey = []string{"warehouse_id", "sku"}
	stockSorts, err := filter.GenerateSortSchema[stockItem](g)
	require.NoError(t, err)
	walks := mustListQuery(t, walkFilters, walkSorts, filter.SQLiteDialect{})
	stocks := mustListQuery(t, stockFilters, stockSorts, filter.SQLiteDialect{})

	tests := []struct {
		name   string
		lq     *filter.ListQuery
		table  string
		index  string
		sort   string
		cursor string
	}{
		{"two keys forwards", walks, "walk_items", "walk_items_name_id", "name", mintCursor(t, false,
			filter.CursorField{Name: "name", Value: "m", Direction: filter.SortAsc},
			filter.CursorField{Name: "id", Value: int64(7), Direction: filter.SortAsc})},
		{"two keys backwards", walks, "walk_items", "walk_items_name_id", "name", mintCursor(t, true,
			filter.CursorField{Name: "name", Value: "m", Direction: filter.SortAsc},
			filter.CursorField{Name: "id", Value: int64(7), Direction: filter.SortAsc})},
		{"two keys descending", walks, "walk_items", "walk_items_name_id", "-name", mintCursor(t, false,
			filter.CursorField{Name: "name", Value: "m", Direction: filter.SortDesc},
			filter.CursorField{Name: "id", Value: int64(7), Direction: filter.SortDesc})},
		{"three keys", stocks, "stock_items", "stock_items_qty_key", "qty", mintCursor(t, false,
			filter.CursorField{Name: "qty", Value: int64(5), Direction: filter.SortAsc},
			filter.CursorField{Name: "warehouse_id", Value: "w1", Direction: filter.SortAsc},
			filter.CursorField{Name: "sku", Value: "b", Direction: filter.SortAsc})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := tt.lq.Build(filter.ListSort(tt.sort), filter.ListLimit(10), filter.ListCursor(tt.cursor))
			require.NoError(t, err)
			plan := queryPlan(t, db, "SELECT * FROM "+tt.table+" "+res.Fragment, res.Args)
			assert.Contains(t, plan, "SEARCH "+tt.table+" USING COVERING INDEX "+tt.index+" (", res.Fragment)
			assert.NotContains(t, plan, "USE TEMP B-TREE FOR ORDER BY", res.Fragment)
		})
	}
}

// A text key the column declares NOCASE still walks by code point, every
// row once in each direction: the operand's BINARY collation decides each
// comparison of the row-value seek.
func TestKeysetSeek_SQLiteNoCaseKeyWalksByCodePoint(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE walk_items (id INTEGER PRIMARY KEY, name TEXT COLLATE NOCASE NOT NULL)`,
		`INSERT INTO walk_items VALUES (1, 'b'), (2, 'B'), (3, 'a'), (4, 'C'), (5, 'b'), (6, 'A')`,
	} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err)
	}
	g := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[walkItem](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"id"}
	ss, err := filter.GenerateSortSchema[walkItem](g)
	require.NoError(t, err)
	lq := mustListQuery(t, fs, ss, filter.SQLiteDialect{})

	for _, tt := range []struct {
		sort string
		want []int64
	}{
		{"name", []int64{6, 2, 4, 3, 1, 5}},
		{"-name", []int64{5, 1, 3, 4, 2, 6}},
	} {
		t.Run(tt.sort, func(t *testing.T) {
			var seen []int64
			cursor := ""
			for range len(tt.want) + 1 {
				opts := []filter.ListOption{filter.ListSort(tt.sort), filter.ListLimit(1)}
				if cursor != "" {
					opts = append(opts, filter.ListCursor(cursor))
				}
				res, err := lq.Build(opts...)
				require.NoError(t, err)
				items := walkItems(t, db, res)
				page, err := filter.NewPage(items, res.Limit, res.Cursor, func(it walkItem, col string) (any, error) {
					return filter.CursorValueByTag(it, "db", col)
				})
				require.NoError(t, err)
				for _, it := range page.Items {
					seen = append(seen, it.ID)
				}
				if !page.PageInfo.HasNextPage {
					break
				}
				cursor = string(page.PageInfo.EndCursor)
			}
			assert.Equal(t, tt.want, seen)
		})
	}
}

// noCaseNames reads the name column through an expression of its own
// collation, as a mapper serving case-insensitive lookups might.
type noCaseNames struct{}

func (noCaseNames) MapColumn(table, column string, _ core.FieldType) string {
	expr := `"` + table + `"."` + column + `"`
	if column == "name" {
		return expr + " COLLATE NOCASE"
	}
	return expr
}

// A key whose expression carries its own collation still walks by code
// point, every row once in each direction: the seek collates the
// expression, where an explicit collation on the operand would lose to the
// expression's.
func TestKeysetSeek_SQLiteCollatedExpressionWalksByCodePoint(t *testing.T) {
	db := dbtest.SQLite(t)
	for _, stmt := range []string{
		`CREATE TABLE walk_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`INSERT INTO walk_items VALUES (1, 'B'), (2, 'a'), (3, 'C')`,
	} {
		_, err := db.ExecContext(t.Context(), stmt)
		require.NoError(t, err)
	}
	g := core.NewSchemaGenerator(core.WithColumnMapper(noCaseNames{}), core.WithSQLTableName("walk_items"))
	fs, err := filter.GenerateFilterSchema[walkItem](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"id"}
	ss, err := filter.GenerateSortSchema[walkItem](g)
	require.NoError(t, err)
	require.Equal(t, `"walk_items"."name" COLLATE NOCASE`, ss.Fields["name"].Expr)
	lq := mustListQuery(t, fs, ss, filter.SQLiteDialect{})

	for _, tt := range []struct {
		sort string
		want []int64
	}{
		{"name", []int64{1, 3, 2}},
		{"-name", []int64{2, 3, 1}},
	} {
		t.Run(tt.sort, func(t *testing.T) {
			var seen []int64
			cursor := ""
			for range len(tt.want) + 1 {
				opts := []filter.ListOption{filter.ListSort(tt.sort), filter.ListLimit(1)}
				if cursor != "" {
					opts = append(opts, filter.ListCursor(cursor))
				}
				res, err := lq.Build(opts...)
				require.NoError(t, err)
				page, err := filter.NewPage(walkItems(t, db, res), res.Limit, res.Cursor, func(it walkItem, col string) (any, error) {
					return filter.CursorValueByTag(it, "db", col)
				})
				require.NoError(t, err)
				for _, it := range page.Items {
					seen = append(seen, it.ID)
				}
				if !page.PageInfo.HasNextPage {
					break
				}
				cursor = string(page.PageInfo.EndCursor)
			}
			assert.Equal(t, tt.want, seen)
		})
	}
}

// walkItems are the walk_items rows res selects.
func walkItems(t *testing.T, db *sql.DB, res *filter.ListResult) []walkItem {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT id, name FROM walk_items "+res.Fragment, res.Args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var items []walkItem
	for rows.Next() {
		var it walkItem
		require.NoError(t, rows.Scan(&it.ID, &it.Name))
		items = append(items, it)
	}
	require.NoError(t, rows.Err())
	return items
}

// queryPlan is SQLite's EXPLAIN QUERY PLAN of query, one detail per line.
func queryPlan(t *testing.T, db *sql.DB, query string, args []any) string {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(details, "\n")
}
