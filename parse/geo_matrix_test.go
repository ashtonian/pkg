//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Stored points on SQLite: JSON text read through parsesqlite's functions.
func TestGeoMatrix_SQLite(t *testing.T) {
	runGeoMatrix(t, dbtest.SQLite(t), filter.SQLiteDialect{}, "TEXT", "")
}
