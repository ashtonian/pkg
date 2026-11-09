//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Numeric facet keys on PostgreSQL 18: a jsonb document, double precision,
// real, bigint and real[] columns.
func TestNumberKeys_PostgreSQL(t *testing.T) {
	runNumberKeys(t, numberKeyDB{
		db: dbtest.Postgres(t), dialect: filter.PostgresDialect{},
		docType: "JSONB", f64Type: "DOUBLE PRECISION", f32Type: "REAL", intType: "BIGINT", listType: "REAL[]", docCast: "%s::jsonb",
		list: func(l []float32) any {
			if l == nil {
				return nil
			}
			return l
		},
	})
}
