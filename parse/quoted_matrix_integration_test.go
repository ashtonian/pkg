//go:build integration

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Quoted integers on each backend: decimal-text operands over INTEGER
// storage on SQLite and bigint on PostgreSQL.
func TestQuotedMatrix(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := b.open(t)
		schema, err := filtertest.QuotedSchema()
		require.NoError(t, err)
		table := schemaTable(schema)
		b.load(t, db, table, recordRows(t, table, filtertest.QuotedRecords()))
		filtertest.RunQuoted(t, b.filterIDs(db, schema))
	})
}
