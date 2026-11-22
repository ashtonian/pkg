//go:build integration

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Timestamps on PostgreSQL 18: a TIMESTAMPTZ column, read on a session
// whose TimeZone and DateStyle are neither UTC nor ISO, which the facet
// keys must not depend on.
func TestTimestamps_PostgreSQL(t *testing.T) {
	db := dbtest.Postgres(t)
	db.SetMaxOpenConns(1) // one session, so its settings hold for every statement
	_, err := db.Exec("SET TIME ZONE 'Asia/Kolkata'")
	require.NoError(t, err)
	_, err = db.Exec("SET DateStyle = 'SQL, DMY'")
	require.NoError(t, err)
	runTimestamps(t, timestampDB{db: db, dialect: filter.PostgresDialect{}, atType: "TIMESTAMPTZ"})
}
