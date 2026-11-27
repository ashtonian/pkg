//go:build unit

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestNullOperands_SQLite(t *testing.T) {
	runNullOperands(t, dbtest.SQLite(t), filter.SQLiteDialect{})
}
