//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Numeric facet keys on SQLite: REAL columns, a JSON text document and a
// JSON text list.
func TestNumberKeys_SQLite(t *testing.T) {
	runNumberKeys(t, numberKeyDB{
		db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{},
		docType: "TEXT", f64Type: "REAL", f32Type: "REAL", intType: "INTEGER", listType: "TEXT",
		list: func(l []float32) any {
			if l == nil {
				return nil
			}
			return jsonText(l)
		},
	})
}
