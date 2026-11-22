//go:build unit

package patch_test

import (
	"maps"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

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
	tagsPath, ok := schema.Path("/tags")
	if !ok {
		t.Fatalf("expected /tags path in schema, got %v", keys(maps.Collect(schema.Paths())))
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

// RenderRelationOp refuses what it cannot render rather than trusting the
// operation: a tenant-scoped pair table without a tenant fails closed.
func TestRenderRelationOp_RefusesWhatItCannotRender(t *testing.T) {
	rel := &core.RelationSchema{JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id"}
	scoped := &core.RelationSchema{JoinTable: "widget_tags", SourceColumn: "widget_id", TargetColumn: "tag_id", TenantColumn: "tenant_id"}
	for name, op := range map[string]patch.RelationPatchOp{
		"remove with no values":      {Field: "tags", Schema: rel, Verb: patch.RelationVerbRemove},
		"add with no values":         {Field: "tags", Schema: rel, Verb: patch.RelationVerbAdd, Values: []any{}},
		"a null value":               {Field: "tags", Schema: rel, Verb: patch.RelationVerbAdd, Values: []any{"t1", nil}},
		"values of two types":        {Field: "tags", Schema: rel, Verb: patch.RelationVerbAdd, Values: []any{"t1", 2}},
		"an unknown verb":            {Field: "tags", Schema: rel, Verb: "merge", Values: []any{"t1"}},
		"a tenant-scoped pair table": {Field: "tags", Schema: scoped, Verb: patch.RelationVerbAdd, Values: []any{"t1"}},
		"a value that is not a key":  {Field: "tags", Schema: rel, Verb: patch.RelationVerbAdd, Values: []any{map[string]any{}}},
	} {
		for _, d := range []patch.SQLDialect{filter.PostgresDialect{}, filter.SQLiteDialect{}} {
			_, err := patch.RenderRelationOp(op, d, []any{"w1"}, patch.RenderRelationOpOptions{})
			assert.Error(t, err, "%s on %s", name, d.Name())
		}
	}
	_, err := patch.RenderRelationOp(patch.RelationPatchOp{Field: "tags", Schema: rel, Verb: patch.RelationVerbAdd, Values: []any{"t1"}}, unknownSQLDialect{}, []any{"w1"}, patch.RenderRelationOpOptions{})
	assert.Error(t, err)
}

// unknownSQLDialect is a dialect relation statements have no form for.
type unknownSQLDialect struct{ filter.SQLiteDialect }

func (unknownSQLDialect) Name() string { return "unknown" }

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

// A relation operation names at most MaxRelationValues keys, repeats counted
// once: one more is E2119 (422) from either parser, with the count and the
// limit in Details, before any SQL exists.
func TestRelationValueLimit(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[widgetPatch](core.NewSchemaGenerator())
	if err != nil {
		t.Fatal(err)
	}
	schema.MaxRelationValues = 3
	keys := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = `"t` + string(rune('a'+i)) + `"`
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	for _, body := range []string{`{"tags":` + keys(4) + `}`, `[{"op":"add","path":"/tags","value":` + keys(4) + `}]`} {
		var err error
		if body[0] == '{' {
			_, err = patch.ParseMergePatch([]byte(body), schema, nil)
		} else {
			_, err = patch.NewParser(schema, nil).Parse([]byte(body))
		}
		var pe *core.ParseError
		if assert.ErrorAs(t, err, &pe, body) {
			assert.Equal(t, patch.ECodeRelationPatchTooLarge.String(), pe.Code)
			assert.Equal(t, 422, pe.ErrorHTTPStatus())
			assert.Equal(t, map[string]any{patch.DetailValueCount: 4, patch.DetailValueLimit: 3}, pe.Details)
		}
	}

	parsed, err := patch.ParseMergePatch([]byte(`{"tags":["ta","tb","ta","tc","tb"]}`), schema, nil)
	if err != nil {
		t.Fatalf("repeated keys within the limit: %v", err)
	}
	assert.Equal(t, []any{"ta", "tb", "tc"}, parsed.Operations[0].Value, "each key once, in the order first named")
	sqlPatch, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatal(err)
	}
	stmts, err := patch.RenderRelationOp(sqlPatch.RelationOps[0], filter.PostgresDialect{}, []any{"w1"}, patch.RenderRelationOpOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, []string{"ta", "tb", "tc"}, stmts[0].Args[1], "the replace's keys bind once")
}
