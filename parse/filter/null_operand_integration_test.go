//go:build integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestNullOperands_Postgres(t *testing.T) {
	runNullOperands(t, dbtest.Postgres(t), filter.PostgresDialect{})
}
