//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Quoted integers on SQLite: decimal-text operands, INTEGER storage.
func TestQuotedMatrix_SQLite(t *testing.T) {
	runQuotedMatrix(t, dbtest.SQLite(t), filter.SQLiteDialect{}, "INTEGER")
}
