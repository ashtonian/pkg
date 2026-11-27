//go:build unit

package filter_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// A memory backend orders a float path's members by the values its
// cursor carries (KeyValue, CompareKeyValues): exactly, as PostgreSQL
// orders them.
func TestPathNumberKeyValues_OrderAsPostgres(t *testing.T) {
	keys := filter.Keyset(&filter.SortResult{Fields: []filter.SortField{{Name: "n", Direction: filter.SortAsc, Schema: parsetest.MeasureSorts().Fields["n"]}}},
		filter.CursorKey{Name: "id", Table: "measures", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64})
	seeds := parsetest.MeasureSeeds()
	values := map[int64][]any{}
	for _, seed := range seeds {
		token, err := filter.CursorPlan{Keys: keys}.Token(false, func(column string) (any, error) {
			if column == "doc" {
				return seed.Doc, nil
			}
			return seed.ID, nil
		})
		require.NoError(t, err)
		values[seed.ID] = []any{token.Fields[0].Value, token.Fields[1].Value}
	}
	ids := make([]int64, 0, len(seeds))
	for _, seed := range seeds {
		ids = append(ids, seed.ID)
	}
	slices.SortFunc(ids, func(a, b int64) int {
		for i, k := range keys {
			c, err := filter.CompareKeyValues(k, values[a][i], values[b][i])
			require.NoError(t, err)
			if c != 0 {
				return c
			}
		}
		return 0
	})
	assert.Equal(t, parsetest.PostgresMeasureOrder(), ids)
}
