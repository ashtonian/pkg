//go:build integration

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Stored points on each backend: JSON text read through parsesqlite's
// functions on SQLite, jsonb read as PostgreSQL casts its members on
// PostGIS.
func TestGeoMatrix(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		db := b.open(t)
		schema, err := filtertest.GeoSchema()
		require.NoError(t, err)
		table := schemaTable(schema)
		b.load(t, db, table, recordRows(t, table, filtertest.GeoRecords()))
		filtertest.RunGeo(t, b.filterIDs(db, schema))
	}, onPostGIS())
}
