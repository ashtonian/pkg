//go:build unit

package filter_test

import (
	"errors"
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

var errNoSQLite = errors.New("no sqlite expression")

// computedItems computes count per dialect and name alike; tagCount serves
// count for each dialect.
func computedItems(tagCount filter.ComputedExpr) (*filter.ResourceSchema, *filter.SortSchema, *filter.SearchSchema) {
	fs := testSchema()
	fs.Fields["count"].Facetable = true
	fs.Computed = []filter.ComputedColumn{
		{Column: "count", Expr: tagCount},
		{Column: "name", Expr: func(filter.Dialect) (string, error) { return labelExpr, nil }},
	}
	ss := testSortSchema()
	ss.Fields["count"] = &filter.SortFieldSchema{Table: "items", Column: "count", DataType: core.FieldTypeInt}
	return fs, ss, testSearchSchema()
}

func tagCountFor(d filter.Dialect) (string, error) {
	if _, ok := d.(filter.SQLiteDialect); ok {
		return sqliteTagCount, nil
	}
	return pgTagCount, nil
}

// A list reads a computed column through the expression of the dialect it
// is built for: its filter, sort keys, search, count and facets. The
// cursor keeps the row's column, which the page reads the key from.
func TestComputed_ListReadsTheDialectsExpression(t *testing.T) {
	for _, tt := range []struct {
		dialect filter.Dialect
		count   string
		other   string
		search  string
	}{
		{filter.PostgresDialect{}, "(" + pgTagCount + ")", sqliteTagCount, "coalesce((" + labelExpr + "), '')"},
		{filter.SQLiteDialect{}, "(" + sqliteTagCount + ")", pgTagCount, "(" + labelExpr + ") LIKE "},
	} {
		t.Run(tt.dialect.Name(), func(t *testing.T) {
			fs, ss, search := computedItems(tagCountFor)
			res, err := mustListQuery(t, fs, ss, tt.dialect).WithSearchSchema(search).Build(
				filter.ListFilter(`{"count":{"$gt":1},"name":"a"}`),
				filter.ListSort("-count"),
				filter.ListSearch("x"),
				filter.ListWithCount(true),
				filter.ListFacets("count"),
			)
			require.NoError(t, err)
			assert.Contains(t, res.Where, tt.count+" > ")
			assert.Contains(t, res.Where, "("+labelExpr+") = ")
			assert.Contains(t, res.OrderBy, tt.count+" DESC")
			assert.Contains(t, res.CountSQL, tt.count+" > ")
			assert.Contains(t, res.Where, tt.search)
			assert.Contains(t, res.CountSQL, tt.search)
			assert.Contains(t, res.Facets.SQL, tt.count)
			for _, sql := range []string{res.Fragment, res.CountSQL, res.Facets.SQL} {
				assert.NotContains(t, sql, tt.other)
				assert.NotContains(t, sql, `"items"."count"`)
			}
			require.NotEmpty(t, res.Cursor.Keys)
			assert.Equal(t, "count", res.Cursor.Keys[0].Column)

			assert.Equal(t, `"items"."count"`, filter.FieldExpr(filter.PostgresDialect{}, fs.Fields["count"]), "the schema stays dialect-neutral")
			assert.Empty(t, ss.Fields["count"].Expr)
		})
	}
}

// A dialect an expression refuses fails every build over the schema, an
// empty filter's included, with the expression's own error rather than a
// filter error.
func TestComputed_RefusedDialectFailsTheBuild(t *testing.T) {
	refuseSQLite := func(d filter.Dialect) (string, error) {
		if _, ok := d.(filter.SQLiteDialect); ok {
			return "", errNoSQLite
		}
		return pgTagCount, nil
	}
	fs, ss, _ := computedItems(refuseSQLite)
	ast, err := filter.NewFilterParser(fs, nil).Parse(`{"status":"active"}`)
	require.NoError(t, err)

	for name, build := range map[string]func(filter.Dialect) error{
		"SQLBuilder": func(d filter.Dialect) error {
			_, err := filter.NewSQLBuilder(fs, d).Build(ast)
			return err
		},
		"ListQuery": func(d filter.Dialect) error {
			_, err := mustListQuery(t, fs, ss, d).Build()
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := build(filter.SQLiteDialect{})
			require.ErrorIs(t, err, errNoSQLite)
			_, isParseError := errors.AsType[*core.ParseError](err)
			assert.False(t, isParseError, "a dialect refusal is no filter error")
			assert.NoError(t, build(filter.PostgresDialect{}))
		})
	}

	fs.Computed = append(fs.Computed, filter.ComputedColumn{Column: "status"})
	_, err = filter.NewSQLBuilder(fs, filter.PostgresDialect{}).Build(ast)
	assert.ErrorContains(t, err, `computed column "status" has no expression`)
}

// A membership constraint on a computed column compares its expression.
func TestComputed_MembershipComparesTheExpression(t *testing.T) {
	fs, _, _ := computedItems(tagCountFor)
	ast := &filter.FilterAST{Root: &filter.FilterNode{Field: fs.Fields["count"], Op: filter.OpMember, Member: &filter.Lookup{Table: "quotas", Project: "n"}}}
	res, err := filter.NewSQLBuilder(fs, filter.PostgresDialect{}).Build(ast)
	require.NoError(t, err)
	assert.Contains(t, res.Where, "("+pgTagCount+") IN (SELECT ")
}

// A keyset reads its key from the row's own column, never an expression.
func TestNewListQuery_RefusesAComputedKey(t *testing.T) {
	fs, ss, _ := computedItems(tagCountFor)
	fs.Computed = append(fs.Computed, filter.ComputedColumn{Column: "id", Expr: tagCountFor})
	_, err := filter.NewListQuery(fs, ss, filter.PostgresDialect{})
	assert.ErrorContains(t, err, `key "id" is computed`)
}

// A copy computes what the schema computes.
func TestComputed_Clone(t *testing.T) {
	fs, _, _ := computedItems(tagCountFor)
	clone := fs.Clone()
	clone.Computed[0].Column = "other"
	assert.Equal(t, "count", fs.Computed[0].Column)
	exprs, err := fs.Clone().ComputedSQL(filter.PostgresDialect{})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"count": "(" + pgTagCount + ")", "name": "(" + labelExpr + ")"}, exprs)
}
