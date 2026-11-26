//go:build integration

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
	"github.com/ashtonian/pkg/parse/internal/ordertest"
)

func TestCanonicalOrder_Postgres(t *testing.T) {
	runCanonicalOrderSQL(t, dbtest.Postgres(t), filter.PostgresDialect{}, func(c ordertest.Case) string { return c.Postgres })
}
