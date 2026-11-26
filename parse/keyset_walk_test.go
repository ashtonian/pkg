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
				schema:  `CREATE TABLE track_items (id TEXT PRIMARY KEY, rank INTEGER, score INTEGER, name TEXT NOT NULL, created_at TEXT NOT NULL, seen_at DATETIME, price ` + priceType + `)`,
				insert: func(db *sql.DB, s trackSeed) error {
					var seen any
					if at := trackSeen(s); at.Valid {
						seen = core.FormatTimestamp(at.Time)
					}
					_, err := db.Exec(`INSERT INTO track_items (id, rank, score, name, created_at, seen_at, price) VALUES (?, ?, ?, ?, ?, ?, ?)`,
						trackID(s.id).String(), s.rank, s.rank, s.name, core.FormatTimestamp(trackTime(s)), seen, s.price)
					return err
				},
			})
		})
	}
}
