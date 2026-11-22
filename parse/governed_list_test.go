//go:build unit

package parse_test

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/dbtest"
)

func governedSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db := dbtest.SQLite(t)
	for _, stmt := range governedFixtureSQL("TEXT") {
		_, err := db.Exec(stmt)
		require.NoError(t, err, stmt)
	}
	return db
}

func TestGovernedLists_SQLite(t *testing.T) {
	db := governedSQLite(t)
	runGovernedLists(t, filter.SQLiteDialect{}, db)
}

func TestGovernedLists_Memory(t *testing.T) {
	db := governedSQLite(t)
	r := governedDocuments(t)
	runGovernedMemory(t, func(t *testing.T, filterJSON string) governedResult {
		return listGoverned(t, r, filter.SQLiteDialect{}, db, filterJSON, parse.Scope{Governance: governanceA()})
	})
}

func TestGovernedMixedTypes_SQLite(t *testing.T) {
	db := dbtest.SQLite(t)
	runGovernedMixedTypes(t, filter.SQLiteDialect{}, "TEXT", db)
}

func TestBuildList_GovernedRelationsFollowTheRow(t *testing.T) {
	r := governedDocuments(t)
	build := func(t *testing.T, scope parse.Scope) (*parse.ListPlan, error) {
		t.Helper()
		req, err := r.ParseListInput(parse.ListInput{
			Filter: `{"notes":{"$some":{"reactions":{"$some":{"kind":"up"}}}},"labels":"lb","topics":"z"}`,
			Facets: []string{"notes", "labels"}, Include: []string{"notes", "labels"},
		})
		require.NoError(t, err)
		return r.BuildList(filter.PostgresDialect{}, req, scope)
	}

	t.Run("every relation tenant column is compared with the row's", func(t *testing.T) {
		plan, err := build(t, parse.Scope{Governance: governanceA()})
		require.NoError(t, err, "a governed list needs no Scope.Tenant")
		for _, want := range []string{
			`"notes"."tenant_id" = "documents"."tenant_id"`,           // depth one
			`"reactions"."tenant_id" = "documents"."tenant_id"`,       // depth two
			`"rel1__"."tenant_id" = "documents"."tenant_id"`,          // the pair table
			`"labels"."tenant_id" = "documents"."tenant_id"`,          // the pair's target
			`"document_topics"."tenant_id" = "documents"."tenant_id"`, // the pair table compared by key
		} {
			assert.Contains(t, plan.Page.SQL, want)
		}
		assert.Contains(t, plan.Facets.SQL, `"documents"."tenant_id" AS __owner_tenant`)
		assert.Contains(t, plan.Facets.SQL, `"j"."tenant_id" = base.__owner_tenant`)
		bound := 0
		for _, arg := range plan.Page.Args {
			if arg == "A" {
				bound++
			}
		}
		assert.Equal(t, 1, bound, "A is bound once, by the governance tree: relations bind no tenant")

		queries, err := r.IncludeQueries(plan, []any{"a1", "b1"})
		require.NoError(t, err)
		require.Len(t, queries, 2)
		for _, q := range queries {
			assert.Contains(t, q.SQL, `JOIN "documents" owner__ ON "owner__"."id" = "j"."document_id"`, q.Field)
			assert.Contains(t, q.SQL, `"j"."tenant_id" = "owner__"."tenant_id"`, q.Field)
		}
	})

	t.Run("a scope's tenant is not used under governance", func(t *testing.T) {
		governed, err := build(t, parse.Scope{Governance: governanceA()})
		require.NoError(t, err)
		withTenant, err := build(t, parse.Scope{Governance: governanceA(), Tenant: "not-checked"})
		require.NoError(t, err)
		assert.Equal(t, governed.Page, withTenant.Page)
	})

	t.Run("empty governance is the tenant floor", func(t *testing.T) {
		_, err := build(t, parse.Scope{Governance: &filter.PolicyConstraints{}})
		require.ErrorIs(t, err, parse.ErrTenantRequired)
		plan, err := build(t, parse.Scope{Governance: &filter.PolicyConstraints{}, Tenant: "A"})
		require.NoError(t, err)
		floor, err := build(t, parse.Scope{Tenant: "A"})
		require.NoError(t, err)
		assert.Equal(t, floor.Page, plan.Page)
		assert.NotContains(t, plan.Page.SQL, `= "documents"."tenant_id"`)
	})

	t.Run("a resource without a tenant column holds relations to Scope.Tenant", func(t *testing.T) {
		labels := parse.MustResource[govLabel]("labels", parse.TenantColumn("tenant_id"))
		boards := parse.MustResource[untenantedDevice]("boards")
		boards.MustRelate("labels", parse.ManyToMany(labels,
			parse.Junction{Table: "board_labels", Source: "device_id", Target: "label_id", Tenant: "tenant_id"}, parse.Indexed()))
		req, err := boards.ParseListInput(parse.ListInput{Filter: `{"labels":"la"}`})
		require.NoError(t, err)
		plan, err := boards.BuildList(filter.PostgresDialect{}, req, parse.Scope{Governance: govEq("id", int64(1)), Tenant: "A"})
		require.NoError(t, err)
		assert.Contains(t, plan.Page.Args, "A")
		_, err = boards.BuildList(filter.PostgresDialect{}, req, parse.Scope{Governance: govEq("id", int64(1))})
		assert.ErrorContains(t, err, "tenant-scoped", "its relations still need a tenant")
	})
}
