//go:build integration

package corpus

import (
	"database/sql"
	"regexp"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// planPrice is a SQLite row whose decimal column holds its canonical text.
type planPrice struct {
	ID     int64           `json:"id"     db:"id"     parse:"filter,sort"`
	Amount decimal.Decimal `json:"amount" db:"amount" parse:"filter,sort"`
}

// SQLite compares a decimal by its key for equality and sets, as it does for
// ranges, sorts and keyset seeks, so the one expression index the operations
// guide recommends, the key and then the keyset's tiebreaker, serves each.
func setPlansSQLiteDecimalKeyIndex(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	db := b.db(t)
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[planPrice](g)
	require.NoError(t, err)
	schema.PrimaryKey = []string{"id"}
	sorts, err := filter.GenerateSortSchema[planPrice](g)
	require.NoError(t, err)
	table := schema.Name
	index := table + "_amount_key"
	dbtest.Exec(t, db,
		"CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, amount TEXT NOT NULL)",
		"INSERT INTO "+table+" WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 20000) SELECT n, (n % 1000) || '.' || (n % 7) FROM g",
		"CREATE INDEX "+index+" ON "+table+" (parse_decimal_key(amount), id)",
		"ANALYZE")
	lq := mustListQuery(t, schema, sorts, b.dialect)

	plan := func(t *testing.T, opts ...filter.ListOption) string {
		t.Helper()
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		return queryPlan(t, db, "SELECT id FROM "+table+" "+res.Fragment, res.Args)
	}
	// The query reads only the key and id, so the index covers it.
	search := regexp.MustCompile(`SEARCH ` + table + ` USING COVERING INDEX ` + index + ` \(<expr>[=<>]`)
	sorted := func(t *testing.T, p string) {
		t.Helper()
		assert.Contains(t, p, " USING COVERING INDEX "+index, p)
		assert.NotContains(t, p, "TEMP B-TREE", "the index orders the page")
	}

	t.Run("equality", func(t *testing.T) {
		assert.Regexp(t, search, plan(t, filter.ListFilter(`{"amount":"500.3"}`)))
	})
	t.Run("a set", func(t *testing.T) {
		assert.Regexp(t, search, plan(t, filter.ListFilter(`{"amount":{"$in":["500.3","7.0","12"]}}`)))
	})
	t.Run("a range", func(t *testing.T) {
		assert.Regexp(t, search, plan(t, filter.ListFilter(`{"amount":{"$gt":"500","$lte":"510.5"}}`)))
	})
	t.Run("a sort", func(t *testing.T) {
		sorted(t, plan(t, filter.ListSort("amount")))
		sorted(t, plan(t, filter.ListSort("-amount")))
	})
	t.Run("a keyset seek", func(t *testing.T) {
		first, err := lq.Build(filter.ListSort("amount"))
		require.NoError(t, err)
		token, err := first.Cursor.Token(false, func(column string) (any, error) {
			return map[string]any{"amount": "500.3", "id": int64(1500)}[column], nil
		})
		require.NoError(t, err)
		cursor, err := filter.EncodeCursor(token)
		require.NoError(t, err)
		p := plan(t, filter.ListSort("amount"), filter.ListCursor(cursor))
		assert.Regexp(t, search, p)
		sorted(t, p)
	})
}

// A keyset seek over several keys walking one direction is a row-value
// comparison on SQLite, as on PostgreSQL, its text keys collated on the
// operand, so SQLite's planner seeks the sort's index to the cursor instead
// of scanning it from the start: the plan SEARCHes the index and needs no
// sort of its own.
func keysetSeekSQLiteSearchesTheSortIndex(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	db := b.db(t)
	b.create(t, db, stockTable())
	dbtest.Exec(t, db,
		`CREATE TABLE walk_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE INDEX walk_items_name_id ON walk_items (name, id)`,
		`CREATE INDEX stock_items_qty_key ON stock_items (qty, warehouse_id, sku)`)
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
	walks := mustListQuery(t, walkFilters, walkSorts, b.dialect)
	stocks := mustListQuery(t, stockFilters, stockSorts, b.dialect)

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

// pricedItem's amount is a decimal held as TEXT, which SQLite orders through
// parse_decimal_key: an index on the key's expression serves its seeks.
type pricedItem struct {
	ID     int64           `json:"id"     db:"id"     parse:"filter,sort"`
	Qty    int64           `json:"qty"    db:"qty"    parse:"filter,sort"`
	Amount decimal.Decimal `json:"amount" db:"amount" parse:"filter,sort"`
}

// SQLite seeks a row value's index only from a bare leading column, so a
// seek led by an expression, here a decimal's order key, also bounds that
// key on its own, which an index on the expression seeks: the plan SEARCHes
// the sort's index, led by the expression or by a bare column before it,
// and needs no sort of its own.
func keysetSeekSQLiteDecimalKeySearchesItsIndex(t *testing.T, engine Engine) {
	b := sqlBackendOf(engine)
	db := b.db(t)
	dbtest.Exec(t, db,
		`CREATE TABLE priced_items (id INTEGER PRIMARY KEY, qty INTEGER NOT NULL, amount TEXT NOT NULL)`,
		`INSERT INTO priced_items WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < 20000) SELECT n, n % 50, (n % 1000) || '.' || (n % 7) FROM g`,
		`CREATE INDEX priced_items_amount_key ON priced_items (parse_decimal_key(amount), id)`,
		`CREATE INDEX priced_items_qty_amount_key ON priced_items (qty, parse_decimal_key(amount), id)`,
		`CREATE INDEX priced_items_amount_qty_key ON priced_items (parse_decimal_key(amount), qty, id)`,
		`ANALYZE`)
	g := core.NewSchemaGenerator(core.WithSQLTableName("priced_items"))
	fs, err := filter.GenerateFilterSchema[pricedItem](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"id"}
	ss, err := filter.GenerateSortSchema[pricedItem](g)
	require.NoError(t, err)
	lq := mustListQuery(t, fs, ss, b.dialect)
	amount := func(dir filter.SortDirection) filter.CursorField {
		field := filter.CursorField{Name: "amount", Value: "500.3", Direction: dir}
		return field
	}
	id := func(dir filter.SortDirection) filter.CursorField {
		field := filter.CursorField{Name: "id", Value: int64(1500), Direction: dir}
		return field
	}
	qty := filter.CursorField{Name: "qty", Value: int64(5), Direction: filter.SortAsc}

	for _, tt := range []struct {
		name   string
		sort   string
		cursor string
		index  string
		seek   string
	}{
		{"a decimal key and the key", "amount", mintCursor(t, false, amount(filter.SortAsc), id(filter.SortAsc)), "priced_items_amount_key", "<expr>>?"},
		{"a descending decimal key", "-amount", mintCursor(t, false, amount(filter.SortDesc), id(filter.SortDesc)), "priced_items_amount_key", "<expr><?"},
		{"backwards over a decimal key", "amount", mintCursor(t, true, amount(filter.SortAsc), id(filter.SortAsc)), "priced_items_amount_key", "<expr><?"},
		{"a decimal key, a bare key and the key", "amount,qty", mintCursor(t, false, amount(filter.SortAsc), qty, id(filter.SortAsc)), "priced_items_amount_qty_key", "<expr>>?"},
		{"a bare key, a decimal key and the key", "qty,amount", mintCursor(t, false, qty, amount(filter.SortAsc), id(filter.SortAsc)), "priced_items_qty_amount_key", "qty>?"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := lq.Build(filter.ListSort(tt.sort), filter.ListLimit(10), filter.ListCursor(tt.cursor))
			require.NoError(t, err)
			plan := queryPlan(t, db, "SELECT id FROM priced_items "+res.Fragment, res.Args)
			assert.Contains(t, plan, "SEARCH priced_items USING COVERING INDEX "+tt.index+" ("+tt.seek+")", res.Fragment)
			assert.NotContains(t, plan, "TEMP B-TREE", res.Fragment)
		})
	}
}

// queryPlan is SQLite's EXPLAIN QUERY PLAN of query, one detail per line.
func queryPlan(t *testing.T, db *sql.DB, query string, args []any) string {
	t.Helper()
	var details []string
	for _, row := range dbtest.QueryRows[[]any](t, db, "EXPLAIN QUERY PLAN "+query, args...) {
		detail, ok := row[3].(string)
		require.True(t, ok, "a plan's detail is text: %T", row[3])
		details = append(details, detail)
	}
	return strings.Join(details, "\n")
}
