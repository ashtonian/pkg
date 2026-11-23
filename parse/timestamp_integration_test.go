//go:build integration

package parse_test

import (
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Timestamps on PostgreSQL 18: a TIMESTAMPTZ column, read on sessions
// whose TimeZone and DateStyle are neither UTC nor ISO, which the facet
// keys must not depend on. The settings are startup parameters, so every
// connection the pool opens has them.
func TestTimestamps_PostgreSQL(t *testing.T) {
	config := dbtest.PostgresConfig(t)
	config.RuntimeParams["TimeZone"] = "Asia/Kolkata"
	config.RuntimeParams["DateStyle"] = "SQL, DMY"
	db := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = db.Close() })

	var zone, style string
	require.NoError(t, db.QueryRow("SELECT current_setting('TimeZone'), current_setting('DateStyle')").Scan(&zone, &style))
	require.Equal(t, "Asia/Kolkata", zone)
	require.Equal(t, "SQL, DMY", style)
	runTimestamps(t, timestampDB{db: db, dialect: filter.PostgresDialect{}, atType: "TIMESTAMPTZ"})
}
