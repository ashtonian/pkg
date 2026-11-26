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

// A request string holding U+0000 or invalid UTF-8 is refused before any
// statement exists: a flat parameter's value and a search term as E2073, a
// cursor's key value as E2082. The query string carries either as written
// (url.ParseQuery does not validate UTF-8). The JSON grammar's values are
// filtertest's cases; its decoder refuses invalid UTF-8 itself.
func TestRequestText_Refused(t *testing.T) {
	code := func(t *testing.T, err error) string {
		t.Helper()
		pe, ok := errors.AsType[*core.ParseError](err)
		require.True(t, ok, "%v is no ParseError", err)
		return pe.Code
	}

	for _, text := range []struct{ name, bad string }{{"U+0000", "\x00"}, {"invalid UTF-8", "\xff"}} {
		t.Run("flat parameter, "+text.name, func(t *testing.T) {
			_, err := queryParams(t, testSchema()).Parse(url.Values{"name": {"Al" + text.bad + "pha"}})
			assert.Equal(t, filter.ECodeValueTypeMismatch.String(), code(t, err))
			assert.ErrorIs(t, err, core.ErrInvalidText)
		})
		t.Run("flat pattern parameter, "+text.name, func(t *testing.T) {
			_, err := queryParams(t, testSchema()).Parse(url.Values{"name_like": {"Al%" + text.bad}})
			assert.Equal(t, filter.ECodeValueTypeMismatch.String(), code(t, err))
		})
		for _, d := range []filter.Dialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
			t.Run("search on "+d.Name()+", "+text.name, func(t *testing.T) {
				lq := mustListQuery(t, testSchema(), nil, d).WithSearchSchema(testSearchSchema())
				_, err := lq.Build(filter.ListSearch("thermo" + text.bad + "stat"))
				assert.Equal(t, filter.ECodeValueTypeMismatch.String(), code(t, err))
				pe, _ := errors.AsType[*core.ParseError](err)
				assert.Equal(t, filter.ParamSearch, pe.Details[core.DetailParameter])
			})
		}
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
		for _, name := range []string{"a\x00b", "a\xffb"} {
			value := row{ID: "a", Name: name}
			token, err := plan.Token(false, func(col string) (any, error) { return filter.CursorValueByTag(value, "db", col) })
			require.NoError(t, err)
			encoded, err := filter.EncodeCursor(token)
			require.NoError(t, err)
			decoded, err := filter.DecodeCursor(encoded)
			require.NoError(t, err)
			assert.Equal(t, filter.ECodeInvalidCursor.String(), code(t, filter.ValidateCursor(decoded, plan.Keys)), "%q", name)
		}
	})
}
