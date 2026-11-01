//go:build integration

package patch_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// The change cases against PostgreSQL, where equality is coarser than a
// read for numeric scale, intervals, a float's zero sign, citext and a
// nondeterministic collation, and a json column has no equality operator.
func TestBuildPatchSQL_ChangePredicatePostgres(t *testing.T) {
	runChangeCases(t, changeDB{
		db:      dbtest.Postgres(t),
		dialect: filter.PostgresDialect{},
		setup: []string{
			// An extension is one per database: in public it outlives the test
			// schema's cleanup, as the PostGIS helper's does.
			`CREATE EXTENSION IF NOT EXISTS citext SCHEMA public`,
			`CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')`,
			`CREATE COLLATION ci (provider = icu, locale = 'und-u-ks-level2', deterministic = false)`,
			`CREATE TABLE things (id BIGINT PRIMARY KEY, name TEXT COLLATE ci, email citext, mood mood,
				rank BIGINT, price NUMERIC(12,2), amount NUMERIC, ratio DOUBLE PRECISION, span INTERVAL, at TIMESTAMPTZ,
				doc JSONB, raw JSON, tags TEXT[] NOT NULL DEFAULT '{}', version BIGINT NOT NULL DEFAULT 1, updated_at TIMESTAMPTZ)`,
		},
		// A decimal's scale, and a json column's whitespace and key order,
		// are not part of the value.
		readBack: `SELECT concat_ws('|', quote_nullable(name::text), quote_nullable(email::text), quote_nullable(mood::text),
			quote_nullable(rank::text), quote_nullable(trim_scale(price)::text), quote_nullable(trim_scale(amount)::text), quote_nullable(ratio::text),
			quote_nullable(span::text), quote_nullable(at::text), quote_nullable(doc::text), quote_nullable(raw::jsonb::text),
			quote_nullable(tags::text)) FROM things WHERE id = 1`,
	})
}
