//go:build integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Keyset walks over computed and member keys on PostgreSQL (jsonb
// documents).
func TestKeysetRender_Postgres(t *testing.T) {
	runKeysetRender(t, renderDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, docType: "JSONB", storage: filter.StorageJSONB})
}
