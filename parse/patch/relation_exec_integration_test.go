//go:build integration

package patch_test

import (
	"testing"

	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

// The pair table's target and tenant are uuid columns its Go keys give as
// strings: every statement types its values by the columns.
func TestRenderRelationOp_ExecutesPostgres(t *testing.T) {
	runRelationCases(t, relationDB{
		db:      dbtest.Postgres(t),
		dialect: filter.PostgresDialect{},
		schema: []string{
			`CREATE TABLE widgets (id BIGINT PRIMARY KEY, tenant_id UUID NOT NULL)`,
			`CREATE TABLE tags (id UUID PRIMARY KEY, tenant_id UUID NOT NULL, deleted_at TIMESTAMPTZ)`,
			`CREATE TABLE widget_tags (widget_id BIGINT NOT NULL, tag_id UUID NOT NULL, tenant_id UUID, PRIMARY KEY (widget_id, tag_id))`,
		},
	})
}
