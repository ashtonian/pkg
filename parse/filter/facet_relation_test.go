//go:build unit

package filter_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// facetSchemaWithRelation extends facetSchema with a M2M relation field.
// The relation is tenant-scoped so we can verify the predicate emission.
func facetSchemaWithRelation() *filter.ResourceSchema {
	rs := facetSchema()
	rs.Fields["tags"] = &filter.FieldSchema{
		ExternalName:     "tags",
		SQLExpr:          "",
		DataType:         core.FieldTypeString,
		AllowedOperators: []filter.FilterOperator{filter.OpEq, filter.OpIn},
		Facetable:        true,
		FacetSchema:      &filter.FacetSchema{Type: filter.FacetTerms, Limit: 5},
		Relation: &core.RelationSchema{
			JoinTable:    "item_tags",
			SourceColumn: "item_id",
			TargetColumn: "tag_id",
			PrimaryKey:   "id",
			TenantColumn: "tenant_id",
		},
	}
	return rs
}

func TestBuildFacetQuery_RelationFacet_PostgresJoin(t *testing.T) {
	q, err := filter.BuildFacetQuery(
		facetSchemaWithRelation(),
		filter.PostgresDialect{},
		"items.tenant_id = $1",
		[]any{"t1"},
		[]filter.FacetRequest{
			{Field: "tags"},
		},
		filter.WithFacetTenant("t1"),
	)
	if err != nil {
		t.Fatalf("BuildFacetQuery: %v", err)
	}
	wants := []string{
		"WITH base AS",
		"items.id AS __owner_pk",
		"INNER JOIN item_tags j ON j.item_id = base.__owner_pk",
		"AND j.tenant_id = ",
		"GROUP BY j.tag_id",
		"ORDER BY c DESC, j.tag_id::text COLLATE pg_c_utf8 LIMIT 5",
	}
	for _, sub := range wants {
		if !strings.Contains(q.SQL, sub) {
			t.Errorf("missing %q\nSQL:\n%s", sub, q.SQL)
		}
	}
	// 1 outer-filter arg + 1 tenant arg = 2 args.
	if len(q.Args) != 2 {
		t.Errorf("want 2 args, got %d: %v", len(q.Args), q.Args)
	}
}

// A tenant-scoped relation facet with no tenant bound fails closed; the
// predicate is omitted only on explicit opt-out.
func TestBuildFacetQuery_RelationFacet_NoTenantBound_FailsClosed(t *testing.T) {
	build := func(opts ...filter.BuildFacetQueryOption) (filter.FacetQuery, error) {
		return filter.BuildFacetQuery(
			facetSchemaWithRelation(),
			filter.PostgresDialect{},
			"items.tenant_id = $1",
			[]any{"t1"},
			[]filter.FacetRequest{{Field: "tags"}},
			opts...,
		)
	}
	if _, err := build(); err == nil || !strings.Contains(err.Error(), "tenant-scoped") {
		t.Fatalf("err = %v, want tenant-scoped error", err)
	}
	q, err := build(filter.WithFacetUnscopedRelations())
	if err != nil {
		t.Fatalf("unscoped: %v", err)
	}
	if strings.Contains(q.SQL, "j.tenant_id") {
		t.Errorf("unscoped facet carried a tenant predicate: %s", q.SQL)
	}
}

func TestBuildFacetQuery_RelationFacet_MixedWithScalar(t *testing.T) {
	q, err := filter.BuildFacetQuery(
		facetSchemaWithRelation(),
		filter.PostgresDialect{},
		"1=1",
		nil,
		[]filter.FacetRequest{
			{Field: "status"},
			{Field: "tags"},
		},
		filter.WithFacetTenant("tenant-A"),
	)
	if err != nil {
		t.Fatalf("BuildFacetQuery: %v", err)
	}
	// Base CTE projects both the scalar facet column AND the owner PK.
	if !strings.Contains(q.SQL, "items.status AS facet__0") {
		t.Errorf("scalar projection missing: %s", q.SQL)
	}
	if !strings.Contains(q.SQL, "items.id AS __owner_pk") {
		t.Errorf("owner PK projection missing: %s", q.SQL)
	}
	// Each facet has its own UNION ALL leg.
	if !strings.Contains(q.SQL, "SELECT 'status' AS facet_field") {
		t.Errorf("status leg missing: %s", q.SQL)
	}
	if !strings.Contains(q.SQL, "SELECT 'tags' AS facet_field") {
		t.Errorf("tags leg missing: %s", q.SQL)
	}
}

func TestBuildFacetQuery_RelationFacet_RejectsRange(t *testing.T) {
	rs := facetSchemaWithRelation()
	rs.Fields["tags"].FacetSchema = &filter.FacetSchema{Type: filter.FacetTypeRange}
	_, err := filter.BuildFacetQuery(
		rs,
		filter.PostgresDialect{},
		"1=1", nil,
		[]filter.FacetRequest{{Field: "tags"}},
	)
	if err == nil {
		t.Fatal("expected error rejecting range facet on relation field")
	}
	if !strings.Contains(err.Error(), "relation") {
		t.Errorf("error should mention relation, got: %v", err)
	}
}

func TestBuildFacetQuery_RelationFacet_SQLite(t *testing.T) {
	q, err := filter.BuildFacetQuery(
		facetSchemaWithRelation(),
		filter.SQLiteDialect{},
		"1=1", nil,
		[]filter.FacetRequest{{Field: "tags"}},
		filter.WithFacetTenant("tenant-A"),
	)
	if err != nil {
		t.Fatalf("BuildFacetQuery: %v", err)
	}
	// SQLite uses CAST for type coercion rather than ::text.
	if !strings.Contains(q.SQL, "CAST(j.tag_id AS TEXT)") {
		t.Errorf("expected CAST(...AS TEXT) on SQLite, got: %s", q.SQL)
	}
}

// TestCountStarWithoutJoin confirms the common no-relation case keeps the
// cheaper COUNT(*) — the distinct path is scoped to actual row multiplication.
func TestCountStarWithoutJoin(t *testing.T) {
	lq := filter.NewListQuery(facetSchemaWithRelation(), nil, filter.PostgresDialect{})
	result, err := lq.Build(
		filter.ListFilter(`{"status":"active"}`),
		filter.ListWithCount(true),
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(result.CountSQL, "SELECT COUNT(*)") {
		t.Errorf("expected plain COUNT(*) for a non-relation filter, got %q", result.CountSQL)
	}
}
