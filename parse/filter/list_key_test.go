//go:build unit

package filter_test

import (
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
		"no schema":     {nil, nil, "needs a filter schema"},
		"no key":        {keyed(func(s *filter.ResourceSchema) { s.PrimaryKey = "" }), nil, "declares no key"},
		"no such field": {keyed(func(s *filter.ResourceSchema) { s.PrimaryKey = "id; DROP TABLE items" }), nil, "is no sort, filter or system field"},
		"untyped":       {keyed(func(s *filter.ResourceSchema) { s.SystemFields["id"].DataType = "" }), nil, "declares no type"},
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
	fs.PrimaryKey = "id"
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
