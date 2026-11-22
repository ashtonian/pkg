//go:build unit

package filter_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

type numberParams struct {
	N     int64   `json:"n"     db:"n"     parse:"filter"`
	Price float64 `json:"price" db:"price" parse:"filter"`
}

// A flat parameter is read as a JSON number, as the JSON filter and the
// SDK read one: Go's wider literal grammar binds none of these.
func TestQueryParamParser_NumbersTakeTheJSONGrammar(t *testing.T) {
	schema, err := filter.GenerateFilterSchema[numberParams](core.NewSchemaGenerator())
	require.NoError(t, err)
	qp := filter.NewQueryParamParser(schema)

	for _, tt := range []struct{ name, value string }{
		{"n", "abc"},
		{"n", "0x1e1"},
		{"n", ".5e1"},
		{"n", "+7"},
		{"n", "007.0"},
		{"n", "0b11e0"},
		{"price", "0x1p-2"},
		{"price", "1_0"},
		{"price", ".5"},
		{"price", "5."},
		{"price", "NaN"},
		{"n_in", "1,0x2"},
	} {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			_, err := qp.Parse(url.Values{tt.name: {tt.value}})
			var perr *core.ParseError
			require.True(t, errors.As(err, &perr), "got %v", err)
			assert.Equal(t, core.CodeValueTypeMismatch.String(), perr.Code, perr.Message)
		})
	}

	for _, tt := range []struct {
		name, value string
		want        any
	}{
		{"n", "1e3", int64(1000)},
		{"n", "7.0", int64(7)},
		{"n", "-0", int64(0)},
		{"price", "2.5e-1", 0.25},
	} {
		t.Run(tt.name+"="+tt.value, func(t *testing.T) {
			ast, err := qp.Parse(url.Values{tt.name: {tt.value}})
			require.NoError(t, err)
			result, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast)
			require.NoError(t, err)
			assert.Equal(t, []any{tt.want}, result.Args)
		})
	}
}
