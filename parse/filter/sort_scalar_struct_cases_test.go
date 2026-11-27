//go:build unit || integration

package filter_test

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// sortCode is an application's codec: a struct its column holds as text.
type sortCode struct{ s string }

func (c sortCode) Value() (driver.Value, error) { return c.s, nil }

// codedItem sorts by a codec its storage tag declares a string.
type codedItem struct {
	ID   int64    `json:"id"   db:"id"`
	Code sortCode `json:"code" db:"code" parse:"sort" storage:"scalar:string"`
}

// runCodedWalks stores codedSeeds in a text column of the database's
// default collation and walks the list by the declared codec, forwards and
// backwards at every page size, holding each walk to the order the cursor
// comparator gives the codes (filter.CompareKeyValues): code points, not
// the collation the column's own ORDER BY follows.
func runCodedWalks(t *testing.T, db *sql.DB, d filter.Dialect) {
	// Codes whose code-point order (A B a b z é) is not a collation's
	// (a A b B é z).
	codedSeeds := []string{"B", "a", "A", "b", "é", "z"}
	sorts, err := filter.GenerateSortSchema[codedItem](core.NewSchemaGenerator())
	require.NoError(t, err)
	code := sorts.Fields["code"]
	require.Equal(t, core.FieldTypeString, code.DataType, "the codec sorts as the string it declares")
	table := code.Table
	_, err = db.ExecContext(t.Context(), "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY, code TEXT NOT NULL)")
	require.NoError(t, err)
	insert := "INSERT INTO " + table + " (id, code) VALUES (" + d.Placeholder(1) + ", " + d.Placeholder(2) + ")"
	ids := make([]int64, len(codedSeeds))
	for i, s := range codedSeeds {
		ids[i] = int64(i + 1)
		_, err = db.ExecContext(t.Context(), insert, ids[i], sortCode{s})
		require.NoError(t, err)
	}

	key := filter.CursorKey{Name: "code", DataType: code.DataType}
	want := slices.Clone(ids)
	slices.SortStableFunc(want, func(a, b int64) int {
		c, err := filter.CompareKeyValues(key, sortCode{codedSeeds[a-1]}, sortCode{codedSeeds[b-1]})
		require.NoError(t, err)
		return c
	})

	schema := &filter.ResourceSchema{
		Name: table, PrimaryKey: []string{"id"},
		SystemFields: map[string]*filter.FieldSchema{
			"id": {ExternalName: "id", Table: table, Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64},
		},
		Fields: map[string]*filter.FieldSchema{},
	}
	w := listWalk[codedItem]{
		lq:    mustListQuery(t, schema, sorts, d),
		query: func(t *testing.T, res *filter.ListResult) []codedItem { return queryCoded(t, db, table, res) },
		id:    func(r codedItem) int64 { return r.ID },
	}
	w.check(t, "code", want)
	descending := slices.Clone(want)
	slices.Reverse(descending)
	w.check(t, "-code", descending)

	_, ok := d.(filter.PostgresDialect)
	if ok {
		collated := orderedIDs(t, db, "SELECT id FROM "+table+" ORDER BY code, id")
		assert.NotEqual(t, toInts(want), collated, "the column's own collation orders the codes otherwise")
	}
}

func queryCoded(t *testing.T, db *sql.DB, table string, res *filter.ListResult) []codedItem {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT "+table+".id, "+table+".code FROM "+table+" "+res.Fragment, res.Args...)
	require.NoError(t, err, res.Fragment)
	defer func() { require.NoError(t, rows.Close()) }()
	var items []codedItem
	for rows.Next() {
		var (
			r    codedItem
			text string
		)
		require.NoError(t, rows.Scan(&r.ID, &text))
		r.Code = sortCode{text}
		items = append(items, r)
	}
	require.NoError(t, rows.Err())
	return items
}

func toInts(ids []int64) []int {
	out := make([]int, len(ids))
	for i, id := range ids {
		out[i] = int(id)
	}
	return out
}
