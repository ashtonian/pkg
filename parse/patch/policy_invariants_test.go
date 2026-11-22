//go:build unit

package patch_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

func p2Schema() *patch.PatchSchema {
	return &patch.PatchSchema{
		Resource: "items",
		AllowedPaths: map[string]*patch.PathSchema{
			"/name":       {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy}, DataType: core.FieldTypeString, SQLColumn: "name"},
			"/notes":      {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy}, DataType: core.FieldTypeString, SQLColumn: "notes"},
			"/status":     {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpMove, patch.PatchOpCopy}, DataType: core.FieldTypeString, SQLColumn: "status"},
			"/secret_ref": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpRemove, patch.PatchOpMove, patch.PatchOpCopy}, DataType: core.FieldTypeString, SQLColumn: "secret_ref"},
			"/config/**":  {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove}, DataType: core.FieldTypeJSON, SQLColumn: "config"},
		},
		MaxOperations: 20,
	}
}

func parseOps(t *testing.T, schema *patch.PatchSchema, body string) *patch.ParsedPatch {
	t.Helper()
	parsed, err := patch.NewParser(schema, nil).Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return parsed
}

// TestCopyFromForbiddenPath: a copy whose from is a policy-forbidden path
// must be rejected — previously only move checked From, so copy exfiltrated
// protected values into readable fields.
func TestCopyFromForbiddenPath(t *testing.T) {
	pc := mustPolicy(t, patch.ForbidPaths("/secret_ref"))
	parsed := parseOps(t, p2Schema(), `[{"op":"copy","from":"/secret_ref","path":"/notes"}]`)
	err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
	if err == nil {
		t.Fatal("copy from forbidden path allowed; want policy rejection")
	}
	var pe *core.ParseError
	if !errors.As(err, &pe) || pe.Code != patch.ECodePatchPolicyForbidden.String() {
		t.Errorf("err = %v; want %s", err, patch.ECodePatchPolicyForbidden.String())
	}
}

// TestMoveFromForbiddenPathStillRejected pins the pre-existing move check.
func TestMoveFromForbiddenPathStillRejected(t *testing.T) {
	pc := mustPolicy(t, patch.ForbidPaths("/secret_ref"))
	parsed := parseOps(t, p2Schema(), `[{"op":"move","from":"/secret_ref","path":"/notes"}]`)
	if err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil); err == nil {
		t.Fatal("move from forbidden path allowed; want policy rejection")
	}
}

// TestCopyIntoConstrainedPath / move: value constraints inspect op.Value,
// which is nil for move/copy — so any value could be staged in an
// unconstrained field and copied over a constrained one. Constrained paths
// accept only add/replace now.
func TestCopyIntoConstrainedPath(t *testing.T) {
	pc := mustPolicy(t, patch.ConstrainValues("/status", patch.ValueConstraint{AllowedValues: []any{"active", "inactive"}}))

	for _, op := range []string{"copy", "move"} {
		t.Run(op, func(t *testing.T) {
			parsed := parseOps(t, p2Schema(), `[{"op":"`+op+`","from":"/name","path":"/status"}]`)
			err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
			if err == nil {
				t.Fatalf("%s into value-constrained path allowed; want rejection", op)
			}
			var pe *core.ParseError
			if !errors.As(err, &pe) || pe.Code != patch.ECodePatchPolicyValue.String() {
				t.Errorf("err = %v; want %s", err, patch.ECodePatchPolicyValue.String())
			}
		})
	}

	// add/replace with a valid value still passes.
	parsed := parseOps(t, p2Schema(), `[{"op":"replace","path":"/status","value":"active"}]`)
	if err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil); err != nil {
		t.Fatalf("replace with allowed value rejected: %v", err)
	}
}

// TestDeepPathRejectedAtParse: pointer depth is bounded by
// ParserConfig.MaxDepth when the patch is parsed, so an
// over-deep path never reaches a document or a backend.
func TestDeepPathRejectedAtParse(t *testing.T) {
	schema := &patch.PatchSchema{
		Resource: "items",
		AllowedPaths: map[string]*patch.PathSchema{
			"/**": {AllowedOps: []patch.PatchOp{patch.PatchOpAdd}, DataType: core.FieldTypeAny},
		},
	}
	cfg := core.DefaultConfig()
	cfg.MaxDepth = 3
	p := patch.NewParser(schema, cfg)

	_, err := p.Parse([]byte(`[{"op":"add","path":"/a/b/c/d","value":1}]`))
	var pe *core.ParseError
	if !errors.As(err, &pe) || pe.Code != patch.ECodePathTooDeep.String() {
		t.Fatalf("4-segment path with MaxDepth 3: err = %v, want %s", err, patch.ECodePathTooDeep.String())
	}
	if _, err := p.Parse([]byte(`[{"op":"add","path":"/a/b/c","value":1}]`)); err != nil {
		t.Fatalf("3-segment path rejected: %v", err)
	}
}

