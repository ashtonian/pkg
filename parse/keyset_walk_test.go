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
// directions and timestamps included.
func TestKeysetWalk_SQLite(t *testing.T) {
	runTrackWalks(t, trackDB{
		db:      dbtest.SQLite(t),
		dialect: filter.SQLiteDialect{},
		schema:  `CREATE TABLE track_items (id TEXT PRIMARY KEY, rank INTEGER, name TEXT NOT NULL, created_at TEXT NOT NULL)`,
		insert: func(db *sql.DB, s trackSeed) error {
			_, err := db.Exec(`INSERT INTO track_items (id, rank, name, created_at) VALUES (?, ?, ?, ?)`,
				trackID(s.id).String(), s.rank, s.name, core.FormatTimestamp(trackTime(s)))
			return err
		},
		scanTime: scanTimeValue,
	})
}
