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
	runPathNumberWalks(t, measureDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, docType: "JSONB", storage: filter.StorageJSONB},
		postgresMeasureOrder)
}
