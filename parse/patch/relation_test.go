//go:build unit

package patch_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// Smoke test: a struct carrying both `parse:"patch"` and `relation:"…"`
// is recognized by the schema generator and the path comes back with a
// non-nil Relation, restricted to the relation-verb subset.
type widgetPatch struct {
	ID   string   `db:"id" json:"id" parse:"patch"`
	Tags []string `db:"-" json:"-" parse:"patch" relation:"widget_tags,widget_id,tag_id"`
}

func TestGeneratePatchSchema_RelationField(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[widgetPatch](g)
	if err != nil {
		t.Fatalf("generate patch schema: %v", err)
	}
	tagsPath, ok := schema.AllowedPaths["/tags"]
	if !ok {
		t.Fatalf("expected /tags path in schema, got %v", keys(schema.AllowedPaths))
	}
	if tagsPath.Relation == nil {
		t.Fatalf("expected /tags Relation to be set")
	}
	if tagsPath.Relation.JoinTable != "widget_tags" {
		t.Fatalf("junction = %q, want widget_tags", tagsPath.Relation.JoinTable)
	}
	if tagsPath.SQLColumn != "" {
		t.Fatalf("relation paths should have no SQLColumn, got %q", tagsPath.SQLColumn)
	}
	wantOps := map[patch.PatchOp]bool{patch.PatchOpReplace: true, patch.PatchOpAdd: true, patch.PatchOpRemove: true}
	if len(tagsPath.AllowedOps) != len(wantOps) {
		t.Fatalf("relation allowed ops = %v, want %v", tagsPath.AllowedOps, wantOps)
	}
	for _, op := range tagsPath.AllowedOps {
		if !wantOps[op] {
			t.Errorf("unexpected op %q on relation path", op)
		}
	}
}

func TestBuildPatchSQL_SplitsRelationsFromSetClause(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[widgetPatch](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	parser := patch.NewParser(schema, nil)
	parsed, err := parser.ParseOps([]patch.RawPatchOp{
		{Op: "replace", Path: "/id", Value: "w1"},
		{Op: "replace", Path: "/tags", Value: []any{"t1", "t2"}},
		{Op: "add", Path: "/tags", Value: []any{"t3"}},
	})
	if err != nil {
		t.Fatalf("parse ops: %v", err)
	}
	result, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatalf("build sql: %v", err)
	}
	if !strings.Contains(result.SetClause, "id =") {
		t.Errorf("expected scalar set clause to include id, got %q", result.SetClause)
	}
	if strings.Contains(result.SetClause, "tags") {
		t.Errorf("set clause should not reference relation field, got %q", result.SetClause)
	}
	if len(result.RelationOps) != 2 {
		t.Fatalf("expected 2 relation ops, got %d", len(result.RelationOps))
	}
	if result.RelationOps[0].Verb != patch.RelationVerbReplace {
		t.Errorf("op[0].Verb = %q, want replace", result.RelationOps[0].Verb)
	}
	if result.RelationOps[1].Verb != patch.RelationVerbAdd {
		t.Errorf("op[1].Verb = %q, want add", result.RelationOps[1].Verb)
	}
}

func TestBuildPatchSQL_RelationOnly_EmptySetClause(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[widgetPatch](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	parser := patch.NewParser(schema, nil)
	parsed, err := parser.ParseOps([]patch.RawPatchOp{
		{Op: "remove", Path: "/tags", Value: []any{"t1"}},
	})
	if err != nil {
		t.Fatalf("parse ops: %v", err)
	}
	result, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatalf("build sql: %v", err)
	}
	if result.SetClause != "" {
		t.Errorf("expected empty set clause for relation-only patch, got %q", result.SetClause)
	}
	if result.Empty {
		t.Errorf("Empty should be false when RelationOps has entries")
	}
	if len(result.RelationOps) != 1 || result.RelationOps[0].Verb != patch.RelationVerbRemove {
		t.Errorf("expected single remove op, got %+v", result.RelationOps)
	}
}

