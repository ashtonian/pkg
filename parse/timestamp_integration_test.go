//go:build integration

package parse_test

import (
	"database/sql"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
)

// Timestamps on each backend. SQLite's column holds each row's canonical
// text, declared TEXT or DATETIME; modernc.org/sqlite scans the DATETIME
// column's text into a time.Time of the same instant. PostgreSQL's
// TIMESTAMPTZ column is read on sessions whose TimeZone and DateStyle are
// neither UTC nor ISO, which the facet keys must not depend on.
func TestTimestamps(t *testing.T) {
	runSQLBackends(t, func(t *testing.T, b sqlBackend) {
		if b.engine == postgresEngine {
			provider, db := b.open(t)
			var zone, style string
			require.NoError(t, db.QueryRowContext(t.Context(), "SELECT current_setting('TimeZone'), current_setting('DateStyle')").Scan(&zone, &style))
			require.Equal(t, "Asia/Kolkata", zone)
			require.Equal(t, "SQL, DMY", style)
			runTimestamps(t, b, provider, db)
			return
		}
		for _, atType := range []string{"TEXT", "DATETIME"} {
			t.Run(atType, func(t *testing.T) {
				b := b.with(storing(core.FieldTypeTimestamp, atType))
				provider, db := b.open(t)
				runTimestamps(t, b, provider, db)
				if atType != "DATETIME" {
					return
				}
				schema, err := filtertest.TimestampSchema()
				require.NoError(t, err)
				var at time.Time
				require.NoError(t, db.QueryRowContext(t.Context(), "SELECT at FROM "+schema.Name+" WHERE id = 'a'").Scan(&at))
				assert.True(t, filtertest.TimestampRecords()[0].At.Equal(at), "scanned %v", at)
			})
		}
	}, sessionParams(map[string]string{"TimeZone": "Asia/Kolkata", "DateStyle": "SQL, DMY"}))
}

// runTimestamps writes TimestampRecords through the dialect's MarshalValue,
// each row in its own offset, then checks that facets, filters, sorts and
// keyset walks on the column read instants: the buckets, rows and orders
// the fixture names, the same on every backend. provider is the one on db.
func runTimestamps(t *testing.T, b sqlBackend, provider *parse.Provider, db *sql.DB) {
	t.Helper()
	schema, err := filtertest.TimestampSchema()
	require.NoError(t, err)
	table := schemaTable(schema)
	b.load(t, db, table, recordRows(t, table, filtertest.TimestampRecords()))
	res, err := parse.NewResource[filtertest.TimestampRecord](provider, schema.Name, parse.PageSize(walkPage, walkPage))
	require.NoError(t, err)

	t.Run("facet", func(t *testing.T) {
		got := facetCounts(t, db, b.dialect, schema, "at")
		assert.Equal(t, map[string]map[string]int64{"at": filtertest.TimestampFacets}, got)
	})

	for _, doc := range slices.Sorted(maps.Keys(filtertest.TimestampFilters)) {
		t.Run("filter "+doc, func(t *testing.T) {
			f, err := res.ParseFilter(doc)
			require.NoError(t, err)
			where, err := filter.NewSQLBuilder(schema, b.dialect).Build(f.AST())
			require.NoError(t, err)
			ids := queryRows[string](t, db, "SELECT id FROM "+schema.Name+" WHERE "+where.Where+" ORDER BY id", where.Args...)
			assert.Equal(t, filtertest.TimestampFilters[doc], strings.Join(ids, ""))
		})
	}

	walkOrders(t, walker[filtertest.TimestampRecord]{resource: res,
		id: func(r filtertest.TimestampRecord) string { return r.ID }}, filtertest.TimestampOrders)
}
