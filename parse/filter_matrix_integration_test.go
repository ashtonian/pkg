//go:build integration

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/filter/filtertest"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Every operator × type case executed on PostgreSQL 18 + PostGIS with the
// storage model's native types: jsonb documents, TEXT[] arrays, timestamptz,
// uuid, numeric.
func TestFilterMatrix_PostgreSQL(t *testing.T) {
	runFilterMatrix(t, "postgres", postgresMatrix(dbtest.PostGIS(t)))
}

// Membership constraints on PostgreSQL 18, as subqueries and resolved, over
// native UUID and BIGINT lookup columns.
func TestMembershipMatrix_PostgreSQL(t *testing.T) {
	runMembershipMatrix(t, postgresMatrix(dbtest.Postgres(t)), map[core.FieldType]string{
		core.FieldTypeString: "TEXT", core.FieldTypeUUID: "UUID", core.FieldTypeInt: "BIGINT",
	})
}

func postgresMatrix(db *sql.DB) matrixDB {
	return matrixDB{
		db:      db,
		dialect: filter.PostgresDialect{},
		schema: `CREATE TABLE records (id TEXT PRIMARY KEY, name TEXT, count BIGINT, score DOUBLE PRECISION, amount NUMERIC,
			active BOOLEAN, at TIMESTAMPTZ, ref UUID, doc JSONB, tags TEXT[], loc JSONB)`,
		values: func(r filtertest.Record) []any {
			var tags any
			if r.Tags != nil {
				tags = r.Tags
			}
			return []any{r.ID, r.Name, r.Count, r.Score, r.Amount, r.Active, r.At, uuidText(r), jsonText(r.Doc), tags, jsonText(r.Loc)}
		},
		casts: map[string]string{"doc": "%s::jsonb", "loc": "%s::jsonb"},
	}
}