func TestBuildPatchSQL_Relation_RejectsMoveCopyTest(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[widgetPatch](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	parser := patch.NewParser(schema, nil)
	for _, badOp := range []string{"move", "copy", "test"} {
		_, err := parser.ParseOps([]patch.RawPatchOp{
			{Op: badOp, Path: "/tags", Value: []any{"t1"}, From: "/id"},
		})
		if err == nil {
			t.Errorf("op %q on relation should be rejected at parse time", badOp)
		}
	}
}

func TestBuildPatchSQL_Relation_AddRequiresValues(t *testing.T) {
	g := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[widgetPatch](g)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	parser := patch.NewParser(schema, nil)
	parsed, err := parser.ParseOps([]patch.RawPatchOp{
		{Op: "add", Path: "/tags", Value: []any{}},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{}); err == nil {
		t.Errorf("add with empty values should error")
	}
}

func TestRenderRelationOp_Replace_Diff(t *testing.T) {
	rel := &core.RelationSchema{
		JoinTable:    "widget_tags",
		SourceColumn: "widget_id",
		TargetColumn: "tag_id",
		PrimaryKey:   "id",
	}
	op := patch.RelationPatchOp{
		Field: "tags", Schema: rel, Verb: patch.RelationVerbReplace,
		Values: []any{"t1", "t2"},
	}
	stmts, err := patch.RenderRelationOp(op, filter.PostgresDialect{}, []any{"w1"}, patch.RenderRelationOpOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(stmts) != 2 {
		t.Fatalf("expected 2 stmts (DELETE NOT IN + INSERT), got %d", len(stmts))
	}
	if !strings.HasPrefix(stmts[0].SQL, "DELETE FROM widget_tags") {
		t.Errorf("stmt[0] should start with DELETE, got %q", stmts[0].SQL)
	}
	if !strings.Contains(stmts[0].SQL, "NOT IN") {
		t.Errorf("stmt[0] should contain NOT IN, got %q", stmts[0].SQL)
	}
	if !strings.HasPrefix(stmts[1].SQL, "INSERT INTO widget_tags") {
		t.Errorf("stmt[1] should start with INSERT, got %q", stmts[1].SQL)
	}
	if !strings.Contains(stmts[1].SQL, "ON CONFLICT DO NOTHING") {
		t.Errorf("stmt[1] should contain ON CONFLICT DO NOTHING, got %q", stmts[1].SQL)
	}
}

func TestRenderRelationOp_Replace_Clear(t *testing.T) {
	rel := &core.RelationSchema{
		JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id",
	}
	op := patch.RelationPatchOp{
		Field: "tags", Schema: rel, Verb: patch.RelationVerbReplace, Values: nil,
	}
	stmts, err := patch.RenderRelationOp(op, filter.PostgresDialect{}, []any{"w1"}, patch.RenderRelationOpOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("clear should emit one DELETE, got %d stmts", len(stmts))
	}
	if !strings.HasPrefix(stmts[0].SQL, "DELETE FROM widget_tags") {
		t.Errorf("expected DELETE prefix, got %q", stmts[0].SQL)
	}
	if strings.Contains(stmts[0].SQL, "tag_id") {
		t.Errorf("clear-DELETE should not filter by tag_id, got %q", stmts[0].SQL)
	}
}

func TestRenderRelationOp_Remove(t *testing.T) {
	rel := &core.RelationSchema{
		JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id",
	}
	op := patch.RelationPatchOp{
		Field: "tags", Schema: rel, Verb: patch.RelationVerbRemove, Values: []any{"t1"},
	}
	stmts, err := patch.RenderRelationOp(op, filter.PostgresDialect{}, []any{"w1"}, patch.RenderRelationOpOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("remove should emit one DELETE, got %d stmts", len(stmts))
	}
	if !strings.Contains(stmts[0].SQL, "tag_id IN") {
		t.Errorf("remove-DELETE should filter by tag_id IN (...), got %q", stmts[0].SQL)
	}
}

func TestRenderRelationOp_TenantScoped(t *testing.T) {
	rel := &core.RelationSchema{
		JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id",
		TenantColumn: "tenant_id",
	}
	op := patch.RelationPatchOp{
		Field: "tags", Schema: rel, Verb: patch.RelationVerbAdd, Values: []any{"t1"},
	}
	stmts, err := patch.RenderRelationOp(op, filter.PostgresDialect{}, []any{"w1"}, patch.RenderRelationOpOptions{TenantValue: "tenant-A"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("got %d stmts", len(stmts))
	}
	if !strings.Contains(stmts[0].SQL, "tenant_id") {
		t.Errorf("tenant-scoped INSERT should include tenant_id column, got %q", stmts[0].SQL)
	}
	hasTenantArg := false
	for _, a := range stmts[0].Args {
		if s, ok := a.(string); ok && s == "tenant-A" {
			hasTenantArg = true
		}
	}
	if !hasTenantArg {
		t.Errorf("expected tenant value in args, got %+v", stmts[0].Args)
	}
}

func TestRenderRelationOp_NoOwners(t *testing.T) {
	rel := &core.RelationSchema{
		JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id",
	}
	op := patch.RelationPatchOp{
		Field: "tags", Schema: rel, Verb: patch.RelationVerbReplace, Values: []any{"t1"},
	}
	stmts, err := patch.RenderRelationOp(op, filter.PostgresDialect{}, nil, patch.RenderRelationOpOptions{})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if len(stmts) != 0 {
		t.Errorf("zero owner PKs should produce zero stmts, got %d", len(stmts))
	}
}

func TestRenderRelationOp_TenantOptOut(t *testing.T) {
	// Schema has TenantColumn empty; opts pass a tenant value but it must be ignored.
	rel := &core.RelationSchema{
		JoinTable: "system_tags", SourceColumn: "tag_id", TargetColumn: "alias_id",
	}
	op := patch.RelationPatchOp{
		Field: "aliases", Schema: rel, Verb: patch.RelationVerbAdd, Values: []any{"a1"},
	}
	stmts, err := patch.RenderRelationOp(op, filter.PostgresDialect{}, []any{"t1"}, patch.RenderRelationOpOptions{TenantValue: "ignored"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, a := range stmts[0].Args {
		if s, ok := a.(string); ok && s == "ignored" {
			t.Errorf("tenant value bound despite empty TenantColumn: %q", stmts[0].SQL)
		}
	}
	if strings.Contains(stmts[0].SQL, "tenant") {
		t.Errorf("global junction SQL should not mention tenant: %q", stmts[0].SQL)
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
