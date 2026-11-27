//go:build unit

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
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
