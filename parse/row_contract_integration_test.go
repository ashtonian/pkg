//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestRowContract_Postgres(t *testing.T) {
	runRowContract(t, dbtest.Postgres(t), filter.PostgresDialect{})
}
