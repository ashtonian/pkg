//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Exact decimals on SQLite: amounts held as decimal text, prices in JSON
// text, compared through parsesqlite's functions.
func TestDecimalMatrix_SQLite(t *testing.T) {
	runDecimalMatrix(t, decimalDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, amountType: "TEXT", docType: "TEXT"})
}
