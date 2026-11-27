//go:build unit

package filter_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestPolicyStoredValues_SQLite(t *testing.T) {
	runPolicyStoredValues(t, dbtest.SQLite(t), filter.SQLiteDialect{})
}
