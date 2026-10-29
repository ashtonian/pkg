//go:build unit

package patch_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Ordered operations on one column must compose, not overwrite each other
// : executed against SQLite, asserting stored values.
func TestBuildPatchSQL_FoldsOrderedOpsSQLite(t *testing.T) {
	runFoldCases(t, foldDB{
		db:       dbtest.SQLite(t),
		dialect:  filter.SQLiteDialect{},
		schema:   `CREATE TABLE items (id INTEGER PRIMARY KEY, tags TEXT, name TEXT NOT NULL, updated_at TEXT, meta TEXT, version INTEGER NOT NULL DEFAULT 1)`,
		seed:     `INSERT INTO items (id, tags, name) VALUES (1, '["zero"]', 'orig')`,
		tagsExpr: "tags",
	})
}

func TestBuildPatchSQL_MergeSQLite(t *testing.T) {
	runMergeCases(t, mergeDB{
		db:      dbtest.SQLite(t),
		dialect: filter.SQLiteDialect{},
		schema:  `CREATE TABLE docs (id INTEGER PRIMARY KEY, doc TEXT)`,
		insert:  `INSERT INTO docs (id, doc) VALUES (1, ?1)`,
		read:    `SELECT doc FROM docs WHERE id = 1`,
	})
}
