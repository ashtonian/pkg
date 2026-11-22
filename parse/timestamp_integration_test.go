//go:build integration

package parse_test

import (
	"database/sql"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Timestamps on PostgreSQL 18: a TIMESTAMPTZ column, read on sessions
// whose TimeZone and DateStyle are neither UTC nor ISO, which the facet
// keys must not depend on. The settings are startup parameters, so every
// connection the pool opens has them.
func TestTimestamps_PostgreSQL(t *testing.T) {
	u, err := url.Parse(dbtest.PostgresDSN(t))
	require.NoError(t, err)
	q := u.Query()
	q.Set("TimeZone", "Asia/Kolkata")
	// No space: pgx reads a DSN query's + literally, not as a space.
	q.Set("DateStyle", "SQL,DMY")
	u.RawQuery = q.Encode()
	db, err := sql.Open("pgx", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var zone, style string
	require.NoError(t, db.QueryRow("SELECT current_setting('TimeZone'), current_setting('DateStyle')").Scan(&zone, &style))
	require.Equal(t, "Asia/Kolkata", zone)
	require.Equal(t, "SQL, DMY", style)
	runTimestamps(t, timestampDB{db: db, dialect: filter.PostgresDialect{}, atType: "TIMESTAMPTZ"})
}
