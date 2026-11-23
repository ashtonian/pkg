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

// A request string holding U+0000 is refused before any statement exists:
// a flat parameter's value and a search term as E2073, a cursor's key value
// as E2082. The JSON grammar's values are filtertest's cases.
func TestNUL_RefusedInRequestText(t *testing.T) {
	code := func(t *testing.T, err error) string {
		t.Helper()
		pe, ok := errors.AsType[*core.ParseError](err)
		require.True(t, ok, "%v is no ParseError", err)
		return pe.Code
	}

	t.Run("flat parameter", func(t *testing.T) {
		_, err := filter.NewQueryParamParser(testSchema()).Parse(url.Values{"name": {"Al\x00pha"}})
		assert.Equal(t, filter.ECodeValueTypeMismatch.String(), code(t, err))
	})

	for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
		t.Run("search on "+d.Name(), func(t *testing.T) {
			lq := mustListQuery(t, testSchema(), nil, d).WithSearchSchema(testSearchSchema())
			_, err := lq.Build(filter.ListSearch("thermo\x00stat"))
			assert.Equal(t, filter.ECodeValueTypeMismatch.String(), code(t, err))
			pe, _ := errors.AsType[*core.ParseError](err)
			assert.Equal(t, filter.ParamSearch, pe.Details[core.DetailParameter])
		})
	}

	t.Run("cursor", func(t *testing.T) {
		type row struct {
			ID   string `json:"id" db:"id" parse:"sort"`
			Name string `json:"name" db:"name" parse:"sort"`
		}
		schema, err := filter.GenerateSortSchema[row](core.NewSchemaGenerator())
		require.NoError(t, err)
		sort, err := filter.ParseSort(schema, "name,id")
		require.NoError(t, err)
		plan := filter.CursorPlan{Keys: filter.Keyset(sort, filter.CursorKey{})}
		value := row{ID: "a", Name: "a\x00b"}
		token, err := plan.Token(false, func(col string) (any, error) { return filter.CursorValueByTag(value, "db", col) })
		require.NoError(t, err)
		encoded, err := filter.EncodeCursor(token)
		require.NoError(t, err)
		decoded, err := filter.DecodeCursor(encoded)
		require.NoError(t, err)
		assert.Equal(t, filter.ECodeInvalidCursor.String(), code(t, filter.ValidateCursor(decoded, plan.Keys)))
	})
}
