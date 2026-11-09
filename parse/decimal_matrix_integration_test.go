//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Exact decimals on PostgreSQL 18: numeric amounts and jsonb prices.
func TestDecimalMatrix_PostgreSQL(t *testing.T) {
	runDecimalMatrix(t, decimalDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, amountType: "NUMERIC", docType: "JSONB", cast: "%s::jsonb"})
}
