//go:build unit

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Every generated sort walks forwards and backwards over exactly the rows,
// in the expected order, on SQLite — UUID text keys, NULL keys, mixed
// directions, timestamps and decimals included. Decimals walk over both
// numeric affinities a decimal column may have: NUMERIC (INTEGER or REAL per
// value) and REAL.
func TestKeysetWalk_SQLite(t *testing.T) {
	for _, priceType := range []string{"NUMERIC", "REAL"} {
		t.Run("price "+priceType, func(t *testing.T) {
			runTrackWalks(t, trackDB{
				db:      dbtest.SQLite(t),
				dialect: filter.SQLiteDialect{},
				schema:  `CREATE TABLE track_items (id TEXT PRIMARY KEY, rank INTEGER, name TEXT NOT NULL, created_at TEXT NOT NULL, price ` + priceType + `)`,
				insert: func(db *sql.DB, s trackSeed) error {
					_, err := db.Exec(`INSERT INTO track_items (id, rank, name, created_at, price) VALUES (?, ?, ?, ?, ?)`,
						trackID(s.id).String(), s.rank, s.name, core.FormatTimestamp(trackTime(s)), s.price)
					return err
				},
				scanTime: scanTimeValue,
			})
		})
	}
}
