//go:build integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestRawPolicy_Postgres(t *testing.T) {
	runRawPolicy(t, filter.PostgresDialect{}, func(t *testing.T) *sql.DB { return dbtest.Postgres(t) })
}
