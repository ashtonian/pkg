//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Array members and columns on SQLite: the document and both arrays are
// JSON text.
func TestArrayMatrix_SQLite(t *testing.T) {
	runArrayMatrix(t, arrayDB{
		db: dbtest.SQLite(t), dialect: filter.SQLiteDialect{},
		docType: "TEXT", labelsType: "TEXT", scoresType: "TEXT",
		values: func(r filtertest.ArrayRecord) (any, any) {
			var scores any
			if r.Scores != nil {
				scores = jsonText(r.Scores)
			}
			return jsonText(r.Labels), scores
		},
	})
}
