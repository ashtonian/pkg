//go:build unit

package filter_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// A list query cannot exist without a key it can end every keyset on: a
// typed column the schemas name.
func TestNewListQuery_RequiresATypedKey(t *testing.T) {
	keyed := func(edit func(s *filter.ResourceSchema)) *filter.ResourceSchema {
		s := testSchema()
		edit(s)
		return s
	}
	for name, tc := range map[string]struct {
		schema *filter.ResourceSchema
		sorts  *filter.SortSchema
		want   string
	}{
		"no schema":       {nil, nil, "needs a filter schema"},
		"no key":          {keyed(func(s *filter.ResourceSchema) { s.PrimaryKey = nil }), nil, "declares no key"},
		"no such field":   {keyed(func(s *filter.ResourceSchema) { s.PrimaryKey = []string{"id; DROP TABLE items"} }), nil, "is no sort, filter or system field"},
		"repeated column": {keyed(func(s *filter.ResourceSchema) { s.PrimaryKey = []string{"id", "id"} }), nil, `key column "id" is listed twice`},
		"empty column":    {keyed(func(s *filter.ResourceSchema) { s.PrimaryKey = []string{"id", ""} }), nil, "empty key column"},
		"second unknown":  {keyed(func(s *filter.ResourceSchema) { s.PrimaryKey = []string{"id", "nope"} }), nil, `key "nope" is no sort, filter or system field`},
		"untyped":         {keyed(func(s *filter.ResourceSchema) { s.SystemFields["id"].DataType = "" }), nil, "declares no type"},
		"computed": {keyed(func(*filter.ResourceSchema) {}), &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
			"id": {Expr: "lower(items.id)", DataType: core.FieldTypeString},
		}}, "is computed"},
	} {
		t.Run(name, func(t *testing.T) {
			lq, err := filter.NewListQuery(tc.schema, tc.sorts, filter.PostgresDialect{})
			assert.Nil(t, lq)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

type walkItem struct {
	ID   int64  `json:"id"   db:"id"   parse:"filter,sort"`
	Name string `json:"name" db:"name" parse:"filter,sort"`
}

// Six rows with one name walk as six rows two at a time: the key breaks
// every tie. A cursor whose key value the key's type cannot hold is E2082
// before any statement exists.
func TestListQuery_KeyBreaksTies(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.Exec(`CREATE TABLE walk_items (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO walk_items VALUES (1,'x'), (2,'x'), (3,'x'), (4,'x'), (5,'x'), (6,'x')`)
	require.NoError(t, err)
	g := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[walkItem](g)
	require.NoError(t, err)
	ss, err := filter.GenerateSortSchema[walkItem](g)
	require.NoError(t, err)
	_, err = filter.NewListQuery(fs, ss, filter.SQLiteDialect{})
	require.ErrorContains(t, err, "declares no key", "a generated schema declares no key until its resource does")
	fs.PrimaryKey = []string{"id"}
	lq := mustListQuery(t, fs, ss, filter.SQLiteDialect{})

	var seen []int64
	cursor := ""
	for range 6 {
		opts := []filter.ListOption{filter.ListSort("name"), filter.ListLimit(2)}
		if cursor != "" {
			opts = append(opts, filter.ListCursor(cursor))
		}
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		rows, err := db.Query("SELECT id, name FROM walk_items "+res.Fragment, res.Args...)
		require.NoError(t, err)
		var items []walkItem
		for rows.Next() {
			var it walkItem
			require.NoError(t, rows.Scan(&it.ID, &it.Name))
			items = append(items, it)
		}
		require.NoError(t, rows.Close())
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
	assert.Equal(t, []int64{1, 2, 3, 4, 5, 6}, seen)

	for _, value := range []any{"zzz", true, 1.5, "a\x00b"} {
		cur := mintCursor(t, false,
			filter.CursorField{Name: "name", Value: "x", Direction: filter.SortAsc},
			filter.CursorField{Name: "id", Value: value, Direction: filter.SortAsc})
		_, err := lq.Build(filter.ListSort("name"), filter.ListCursor(cur))
		assertParseCode(t, err, filter.ECodeInvalidCursor)
	}
}

type stockItem struct {
	WarehouseID string `json:"warehouse_id" db:"warehouse_id" parse:"filter,sort"`
	SKU         string `json:"sku"          db:"sku"          parse:"filter,sort"`
	Qty         int64  `json:"qty"          db:"qty"          parse:"filter,sort"`
}

// A key of two columns ends every keyset with each column the sort does not
// name, in key order and in the sort's primary direction, so rows that tie
// on the sort and on either column alone still page apart: every sort, both
// directions, every page size, forwards and back, reads each row once. A
// cursor minted under another key is E2082.
func TestListQuery_CompositeKeyBreaksTies(t *testing.T) {
	db := dbtest.SQLite(t)
	_, err := db.Exec(`CREATE TABLE stock_items (warehouse_id TEXT NOT NULL, sku TEXT NOT NULL, qty INTEGER NOT NULL, PRIMARY KEY (warehouse_id, sku))`)
	require.NoError(t, err)
	rows := []stockItem{{"w1", "a", 5}, {"w1", "b", 5}, {"w2", "a", 5}, {"w2", "b", 1}, {"w3", "a", 1}}
	for _, r := range rows {
		_, err := db.Exec(`INSERT INTO stock_items VALUES (?, ?, ?)`, r.WarehouseID, r.SKU, r.Qty)
		require.NoError(t, err)
	}
	g := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[stockItem](g)
	require.NoError(t, err)
	ss, err := filter.GenerateSortSchema[stockItem](g)
	require.NoError(t, err)
	fs.PrimaryKey = []string{"warehouse_id", "sku"}
	lq := mustListQuery(t, fs, ss, filter.SQLiteDialect{})

	keyset := func(sort string) []string {
		res, err := lq.Build(filter.ListSort(sort))
		require.NoError(t, err)
		names := make([]string, len(res.Cursor.Keys))
		for i, k := range res.Cursor.Keys {
			names[i] = k.Name + " " + string(k.Direction)
		}
		return names
	}
	assert.Equal(t, []string{"qty desc", "warehouse_id desc", "sku desc"}, keyset("-qty"))
	assert.Equal(t, []string{"sku asc", "warehouse_id asc"}, keyset("sku"), "a key column the sort names ends it once")

	fetch := func(res *filter.ListResult) []stockItem {
		rs, err := db.Query("SELECT warehouse_id, sku, qty FROM stock_items "+res.Fragment, res.Args...)
		require.NoError(t, err)
		var items []stockItem
		for rs.Next() {
			var it stockItem
			require.NoError(t, rs.Scan(&it.WarehouseID, &it.SKU, &it.Qty))
			items = append(items, it)
		}
		require.NoError(t, rs.Close())
		return items
	}
	build := func(opts ...filter.ListOption) *filter.ListResult {
		res, err := lq.Build(opts...)
		require.NoError(t, err)
		return res
	}
	page := func(res *filter.ListResult) *filter.Page[stockItem] {
		p, err := filter.NewPage(fetch(res), res.Limit, res.Cursor, func(it stockItem, col string) (any, error) {
			return filter.CursorValueByTag(it, "db", col)
		})
		require.NoError(t, err)
		return p
	}
	for _, sort := range []string{"", "qty", "-qty", "sku", "-sku", "warehouse_id", "-warehouse_id", "qty,-sku"} {
		want := fetch(build(filter.ListSort(sort), filter.ListLimit(len(rows))))
		require.Len(t, want, len(rows))
		for size := 1; size <= len(rows); size++ {
			t.Run(fmt.Sprintf("sort=%q/size=%d", sort, size), func(t *testing.T) {
				var forward []stockItem
				var last *filter.Page[stockItem]
				cursor := ""
				for range len(rows) + 1 {
					opts := []filter.ListOption{filter.ListSort(sort), filter.ListLimit(size)}
					if cursor != "" {
						opts = append(opts, filter.ListCursor(cursor))
					}
					last = page(build(opts...))
					forward = append(forward, last.Items...)
					if !last.PageInfo.HasNextPage {
						break
					}
					cursor = string(last.PageInfo.EndCursor)
				}
				assert.Equal(t, want, forward)

				backward := []stockItem{}
				for cursor = string(last.PageInfo.StartCursor); cursor != ""; {
					prev := page(build(filter.ListSort(sort), filter.ListLimit(size), filter.ListCursor(cursor)))
					backward = append(slices.Clone(prev.Items), backward...)
					cursor = ""
					if prev.PageInfo.HasPrevPage {
						cursor = string(prev.PageInfo.StartCursor)
					}
				}
				assert.Equal(t, want[:len(want)-len(last.Items)], backward, "walking back from the last page")
			})
		}
	}

	oneColumn := mintCursor(t, false,
		filter.CursorField{Name: "qty", Value: int64(5), Direction: filter.SortAsc},
		filter.CursorField{Name: "warehouse_id", Value: "w1", Direction: filter.SortAsc})
	_, err = lq.Build(filter.ListSort("qty"), filter.ListCursor(oneColumn))
	assertParseCode(t, err, filter.ECodeInvalidCursor)
}
