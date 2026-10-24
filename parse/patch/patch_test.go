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
	return &patch.PatchSchema{
		Resource: "devices",
		AllowedPaths: map[string]*patch.PathSchema{
			"/name":   {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "name"},
			"/status": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "status", Validators: []core.Validator{enumValidator("active", "offline")}},
			// ID not in schema — not patchable (no parse:"patch" tag)
		},
		MaxOperations: 50,
	}
}

func enumValidator(values ...string) core.Validator {
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
			return &core.ParseError{Code: "E2087", Message: "invalid enum"}
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

func TestBuildPatchSQL_Replace(t *testing.T) {
	parsed := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpReplace, Path: "/name", Value: "new", Schema: &patch.PathSchema{SQLColumn: "name", DataType: core.FieldTypeString}},
		},
	}
	result, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Empty {
		t.Error("expected non-empty")
	}
	if !strings.Contains(result.SetClause, "name = $1") {
		t.Errorf("expected name = $1, got %q", result.SetClause)
	}
}

func TestBuildPatchSQL_Remove(t *testing.T) {
	parsed := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpRemove, Path: "/name", Schema: &patch.PathSchema{SQLColumn: "name"}},
		},
	}
	result, err := patch.BuildPatchSQL(parsed, filter.PostgresDialect{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.SetClause, "name = NULL") {
		t.Errorf("expected NULL, got %q", result.SetClause)
	}
}

func TestBuildPatchSQL_SQLite(t *testing.T) {
	parsed := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpReplace, Path: "/name", Value: "new", Schema: &patch.PathSchema{SQLColumn: "name", DataType: core.FieldTypeString}},
		},
	}
	result, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.SetClause, "name = ?1") {
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
	schema := &patch.PatchSchema{
		Resource: "test",
		AllowedPaths: map[string]*patch.PathSchema{
			"/name": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove}, DataType: core.FieldTypeString},
		},
		MaxOperations: 50,
	}
	result, err := patch.ParseMergePatch([]byte(`{"name":null}`), schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operations[0].Op != patch.PatchOpRemove {
		t.Errorf("expected remove for null, got %s", result.Operations[0].Op)
	}
}

func TestParseMergePatch_MaxOperations(t *testing.T) {
	schema := &patch.PatchSchema{
		Resource: "test",
		AllowedPaths: map[string]*patch.PathSchema{
			"/a": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString},
			"/b": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString},
			"/c": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString},
		},
		MaxOperations: 2,
	}

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
	parsed := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpReplace, Path: "/secret", Value: "x"},
		},
	}
	pc := &patch.PatchPolicyConstraints{ForbiddenPaths: []string{"/secret"}}
	err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
	if err == nil {
		t.Fatal("expected error for forbidden path")
	}
}

func TestApplyPatchPolicy_AllowedValues(t *testing.T) {
	parsed := &patch.ParsedPatch{
		Operations: []*patch.PatchOperation{
			{Op: patch.PatchOpReplace, Path: "/status", Value: "active"},
		},
	}
	pc := &patch.PatchPolicyConstraints{
		ValueConstraints: map[string]*patch.ValueConstraint{
			"/status": {AllowedValues: []any{"active", "inactive"}},
		},
	}
	err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
	if err != nil {
		t.Fatalf("expected allowed: %v", err)
	}

	parsed.Operations[0].Value = "forbidden"
	err = patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
	if err == nil {
		t.Fatal("expected error for disallowed value")
	}
}

func TestNewValueConstraint_ValidPattern(t *testing.T) {
	vc, err := patch.NewValueConstraint(`^[a-z]+$`, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if vc == nil {
		t.Fatal("expected non-nil constraint")
	}
}

func TestNewValueConstraint_InvalidPattern(t *testing.T) {
	_, err := patch.NewValueConstraint(`[invalid`, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid regex pattern")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("error should mention invalid pattern, got: %s", err.Error())
	}
}

func TestNewValueConstraint_NoPattern(t *testing.T) {
	vc, err := patch.NewValueConstraint("", []any{"a", "b"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(vc.AllowedValues) != 2 {
		t.Errorf("expected 2 allowed values, got %d", len(vc.AllowedValues))
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
	if _, ok := schema.AllowedPaths["/name"]; !ok {
		t.Error("missing /name path")
	}
	if _, ok := schema.AllowedPaths["/status"]; !ok {
		t.Error("missing /status path")
	}
	if _, ok := schema.AllowedPaths["/id"]; ok {
		t.Error("/id should not be patchable (no parse:patch tag)")
	}
}
