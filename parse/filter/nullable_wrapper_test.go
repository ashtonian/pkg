//go:build unit

package filter_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// SQLite stores a timestamp as its canonical text.
func TestNullableWrappers_SQLite(t *testing.T) {
	runNullableWrappers(t, visitDB{db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{}, timestampType: "DATETIME", stamp: func(at sql.NullTime) any {
		if !at.Valid {
			return nil
		}
		return core.FormatTimestamp(at.Time)
	}})
}
