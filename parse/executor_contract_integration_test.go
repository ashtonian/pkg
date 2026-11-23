//go:build integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestExecutorContract_Postgres(t *testing.T) {
	runExecutorContract(t, func(t *testing.T) *sql.DB { return dbtest.Postgres(t) }, filter.PostgresDialect{}, "TIMESTAMPTZ")
}
