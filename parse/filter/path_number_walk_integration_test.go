//go:build integration

package filter_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// PostgreSQL orders a number member exactly (::numeric), so members one
// float64 apart order apart: the walk resumes at each boundary without
// repeating or skipping a row.
func TestPathNumberWalk_Postgres(t *testing.T) {
	runPathNumberWalks(t, postgresMeasures(t), postgresMeasureOrder())
}

// A token an earlier release minted carries the member as a float64, which
// rounds 0.10000000000000001 to 0.1: resuming from the page that ended on
// row 2 returns row 3 (0.1) and row 2 itself again, as that release did.
func TestPathNumberWalk_PostgresResumesAFloat64TokenFromItsRoundedValue(t *testing.T) {
	m := postgresMeasures(t)
	storeMeasures(t, m)
	lq := mustListQuery(t, measureSchema(m.storage), measureSorts(m.storage), m.dialect)
	cursor, err := filter.EncodeCursor(&filter.PageToken{Fields: []filter.CursorField{
		{Name: "n", Value: 0.10000000000000001, Direction: filter.SortAsc},
		{Name: "id", Value: int64(2), Direction: filter.SortAsc},
	}})
	require.NoError(t, err)
	res, err := lq.Build(filter.ListSort("n"), filter.ListLimit(len(measureSeeds())), filter.ListCursor(cursor))
	require.NoError(t, err)
	var ids []int64
	for _, r := range queryMeasures(t, m.db, res) {
		ids = append(ids, r.ID)
	}
	assert.Equal(t, []int64{3, 2, 5, 6, 9, 10, 13, 7, 8, 11, 12}, ids, "the rows after (0.1, 2)")
}

// PostgreSQL compares a number member exactly (::numeric, or containment),
// as Evaluate does: 0.10000000000000001 is not 0.1.
func TestPathNumberFilters_Postgres(t *testing.T) {
	runPathNumberFilters(t, postgresMeasures(t), []string{
		`{"n":0.1}`, `{"n":{"$ne":1}}`, `{"n":{"$gt":0.1}}`, `{"n":{"$lt":0.1}}`, `{"n":{"$gte":1}}`, `{"n":{"$lte":0.1}}`,
		`{"n":{"$in":[0.1,1]}}`, `{"n":{"$nin":[0.1,1]}}`, `{"n":{"$null":true}}`, `{"n":{"$null":false}}`,
		`{"n":{"$gte":1e300}}`, `{"n":{"$lte":-1e300}}`, `{"n":{"$gt":0}}`,
		`{"ns":{"$some":{"$eq":0.1}}}`, `{"ns":{"$some":{"$gt":0.1}}}`, `{"ns":{"$contains":0.1}}`, `{"ns":{"$null":true}}`,
	})
}

func TestPathNumberFacets_Postgres(t *testing.T) {
	runPathNumberFacets(t, postgresMeasures(t))
}

func postgresMeasures(t *testing.T) measureDB {
	measures := measureDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, docType: "JSONB", storage: filter.StorageJSONB}
	return measures
}
