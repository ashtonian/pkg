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
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// SQLite reads a number member as the INTEGER or REAL json_extract
// returns, so members one float64 apart tie and break on the id: the
// walk resumes at each boundary in that order.
func TestPathNumberWalk_SQLite(t *testing.T) {
	runPathNumberWalks(t, sqliteMeasures(t), sqliteMeasureOrder())
}

// SQLite agrees with Evaluate on which members are numbers, and on every
// comparison the REAL json_extract reads decides as the exact value does.
func TestPathNumberFilters_SQLite(t *testing.T) {
	runPathNumberFilters(t, sqliteMeasures(t), []string{
		`{"n":{"$null":true}}`, `{"n":{"$null":false}}`, `{"n":{"$gt":0.5}}`, `{"n":{"$lt":0.5}}`,
		`{"n":{"$gte":1e300}}`, `{"n":{"$lte":-1e300}}`, `{"ns":{"$some":{"$gt":0.5}}}`, `{"ns":{"$null":true}}`,
	})
}

func TestPathNumberFacets_SQLite(t *testing.T) {
	runPathNumberFacets(t, sqliteMeasures(t))
}

func sqliteMeasures(t *testing.T) measureDB {
	measures := measureDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, docType: "TEXT"}
	return measures
}

// A memory backend orders a float path's members by the values its
// cursor carries (KeyValue, CompareKeyValues): exactly, as PostgreSQL
// orders them.
func TestPathNumberKeyValues_OrderAsPostgres(t *testing.T) {
	keys := filter.Keyset(&filter.SortResult{Fields: []filter.SortField{{Name: "n", Direction: filter.SortAsc, Schema: measureSorts("").Fields["n"]}}},
		filter.CursorKey{Name: "id", Table: "measures", Column: "id", DataType: core.FieldTypeInt, NumberKind: reflect.Int64})
	seeds := measureSeeds()
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
	assert.Equal(t, postgresMeasureOrder(), ids)
}
