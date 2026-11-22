//go:build unit

package patch_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func TestRenderRelationOp_ExecutesSQLite(t *testing.T) {
	runRelationCases(t, relationDB{
		db:      dbtest.SQLite(t),
		dialect: filter.SQLiteDialect{},
		schema: []string{
			`CREATE TABLE widgets (id INTEGER PRIMARY KEY, tenant_id TEXT NOT NULL)`,
			`CREATE TABLE tags (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, deleted_at TEXT)`,
			`CREATE TABLE widget_tags (widget_id INTEGER NOT NULL, tag_id TEXT NOT NULL, tenant_id TEXT, PRIMARY KEY (widget_id, tag_id))`,
		},
	})
}
