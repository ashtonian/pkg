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

func sortSchemaAB() *filter.SortSchema {
	field := func(name string) *filter.SortFieldSchema {
		f := &filter.SortFieldSchema{Table: "items", Column: name, DataType: core.FieldTypeInt}
		return f
	}
	schema := &filter.SortSchema{
		Fields:       map[string]*filter.SortFieldSchema{"a": field("a"), "b": field("b"), "c": field("c")},
		DefaultField: "b",
		DefaultDir:   filter.SortDesc,
		MaxFields:    2,
	}
	return schema
}

func sortNames(r *filter.SortResult) []string {
	var out []string
	for _, f := range r.Fields {
		out = append(out, string(f.Direction)+" "+f.Name)
	}
	return out
}

// A sort parameter names its fields once each: blank items are no fields,
// so they count toward no limit, and a parameter with no field left is the
// default sort, as an absent one is.
func TestParseSort_NamesEachFieldOnce(t *testing.T) {
	schema := sortSchemaAB()
	for _, tt := range []struct {
		param string
		want  []string
	}{
		{"", []string{"desc b"}},
		{",", []string{"desc b"}},
		{" , ,", []string{"desc b"}},
		{"a,,b,", []string{"asc a", "asc b"}},
		{"-a, ,+c", []string{"desc a", "asc c"}},
	} {
		got, err := filter.ParseSort(schema, tt.param)
		require.NoError(t, err, "sort=%q", tt.param)
		assert.Equal(t, tt.want, sortNames(got), "sort=%q", tt.param)
	}

	for _, param := range []string{"a,-a", "a,a", "-b,+b"} {
		_, err := filter.ParseSort(schema, param)
		require.ErrorIs(t, err, filter.ECodeSortFieldNotAllowed, "sort=%q", param)
		pe, ok := errors.AsType[*core.ParseError](err)
		require.True(t, ok)
		assert.Contains(t, pe.Message, "more than once", "sort=%q", param)
	}

	_, err := filter.ParseSort(schema, "a,b,c")
	require.ErrorIs(t, err, filter.ECodeSortFieldLimit, "three fields exceed MaxFields 2")
}
