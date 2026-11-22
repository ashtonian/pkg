//go:build unit

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestRawPolicy_SQLite(t *testing.T) {
	runRawPolicy(t, filter.SQLiteDialect{}, func(t *testing.T) *sql.DB { return dbtest.SQLite(t) })
}