// TestDeterministicWildcardSelection: overlapping wildcard patterns with
// different AllowedOps resolve to the most specific PathSchema on every call,
// whatever order the map yields them in.
func TestDeterministicWildcardSelection(t *testing.T) {
	mk := func() *patch.PatchSchema {
		return &patch.PatchSchema{
			Resource: "items",
			AllowedPaths: map[string]*patch.PathSchema{
				// /config/* is more specific than /config/** (one token, not
				// any number) and than /*/lang (a longer literal prefix), and
				// less than the exact /config/theme.
				"/config/theme": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "theme"},
				"/config/*":     {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd}, DataType: core.FieldTypeJSON, SQLColumn: "config"},
				"/config/**":    {AllowedOps: []patch.PatchOp{patch.PatchOpRemove}, DataType: core.FieldTypeJSON, SQLColumn: "subtree"},
				"/*/lang":       {AllowedOps: []patch.PatchOp{patch.PatchOpRemove}, DataType: core.FieldTypeJSON, SQLColumn: "lang"},
			},
			MaxOperations: 20,
		}
	}
	for i := range 100 {
		schema := mk() // a fresh map each round: a fresh iteration order
		parsed, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"add","path":"/config/lang","value":"en"}]`))
		if err != nil {
			t.Fatalf("iteration %d: add on /config/lang rejected — nondeterministic wildcard pick: %v", i, err)
		}
		if got := parsed.Operations[0].Schema.SQLColumn; got != "config" {
			t.Fatalf("iteration %d: matched %q; want config", i, got)
		}
	}
}

// TestValueConstraintDeterministicSelection: the same rule for a policy's
// constraints, given broadest first so that only the specificity order picks
// the specific one.
func TestValueConstraintDeterministicSelection(t *testing.T) {
	pc := mustPolicy(t,
		patch.ConstrainValues("/config/**", patch.ValueConstraint{AllowedValues: []any{"zzz"}}),
		patch.ConstrainValues("/*/x", patch.ValueConstraint{AllowedValues: []any{"zzz"}}),
		patch.ConstrainValues("/config/*", patch.ValueConstraint{AllowedValues: []any{"a"}}),
	)
	parsed := parseOps(t, p2Schema(), `[{"op":"replace","path":"/config/x","value":"a"}]`)
	if err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil); err != nil {
		t.Fatalf("allowed value rejected — a broader constraint won the pick: %v", err)
	}
}

// A policy is checked when it is built: a token holding * among other
// characters, which would match nothing, and a value pattern that does not
// compile are startup errors, never a request's E2085.
func TestPolicyRefusesBadPatternsAtConstruction(t *testing.T) {
	for _, tc := range []struct {
		opt  patch.PolicyOption
		want string
	}{
		{patch.ForbidPaths("/tenant_id", "/internal_*"), `token "internal_*"`},
		{patch.ForbidPaths("/c**"), `token "c**"`},
		{patch.ForbidPaths(""), "names no path"},
		{patch.ForbidPaths("/a/~2"), `"/a/~2"`},
		{patch.ConstrainValues("/config/*_x", patch.ValueConstraint{}), `token "*_x"`},
		{patch.ConstrainValues("/status", patch.ValueConstraint{Pattern: "[invalid"}), `pattern "[invalid"`},
		{patch.ValidateWith(nil), "validator 0 is nil"},
	} {
		_, err := patch.NewPatchPolicyConstraints(tc.opt)
		if assert.Error(t, err) {
			assert.Contains(t, err.Error(), tc.want)
		}
	}
	_, err := patch.NewPatchPolicyConstraints(
		patch.ConstrainValues("/status", patch.ValueConstraint{}),
		patch.ConstrainValues("/status", patch.ValueConstraint{}),
	)
	assert.ErrorContains(t, err, `value constraint "/status" is given twice`)
}

// ** takes zero tokens too, so a subtree pattern forbids its root.
func TestForbiddenSubtreeCoversItsRoot(t *testing.T) {
	pc := mustPolicy(t, patch.ForbidPaths("/doc/**"))
	for _, body := range []string{
		`[{"op":"replace","path":"/doc","value":{}}]`,
		`[{"op":"replace","path":"/doc/a","value":1}]`,
	} {
		requireCode(t, patch.ApplyPatchPolicyConstraints(parseWrites(t, false, body), pc, nil), patch.ECodePatchPolicyForbidden.String())
	}
	assert.NoError(t, patch.ApplyPatchPolicyConstraints(parseWrites(t, false, `[{"op":"replace","path":"/other","value":{}}]`), pc, nil))
}

// TestValidatorsRunOnExplicitNull: a replace carrying an explicit null
// previously skipped field validators entirely.
func TestValidatorsRunOnExplicitNull(t *testing.T) {
	rejectNil := func(v any) error {
		if v == nil {
			return errors.New("null not permitted")
		}
		return nil
	}
	schema := p2Schema()
	schema.AllowedPaths["/name"].Validators = []core.Validator{rejectNil}

	_, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":null}]`))
	if err == nil {
		t.Fatal("replace with explicit null bypassed validators; want rejection")
	}
	if !strings.Contains(err.Error(), "null not permitted") {
		t.Errorf("unexpected error: %v", err)
	}

	// A remove writes null too, whatever value it carries; so does a merge
	// patch's null.
	for _, body := range []string{`[{"op":"remove","path":"/name"}]`, `[{"op":"remove","path":"/name","value":"x"}]`} {
		_, err = patch.NewParser(schema, nil).Parse([]byte(body))
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
		assert.ErrorContains(t, err, "null not permitted", body)
	}
	_, err = patch.ParseMergePatch([]byte(`{"name":null}`), schema, nil)
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
	// A move writes null into its source.
	_, err = patch.NewParser(schema, nil).Parse([]byte(`[{"op":"move","from":"/name","path":"/notes"}]`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
	assert.ErrorContains(t, err, "null not permitted")

	// A validator that tolerates nil still passes.
	schema2 := p2Schema()
	schema2.AllowedPaths["/name"].Validators = []core.Validator{func(any) error { return nil }}
	if _, err := patch.NewParser(schema2, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":null}]`)); err != nil {
		t.Fatalf("nil-tolerant validator rejected: %v", err)
	}
}
