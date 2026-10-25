//go:build unit

package filter_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

// widgetWithRelation is a struct fixture for tenant-scoped junction tests.
// Tags is the canonical "virtual filter field" shape: db:"-" excludes the
// field from SELECT/INSERT, json:"-" hides from response bodies, and the
// 5-arg relation tag opts the junction into tenant scoping.
type widgetWithRelation struct {
	ID   string   `db:"id" json:"id" parse:"filter,sort" sort_default:"asc"`
	Tags []string `db:"-" json:"-" parse:"filter" filter_ops:"eq,in,nin" relation:"widget_tags,widget_id,tag_id,id,tenant_id"`
}

func TestParseRelationTag_TenantSlot(t *testing.T) {
	cases := []struct {
		name       string
		tag        string
		wantTenant string
		wantErr    bool
	}{
		{"3-arg", "widget_tags,widget_id,tag_id", "", false},
		{"4-arg", "widget_tags,widget_id,tag_id,id", "", false},
		{"5-arg tenant", "widget_tags,widget_id,tag_id,id,tenant_id", "tenant_id", false},
		{"5-arg opt-out", "widget_tags,widget_id,tag_id,id,-", "", false},
		{"6-arg rejected", "widget_tags,widget_id,tag_id,id,subquery,tenant_id", "", true},
		{"identifier injection", "widget_tags; DROP TABLE x,widget_id,tag_id", "", true},
		{"bad tenant identifier", "widget_tags,widget_id,tag_id,id,tenant id", "", true},
		{"empty parts", ",,", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel, err := core.ParseRelationTag(tc.tag)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error parsing %q", tc.tag)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rel.TenantColumn != tc.wantTenant {
				t.Errorf("TenantColumn = %q, want %q", rel.TenantColumn, tc.wantTenant)
			}
		})
	}
}

func TestSubqueryRelation_AppendsTenantPredicate(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[widgetWithRelation](g)
	if err != nil {
		t.Fatalf("generate filter schema: %v", err)
	}
	tagsField, ok := schema.Fields["tags"]
	if !ok {
		t.Fatalf("expected tags field; got %v", keys(schema.Fields))
	}
	if tagsField.Relation == nil {
		t.Fatalf("Relation expected, got nil")
	}
	if tagsField.Relation.TenantColumn != "tenant_id" {
		t.Errorf("TenantColumn = %q, want tenant_id", tagsField.Relation.TenantColumn)
	}

	ast := &filter.FilterAST{Root: &filter.FilterNode{
		Field: tagsField,
		Op:    filter.OpIn,
		Value: []any{"t1", "t2"},
	}}

	builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{}, filter.WithTenant("tenant-A"))
	res, err := builder.Build(ast)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(res.Where, "rel__.tenant_id =") {
		t.Errorf("expected tenant predicate inside subquery, got: %q", res.Where)
	}
	hasTenantArg := false
	for _, a := range res.Args {
		if s, ok := a.(string); ok && s == "tenant-A" {
			hasTenantArg = true
		}
	}
	if !hasTenantArg {
		t.Errorf("tenant value not bound, args=%v", res.Args)
	}
}

// A tenant-scoped junction with no tenant bound fails closed: matching an
// owner through another tenant's junction rows is exactly the leak
//
//	closes. Omitting the predicate takes an explicit opt-out.
func TestRelation_NoTenantBound_FailsClosed(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[widgetWithRelation](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	ast := &filter.FilterAST{Root: &filter.FilterNode{Field: schema.Fields["tags"], Op: filter.OpIn, Value: []any{"t1"}}}

	if _, buildErr := filter.NewSQLBuilder(schema, filter.PostgresDialect{}).Build(ast); buildErr == nil || !strings.Contains(buildErr.Error(), "tenant-scoped") {
		t.Fatalf("build without a tenant: buildErr = %v, want tenant-scoped error", buildErr)
	}
	res, err := filter.NewSQLBuilder(schema, filter.PostgresDialect{}, filter.WithUnscopedRelations()).Build(ast)
	if err != nil {
		t.Fatalf("unscoped build: %v", err)
	}
	if strings.Contains(res.Where, "tenant_id") {
		t.Errorf("unscoped build carried a tenant predicate: %q", res.Where)
	}
}

type widgetGlobalRel struct {
	ID   string   `db:"id" json:"id" parse:"filter"`
	Tags []string `db:"-" json:"-" parse:"filter" filter_ops:"eq,in" relation:"system_tags,id,tag_id,id,-"`
}

func TestSubqueryRelation_TenantOptOut(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := filter.GenerateFilterSchema[widgetGlobalRel](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	tagsField := schema.Fields["tags"]
	if tagsField.Relation == nil {
		t.Fatalf("relation nil")
	}
	if tagsField.Relation.TenantColumn != "" {
		t.Errorf("opt-out should leave TenantColumn empty, got %q", tagsField.Relation.TenantColumn)
	}

	ast := &filter.FilterAST{Root: &filter.FilterNode{Field: tagsField, Op: filter.OpIn, Value: []any{"a"}}}
	builder := filter.NewSQLBuilder(schema, filter.PostgresDialect{}, filter.WithTenant("ignored"))
	res, err := builder.Build(ast)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if strings.Contains(res.Where, "tenant_id") {
		t.Errorf("opt-out junction should never include tenant_id, got: %q", res.Where)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
