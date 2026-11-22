//go:build integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func governedPostgres(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.Postgres(t)
	for _, stmt := range governedFixtureSQL("TIMESTAMPTZ") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

func TestGovernedLists_Postgres(t *testing.T) {
	db := governedPostgres(t)
	runGovernedLists(t, filter.PostgresDialect{}, db)
}

func TestGovernedLists_MemoryMatchesPostgres(t *testing.T) {
	db := governedPostgres(t)
	r := governedDocuments(t)
	runGovernedMemory(t, func(t *testing.T, filterJSON string) governedResult {
		return listGoverned(t, r, filter.PostgresDialect{}, db, filterJSON, parse.Scope{Governance: governanceA()})
	})
}

// PostgreSQL has no operator between uuid and text: the comparison is by
// the UUID's text.
func TestGovernedMixedTypes_Postgres(t *testing.T) {
	db := dbtest.Postgres(t)
	runGovernedMixedTypes(t, filter.PostgresDialect{}, "UUID", db)
}
