//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Typed JSON paths on PostgreSQL, for each storage a document column can
// declare.
func TestPathMatrix_Postgres(t *testing.T) {
	for _, tc := range []struct {
		name    string
		docType string
		cast    string
		storage filter.JSONStorage
	}{
		{"jsonb", "JSONB", "%s::jsonb", ""},
		{"json", "JSON", "%s::json", filter.StorageJSON},
		{"text", "TEXT", "", filter.StorageText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runPathMatrix(t, pathDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, docType: tc.docType, cast: tc.cast}, tc.storage)
		})
	}
}

// Keyset walks over path sort keys on PostgreSQL (jsonb documents).
func TestPathWalks_Postgres(t *testing.T) {
	runPathWalks(t, pathDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, docType: "JSONB", cast: "%s::jsonb"})
}

// Facets over paths on PostgreSQL (jsonb documents).
func TestPathFacets_Postgres(t *testing.T) {
	runPathFacets(t, pathDB{db: dbtest.Postgres(t), dialect: filter.PostgresDialect{}, docType: "JSONB", cast: "%s::jsonb"})
}

// $has_key on PostgreSQL jsonb.
func TestHasKey_Postgres(t *testing.T) {
	runHasKey(t, dbtest.Postgres(t), filter.PostgresDialect{}, "JSONB", "::jsonb")
}

// A merged typed-document member reads back through its path on PostgreSQL.
func TestPathPatches_Postgres(t *testing.T) {
	runPathPatches(t, dbtest.Postgres(t), filter.PostgresDialect{}, "JSONB", "::jsonb")
}
