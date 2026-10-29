//go:build integration

package patch_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// The same fold cases against PostgreSQL, which additionally rejects any
// repeated column assignment.
func TestBuildPatchSQL_FoldsOrderedOpsPostgres(t *testing.T) {
	runFoldCases(t, foldDB{
		db:       dbtest.Postgres(t),
		dialect:  filter.PostgresDialect{},
		schema:   `CREATE TABLE items (id BIGINT PRIMARY KEY, tags TEXT[], name TEXT NOT NULL, updated_at TIMESTAMPTZ, meta JSONB, version BIGINT NOT NULL DEFAULT 1)`,
		seed:     `INSERT INTO items (id, tags, name) VALUES (1, ARRAY['zero'], 'orig')`,
		tagsExpr: "array_to_json(tags)::text",
	})
}

func TestBuildPatchSQL_MergePostgres(t *testing.T) {
	runMergeCases(t, mergeDB{
		db:      dbtest.Postgres(t),
		dialect: filter.PostgresDialect{},
		schema:  `CREATE TABLE docs (id BIGINT PRIMARY KEY, doc JSONB)`,
		insert:  `INSERT INTO docs (id, doc) VALUES (1, $1::jsonb)`,
		read:    `SELECT doc::text FROM docs WHERE id = 1`,
	})
}
