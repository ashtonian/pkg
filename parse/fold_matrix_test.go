//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// `$ilike` case on SQLite, which lower-cases ASCII letters only.
func TestFoldMatrix_SQLite(t *testing.T) {
	runFoldMatrix(t, dbtest.SQLite(t), filter.SQLiteDialect{}, "TEXT", false)
}
