//go:build unit

package patch_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

func testPatchSchema() *patch.PatchSchema {
	return newSchema("devices", 50, map[string]*patch.PathSchema{
		"/name":   {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "name"},
		"/status": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "status", Validators: []patch.Validator{enumValidator("active", "offline")}},
		// ID not in schema — not patchable (no parse:"patch" tag)
	})
}

func enumValidator(values ...string) patch.Validator {
	allowed := make(map[string]struct{}, len(values))
	for _, v := range values {
		allowed[v] = struct{}{}
	}
	return func(value any) error {
		s, ok := value.(string)
		if !ok {
			return nil
		}
		if _, found := allowed[s]; !found {
			return core.NewError(core.CodeInvalidEnumValue, "invalid enum")
		}
		return nil
	}
}

func TestParser_ValidReplace(t *testing.T) {
	p := patch.NewParser(testPatchSchema(), nil)
	result, err := p.Parse([]byte(`[{"op":"replace","path":"/name","value":"new-name"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Operations))
	}
	if result.Operations[0].Op != patch.PatchOpReplace {
		t.Errorf("expected replace, got %s", result.Operations[0].Op)
	}
	if result.Operations[0].Path != "/name" {
		t.Errorf("expected /name, got %s", result.Operations[0].Path)
	}
}

func TestParser_UnknownPath(t *testing.T) {
	p := patch.NewParser(testPatchSchema(), nil)
	_, err := p.Parse([]byte(`[{"op":"replace","path":"/unknown","value":"x"}]`))
	if err == nil {
		t.Fatal("expected error for unknown path")
	}
}

func TestParser_UnknownPath_ID(t *testing.T) {
	p := patch.NewParser(testPatchSchema(), nil)
	_, err := p.Parse([]byte(`[{"op":"replace","path":"/id","value":"new-id"}]`))
	if err == nil {
		t.Fatal("expected error for path not in schema")
	}
}

func TestParser_EnumValidation(t *testing.T) {
	p := patch.NewParser(testPatchSchema(), nil)
	_, err := p.Parse([]byte(`[{"op":"replace","path":"/status","value":"active"}]`))
	if err != nil {
		t.Fatalf("valid enum should pass: %v", err)
	}
	_, err = p.Parse([]byte(`[{"op":"replace","path":"/status","value":"bad"}]`))
	if err == nil {
		t.Fatal("expected error for invalid enum")
	}
}

func TestParser_InvalidJSON(t *testing.T) {
	p := patch.NewParser(testPatchSchema(), nil)
	_, err := p.Parse([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParser_InvalidOp(t *testing.T) {
	p := patch.NewParser(testPatchSchema(), nil)
	_, err := p.Parse([]byte(`[{"op":"destroy","path":"/name","value":"x"}]`))
	if err == nil {
		t.Fatal("expected error for invalid op")
	}
}

func TestParser_AffectedPaths(t *testing.T) {
	p := patch.NewParser(testPatchSchema(), nil)
	result, err := p.Parse([]byte(`[{"op":"replace","path":"/name","value":"a"},{"op":"replace","path":"/status","value":"active"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AffectedPaths) != 2 {
		t.Errorf("expected 2 affected paths, got %d", len(result.AffectedPaths))
	}
}

// --- BuildPatchSQL Tests ---

// parseSQLPatch parses body against a schema whose /name can be replaced and
// removed.
func parseSQLPatch(t *testing.T, body string) *patch.ParsedPatch {
	t.Helper()
	schema := newSchema("devices", 0, map[string]*patch.PathSchema{
		"/name": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove}, DataType: core.FieldTypeString, SQLColumn: "name"},
	})
	parsed, err := patch.NewParser(schema, nil).Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestBuildPatchSQL_Replace(t *testing.T) {
	parsed := parseSQLPatch(t, `[{"op":"replace","path":"/name","value":"new"}]`)
	result, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Empty {
		t.Error("expected non-empty")
	}
	if !strings.Contains(result.SetClause, `"name" = $1`) {
		t.Errorf("expected name = $1, got %q", result.SetClause)
	}
}

func TestBuildPatchSQL_Remove(t *testing.T) {
	parsed := parseSQLPatch(t, `[{"op":"remove","path":"/name"}]`)
	result, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.SetClause, `"name" = NULL`) {
		t.Errorf("expected NULL, got %q", result.SetClause)
	}
}

func TestBuildPatchSQL_SQLite(t *testing.T) {
	parsed := parseSQLPatch(t, `[{"op":"replace","path":"/name","value":"new"}]`)
	result, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.SetClause, `"name" = ?1`) {
		t.Errorf("expected ?1, got %q", result.SetClause)
	}
}

