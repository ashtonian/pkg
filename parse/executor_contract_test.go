//go:build unit

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestExecutorContract_SQLite(t *testing.T) {
	runExecutorContract(t, func(t *testing.T) *sql.DB { return dbtest.SQLite(t) }, filter.SQLiteDialect{}, "TEXT")
}
