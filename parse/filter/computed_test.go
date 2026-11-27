//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

const (
	pgTagCount     = "SELECT count(*) FROM tags WHERE tags.item_id = items.id"
	sqliteTagCount = "SELECT count(1) FROM tags WHERE tags.item_id = items.id"
	labelExpr      = "lower(items.label)"
)

// computedItems computes count through countSQL, the tag count in the
// dialect the schema compiles for, and name alike in every dialect.
func computedItems(countSQL string) (*filter.ResourceSchema, *filter.SortSchema, *filter.SearchSchema) {
	fs := testSchema()
	fs.Fields["count"].Facetable = true
	fs.Computed = []filter.ComputedColumn{
		{Column: "count", SQL: countSQL},
		{Column: "name", SQL: labelExpr},
	}
	ss := testSortSchema()
	ss.Fields["count"] = &filter.SortFieldSchema{Table: "items", Column: "count", DataType: core.FieldTypeInt}
	return fs, ss, testSearchSchema()
}

// A list reads a computed column through its expression: its filter, sort
// keys, search, count and facets. The cursor keeps the row's column, which
// the page reads the key from.
func TestComputed_ListReadsTheExpression(t *testing.T) {
	for _, tt := range []struct {
		dialect filter.Dialect
		sql     string
		search  string
	}{
		{filter.PostgresDialect{}, pgTagCount, "coalesce((" + labelExpr + "), '')"},
		{filter.SQLiteDialect{}, sqliteTagCount, "(" + labelExpr + ") LIKE "},
	} {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			fs, ss, search := computedItems(tt.sql)
			res, err := mustListQuery(t, fs, ss, tt.dialect).WithSearchSchema(search).Build(
				filter.ListFilter(`{"count":{"$gt":1},"name":"a"}`),
				filter.ListSort("-count"),
				filter.ListSearch("x"),
				filter.ListWithCount(true),
				filter.ListFacets("count"),
			)
			require.NoError(t, err)
			count := "(" + tt.sql + ")"
			assert.Contains(t, res.Where, count+" > ")
			assert.Contains(t, res.Where, "("+labelExpr+") = ")
			assert.Contains(t, res.OrderBy, count+" DESC")
			assert.Contains(t, res.CountSQL, count+" > ")
			assert.Contains(t, res.Where, tt.search)
			assert.Contains(t, res.CountSQL, tt.search)
			assert.Contains(t, res.Facets.SQL, count)
			for _, sql := range []string{res.Fragment, res.CountSQL, res.Facets.SQL} {
				assert.NotContains(t, sql, `"items"."count"`)
			}
			require.NotEmpty(t, res.Cursor.Keys)
			assert.Equal(t, "count", res.Cursor.Keys[0].Column)

			assert.Equal(t, `"items"."count"`, filter.FieldExpr(filter.PostgresDialect{}, fs.Fields["count"]), "the schema names the column")
			assert.Empty(t, ss.Fields["count"].Expr)
		})
	}
}

// A membership constraint on a computed column compares its expression.
func TestComputed_MembershipComparesTheExpression(t *testing.T) {
	fs, _, _ := computedItems(pgTagCount)
	ast := &filter.FilterAST{Root: &filter.FilterNode{Field: fs.Fields["count"], Op: filter.OpMember, Member: &filter.Lookup{Table: "quotas", Project: "n"}}}
	res, err := filter.NewSQLBuilder(fs, filter.PostgresDialect{}).Build(ast)
	require.NoError(t, err)
	assert.Contains(t, res.Where, "("+pgTagCount+") IN (SELECT ")
}

// A keyset reads its key from the row's own column, never an expression.
func TestNewListQuery_RefusesAComputedKey(t *testing.T) {
	fs, ss, _ := computedItems(pgTagCount)
	fs.Computed = append(fs.Computed, filter.ComputedColumn{Column: "id", SQL: pgTagCount})
	_, err := filter.NewListQuery(fs, ss, filter.PostgresDialect{})
	assert.ErrorContains(t, err, `key "id" is computed`)
}

// A copy computes what the schema computes.
func TestComputed_Clone(t *testing.T) {
	fs, _, _ := computedItems(pgTagCount)
	clone := fs.Clone()
	clone.Computed[0].Column = "other"
	assert.Equal(t, "count", fs.Computed[0].Column)
	exprs := fs.Clone().ComputedSQL()
	assert.Equal(t, map[string]string{"count": "(" + pgTagCount + ")", "name": "(" + labelExpr + ")"}, exprs)
}
