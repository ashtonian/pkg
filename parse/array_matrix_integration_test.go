//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Array members and columns on PostgreSQL 18: a jsonb document, a text[]
// and a bigint[] column.
func TestArrayMatrix_PostgreSQL(t *testing.T) {
	runArrayMatrix(t, arrayDB{
		db: dbtest.Postgres(t), dialect: filter.PostgresDialect{},
		docType: "JSONB", labelsType: "TEXT[]", scoresType: "BIGINT[]", docCast: "%s::jsonb",
		values: func(r filtertest.ArrayRecord) (any, any) {
			return r.Labels, r.Scores
		},
	})
}
