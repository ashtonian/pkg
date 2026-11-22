//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Quoted integers on PostgreSQL: decimal-text operands, bigint storage.
func TestQuotedMatrix_PostgreSQL(t *testing.T) {
	runQuotedMatrix(t, dbtest.Postgres(t), filter.PostgresDialect{}, "BIGINT")
}
