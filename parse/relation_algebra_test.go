//go:build unit

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// Relation predicates keep set semantics inside AND / OR / NOT, stay inside
// the tenant, and never multiply owners in counts or facets — executed on
// SQLite.
func TestRelationAlgebra_SQLite(t *testing.T) {
	runRelationAlgebra(t, relDB{
		db:      dbtest.SQLite(t),
		dialect: filter.SQLiteDialect{},
		ddl: []string{
			`CREATE TABLE widgets (tenant_id TEXT NOT NULL, id TEXT NOT NULL, status TEXT NOT NULL, PRIMARY KEY (tenant_id, id))`,
			`CREATE TABLE widget_tags (tenant_id TEXT NOT NULL, widget_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (tenant_id, widget_id, tag_id))`,
		},
	})
}
