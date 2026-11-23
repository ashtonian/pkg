//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// `$ilike` case on PostgreSQL: a pg_c_utf8 column lower-cases every letter,
// as the reference evaluator does; a C column lower-cases ASCII only.
func TestFoldMatrix_PostgreSQL(t *testing.T) {
	t.Run("pg_c_utf8", func(t *testing.T) {
		runFoldMatrix(t, dbtest.Postgres(t), filter.PostgresDialect{Collation: filter.CollationPgCUTF8}, "TEXT COLLATE pg_c_utf8", true)
	})
	t.Run("C", func(t *testing.T) {
		runFoldMatrix(t, dbtest.Postgres(t), filter.PostgresDialect{Collation: filter.CollationC}, `TEXT COLLATE "C"`, false)
	})
}
