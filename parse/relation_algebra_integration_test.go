//go:build integration

package parse_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// The same relation algebra executed on PostgreSQL.
func TestRelationAlgebra_Postgres(t *testing.T) {
	runRelationAlgebra(t, relDB{
		db:      dbtest.Postgres(t),
		dialect: filter.PostgresDialect{},
		ddl: []string{
			`CREATE TABLE widgets (tenant_id TEXT NOT NULL, id TEXT NOT NULL, status TEXT NOT NULL, PRIMARY KEY (tenant_id, id))`,
			`CREATE TABLE widget_tags (tenant_id TEXT NOT NULL, widget_id TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY (tenant_id, widget_id, tag_id))`,
		},
	})
}
