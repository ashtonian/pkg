//go:build integration

package parse_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// A UUID key's range selects the same rows on PostgreSQL, whose uuid order
// is its bytes', as in filter.Evaluate and on SQLite.
func TestUUIDKeyRanges_Postgres(t *testing.T) {
	runUUIDKeyRanges(t, dbtest.Postgres(t), filter.PostgresDialect{},
		`CREATE TABLE uuid_keyed (id UUID PRIMARY KEY, name TEXT NOT NULL)`,
		func(id uuid.UUID) any { return id })
}
