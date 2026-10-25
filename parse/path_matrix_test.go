//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Typed JSON paths on SQLite, whose documents are JSON text.
func TestPathMatrix_SQLite(t *testing.T) {
	runPathMatrix(t, pathDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, docType: "TEXT"}, "")
}

// Keyset walks over path sort keys on SQLite.
func TestPathWalks_SQLite(t *testing.T) {
	runPathWalks(t, pathDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, docType: "TEXT"})
}

// Facets over paths on SQLite.
func TestPathFacets_SQLite(t *testing.T) {
	runPathFacets(t, pathDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, docType: "TEXT"})
}

// $has_key on SQLite's JSON text, against jsonb `?` semantics.
func TestHasKey_SQLite(t *testing.T) {
	runHasKey(t, dbtest.SQLite(t), filter.SQLiteDialect{}, "TEXT", "")
}

// A merged typed-document member reads back through its path on SQLite.
func TestPathPatches_SQLite(t *testing.T) {
	runPathPatches(t, dbtest.SQLite(t), filter.SQLiteDialect{}, "TEXT", "")
}
