//go:build unit

package patch_test

import (
	"strings"
	"testing"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// arrayAppendSchema fakes a generated PatchSchema where the
// /photo_blob_keys path opts into array_append (and only that op —
// replace is intentionally NOT allowed to prove AllowsOp gates the
// new verb correctly).
func arrayAppendSchema() *patch.PatchSchema {
	return &patch.PatchSchema{
		Resource: "work_orders",
		AllowedPaths: map[string]*patch.PathSchema{
			"/photo_blob_keys": {
				AllowedOps: []patch.PatchOp{patch.PatchOpArrayAppend},
				DataType:   core.FieldTypeString,
				SQLColumn:  "photo_blob_keys",
			},
			"/title": {
				AllowedOps: []patch.PatchOp{patch.PatchOpReplace},
				DataType:   core.FieldTypeString,
				SQLColumn:  "title",
			},
		},
		MaxOperations: 10,
	}
}

// TestParser_AcceptsArrayAppend exercises the wire-level path: a
// JSON-encoded `{"op":"array_append",...}` body must parse cleanly
// when the PathSchema lists the op as allowed. This is the FU-9
// regression — before the fix, `isValidPatchOp` rejected the string
// at the parser front door regardless of the schema.
func TestParser_AcceptsArrayAppend(t *testing.T) {
	t.Parallel()
	p := patch.NewParser(arrayAppendSchema(), nil)
	body := `[{"op":"array_append","path":"/photo_blob_keys","value":"tenants/x/work_orders/y/photos/abc.jpg"}]`
	result, err := p.Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(result.Operations) != 1 {
		t.Fatalf("expected 1 op, got %d", len(result.Operations))
	}
	if result.Operations[0].Op != patch.PatchOpArrayAppend {
		t.Errorf("op = %q, want %q", result.Operations[0].Op, patch.PatchOpArrayAppend)
	}
	if result.Operations[0].Schema == nil {
		t.Error("expected Schema to be resolved from PatchSchema")
	}
	if result.EstimatedCost <= 0 {
		t.Errorf("expected non-zero cost, got %d", result.EstimatedCost)
	}
}

// TestParser_RejectsArrayAppend_WhenNotAllowed asserts the AllowedOps
// gate fires correctly for the new verb. A schema where /title only
// allows `replace` must reject an `array_append` against /title with
// E2076 (operation not allowed on path).
func TestParser_RejectsArrayAppend_WhenNotAllowed(t *testing.T) {
	t.Parallel()
	p := patch.NewParser(arrayAppendSchema(), nil)
	body := `[{"op":"array_append","path":"/title","value":"x"}]`
	_, err := p.Parse([]byte(body))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not allowed on path") {
		t.Errorf("expected 'not allowed on path' error, got %v", err)
	}
}

// TestParser_RejectsUnknownPatchOp confirms isValidPatchOp still
// catches genuinely-bogus ops even after the ValidPatchOps refactor.
// The drift-killer change MUST NOT widen the accepted set beyond what
// schema.go declares.
func TestParser_RejectsUnknownPatchOp(t *testing.T) {
	t.Parallel()
	p := patch.NewParser(arrayAppendSchema(), nil)
	body := `[{"op":"squish","path":"/title","value":"x"}]`
	_, err := p.Parse([]byte(body))
	if err == nil {
		t.Fatal("expected error for unknown op")
	}
	if !strings.Contains(err.Error(), "invalid operation") {
		t.Errorf("expected 'invalid operation' error, got %v", err)
	}
}

// TestParser_ArrayAppend_ValidatorFires asserts the value-level
// validator chain (PathSchema.Validators) runs for array_append the
// same way it does for replace/add — uploaded blob keys can be
// length-checked, prefix-checked, etc. without an extra code path.
func TestParser_ArrayAppend_ValidatorFires(t *testing.T) {
	t.Parallel()
	schema := arrayAppendSchema()
	schema.AllowedPaths["/photo_blob_keys"].Validators = []core.Validator{
		func(v any) error {
			s, ok := v.(string)
			if !ok || !strings.HasPrefix(s, "tenants/") {
				return &core.ParseError{Code: "E2087", Message: "must start with tenants/"}
			}
			return nil
		},
	}
	p := patch.NewParser(schema, nil)
	body := `[{"op":"array_append","path":"/photo_blob_keys","value":"naughty/path.jpg"}]`
	_, err := p.Parse([]byte(body))
	if err == nil {
		t.Fatal("expected validator error")
	}
	if !strings.Contains(err.Error(), "must start with tenants/") {
		t.Errorf("expected validator message, got %v", err)
	}
}
