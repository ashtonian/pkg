//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Stored points on PostGIS: jsonb read as PostgreSQL casts its members.
func TestGeoMatrix_PostGIS(t *testing.T) {
	runGeoMatrix(t, dbtest.PostGIS(t), filter.PostgresDialect{}, "JSONB", "%s::jsonb")
}
