//go:build unit

package parse_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// Cursor keys: every cursor a page mints decodes, sort keys are scalars, and
// string keys order and seek by code point on every dialect.

// A page whose boundary row holds a long sort value fails with E2065
// instead of returning a cursor the next request rejects.
func TestNewPage_RefusesACursorItCouldNotDecode(t *testing.T) {
	plan := filter.CursorPlan{Keys: []filter.CursorKey{{Name: "name", Column: "name", DataType: core.FieldTypeString, Direction: filter.SortAsc}}}
	rows := []string{strings.Repeat("a", 4000), strings.Repeat("b", 4000)}

	_, err := filter.NewPage(rows, 1, plan, func(s, _ string) (any, error) { return s, nil })

	var pe *core.ParseError
	require.True(t, errors.As(err, &pe), "got %v", err)
	assert.Equal(t, filter.ECodeCursorTooLarge.String(), pe.Code)
}

// Every cursor a page can mint decodes.
func TestNewPage_MintedCursorsDecode(t *testing.T) {
	plan := filter.CursorPlan{Keys: []filter.CursorKey{{Name: "name", Column: "name", DataType: core.FieldTypeString, Direction: filter.SortAsc}}}
	rows := []string{strings.Repeat("a", 2000), strings.Repeat("b", 2000)}

	page, err := filter.NewPage(rows, 1, plan, func(s, _ string) (any, error) { return s, nil })
	require.NoError(t, err)
	_, err = filter.DecodeCursor(string(page.PageInfo.EndCursor))
	assert.NoError(t, err)
}

// A JSON document or array cannot be a sort key; a struct whose value
// reduces to a scalar can.
func TestGenerateSortSchema_ScalarKeysOnly(t *testing.T) {
	type labelled struct {
		ID     string         `json:"id" db:"id" parse:"sort"`
		Labels map[string]any `json:"labels" db:"labels" parse:"sort"`
	}
	_, err := parse.NewResource[labelled]("labelleds")
	assert.ErrorContains(t, err, "json fields are not sortable")

	type tagged struct {
		ID   string   `json:"id" db:"id" parse:"sort"`
		Tags []string `json:"tags" db:"tags" parse:"sort"`
	}
	_, err = parse.NewResource[tagged]("taggeds")
	assert.ErrorContains(t, err, "array fields are not sortable")

	type priced struct {
		ID    string          `json:"id" db:"id" parse:"sort"`
		Price decimal.Decimal `json:"price" db:"price" parse:"sort"`
	}
	p, err := parse.NewResource[priced]("priceds")
	require.NoError(t, err)
	assert.Contains(t, p.SortSchema().Fields, "price")
}

// String keys order and seek by code point on every dialect.
func TestListQuery_StringKeysOrderByCodePoint(t *testing.T) {
	schema := &filter.ResourceSchema{Name: "probe", PrimaryKey: []string{"rank"}}
	sorts := &filter.SortSchema{Fields: map[string]*filter.SortFieldSchema{
		"name": {Table: "probe", Column: "name", DataType: core.FieldTypeString},
		"rank": {Table: "probe", Column: "rank", DataType: core.FieldTypeInt},
	}}
	for _, tc := range []struct {
		dialect filter.Dialect
		collate string
	}{
		{filter.PostgresDialect{}, `"probe"."name" COLLATE pg_c_utf8`},
		{filter.PostgresDialect{Collation: filter.CollationC}, `"probe"."name" COLLATE "C"`},
		{filter.SQLiteDialect{}, `"probe"."name" COLLATE BINARY`},
	} {
		lq, err := filter.NewListQuery(schema, sorts, tc.dialect)
		require.NoError(t, err)
		q, err := lq.Build(filter.ListSort("name,rank"))
		require.NoError(t, err)
		assert.Contains(t, q.OrderBy, tc.collate+" ASC", tc.dialect.Name())
		assert.NotContains(t, q.OrderBy, `"probe"."rank" COLLATE`, "non-string keys keep their own order")
	}
}