// --- MergePatch Tests ---

func TestParseMergePatch_Replace(t *testing.T) {
	result, err := patch.ParseMergePatch(
		[]byte(`{"name":"updated","status":"active"}`),
		testPatchSchema(), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Operations) != 2 {
		t.Errorf("expected 2 ops, got %d", len(result.Operations))
	}
	for _, op := range result.Operations {
		if op.Op != patch.PatchOpReplace {
			t.Errorf("expected replace, got %s", op.Op)
		}
	}
}

func TestParseMergePatch_NullRemoves(t *testing.T) {
	schema := newSchema("test", 50, map[string]*patch.PathSchema{
		"/name": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove}, DataType: core.FieldTypeString},
	})
	result, err := patch.ParseMergePatch([]byte(`{"name":null}`), schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations[0].Op != patch.PatchOpRemove {
		t.Errorf("expected remove for null, got %s", result.Operations[0].Op)
	}
}

func TestParseMergePatch_MaxOperations(t *testing.T) {
	schema := newSchema("test", 2, map[string]*patch.PathSchema{
		"/a": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString},
		"/b": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString},
		"/c": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString},
	})

	// 3 top-level keys exceeds MaxOperations=2 → rejected like the JSON-Patch path.
	if _, err := patch.ParseMergePatch([]byte(`{"a":"1","b":"2","c":"3"}`), schema, nil); err == nil {
		t.Fatal("expected a too-many-operations error for 3 keys with MaxOperations=2")
	} else if !strings.Contains(err.Error(), "too many operations") {
		t.Errorf("error = %q, want it to mention too many operations", err.Error())
	}

	// 2 keys is within the limit.
	if _, err := patch.ParseMergePatch([]byte(`{"a":"1","b":"2"}`), schema, nil); err != nil {
		t.Fatalf("2 keys within MaxOperations=2 should parse, got %v", err)
	}
}

// --- Policy Tests ---

func TestApplyPatchPolicy_ForbiddenPath(t *testing.T) {
	parsed := parseSQLPatch(t, `[{"op":"replace","path":"/name","value":"x"}]`)
	pc := mustPolicy(t, patch.ForbidPaths("/name"))
	err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
	if err == nil {
		t.Fatal("expected error for forbidden path")
	}
}

func TestApplyPatchPolicy_AllowedValues(t *testing.T) {
	pc := mustPolicy(t, patch.ConstrainValues("/name", patch.ValueConstraint{AllowedValues: []any{"active", "inactive"}}))
	err := patch.ApplyPatchPolicyConstraints(parseSQLPatch(t, `[{"op":"replace","path":"/name","value":"active"}]`), pc, nil)
	if err != nil {
		t.Fatalf("expected allowed: %v", err)
	}

	err = patch.ApplyPatchPolicyConstraints(parseSQLPatch(t, `[{"op":"replace","path":"/name","value":"forbidden"}]`), pc, nil)
	if err == nil {
		t.Fatal("expected error for disallowed value")
	}
}

// --- Schema Generation Tests ---

func TestGeneratePatchSchema(t *testing.T) {
	type Device struct {
		ID     string `json:"id"     parse:"filter"`
		Name   string `json:"name"   parse:"filter,patch" patch_ops:"replace"`
		Status string `json:"status" parse:"patch" patch_ops:"replace" enum:"active,offline"`
	}
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[Device](gen)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Path("/name"); !ok {
		t.Error("missing /name path")
	}
	if _, ok := schema.Path("/status"); !ok {
		t.Error("missing /status path")
	}
	if _, ok := schema.Path("/id"); ok {
		t.Error("/id should not be patchable (no parse:patch tag)")
	}
}
