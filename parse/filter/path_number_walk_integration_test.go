//go:build integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// PostgreSQL orders a number member exactly (::numeric), so members one
// float64 apart order apart: the walk resumes at each boundary without
// repeating or skipping a row.
func TestPathNumberWalk_Postgres(t *testing.T) {
	runPathNumberWalks(t, postgresMeasures(t), postgresMeasureOrder)
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
	return measureDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, docType: "JSONB", storage: filter.StorageJSONB}
}
