//go:build integration

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// `$ilike` case on each backend. SQLite lower-cases ASCII letters only. On
// PostgreSQL a pg_c_utf8 column lower-cases every letter, as the reference
// evaluator does, and a C column ASCII letters only.
func TestFoldMatrix(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		if b.engine == sqliteEngine {
			runFoldMatrix(t, b, false)
			return
		}
		t.Run("pg_c_utf8", func(t *testing.T) { runFoldMatrix(t, b.with(collation(filter.CollationPgCUTF8)), true) })
		t.Run("C", func(t *testing.T) { runFoldMatrix(t, b.with(collation(filter.CollationC)), false) })
	})
}

// runFoldMatrix loads filtertest.FoldRecords on b and runs the `$ilike`
// case cases on them; unicode adds those whose answer lower-cases a
// non-ASCII letter.
func runFoldMatrix(t *testing.T, b sqlBackend, unicode bool) {
	t.Helper()
	db := b.open(t)
	schema, err := filtertest.FoldSchema()
	require.NoError(t, err)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.FoldRecords()))
	filtertest.RunFold(t, unicode, b.filterIDs(db, schema))
}
