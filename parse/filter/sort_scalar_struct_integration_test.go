//go:build integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// PostgreSQL orders the codec's text column by its collation; the
// declared key orders and pages by code point, as the cursor compares.
func TestCodedWalks_Postgres(t *testing.T) {
	runCodedWalks(t, dbtest.Postgres(t), filter.PostgresDialect{})
}
