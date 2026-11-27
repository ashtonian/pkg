//go:build integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestPolicyStoredValues_Postgres(t *testing.T) {
	runPolicyStoredValues(t, dbtest.Postgres(t), filter.PostgresDialect{})
}
