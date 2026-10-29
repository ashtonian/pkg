//go:build integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// The same walks against PostgreSQL: native uuid, timestamptz and numeric
// columns, whose default NULL placement differs from SQLite's.
func TestKeysetWalk_Postgres(t *testing.T) {
	runTrackWalks(t, trackDB{
		db:      dbtest.Postgres(t),
		dialect: filter.PostgresDialect{},
		schema:  `CREATE TABLE track_items (id UUID PRIMARY KEY, rank BIGINT, name TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL, price NUMERIC)`,
		insert: func(db *sql.DB, s trackSeed) error {
			_, err := db.Exec(`INSERT INTO track_items (id, rank, name, created_at, price) VALUES ($1, $2, $3, $4, $5)`,
				trackID(s.id), s.rank, s.name, trackTime(s), s.price)
			return err
		},
		scanTime: scanTimeValue,
	})
}
