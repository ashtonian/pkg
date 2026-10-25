//go:build unit

package patch_test

import (
	"errors"
	"strings"
	"testing"

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
	pc := &patch.PatchPolicyConstraints{ForbiddenPaths: []string{"/secret_ref"}}
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
	pc := &patch.PatchPolicyConstraints{ForbiddenPaths: []string{"/secret_ref"}}
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
	vc, err := patch.NewValueConstraint("", []any{"active", "inactive"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	pc := &patch.PatchPolicyConstraints{ValueConstraints: map[string]*patch.ValueConstraint{"/status": vc}}

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
// different AllowedOps must resolve to the same (most-specific) PathSchema on
// every call — previously map iteration made the selection random per
// process/call.
func TestDeterministicWildcardSelection(t *testing.T) {
	mk := func() *patch.PatchSchema {
		return &patch.PatchSchema{
			Resource: "items",
			AllowedPaths: map[string]*patch.PathSchema{
				// /config/* is more specific (longer literal prefix) than /c**
				// and less than the exact /config/theme.
				"/config/theme": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "theme"},
				"/config/*":     {AllowedOps: []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd}, DataType: core.FieldTypeJSON, SQLColumn: "config"},
				"/c**":          {AllowedOps: []patch.PatchOp{patch.PatchOpRemove}, DataType: core.FieldTypeJSON, SQLColumn: "cstar"},
			},
			MaxOperations: 20,
		}
	}

	// /config/lang matches both /config/* and /c** — /config/* has the longer
	// literal prefix so its ops must win, every single time. With the old map
	// iteration, ~half the runs picked /c** (remove-only) and this parse
	// failed.
	for i := range 100 {
		schema := mk() // fresh schema each round — fresh map iteration order
		parsed, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"add","path":"/config/lang","value":"en"}]`))
		if err != nil {
			t.Fatalf("iteration %d: add on /config/lang rejected — nondeterministic wildcard pick: %v", i, err)
		}
		if got := parsed.Operations[0].Schema.SQLColumn; got != "config" {
			t.Fatalf("iteration %d: matched %q; want config", i, got)
		}
	}
}

// TestValueConstraintDeterministicSelection: same class via the policy map.
func TestValueConstraintDeterministicSelection(t *testing.T) {
	specific, _ := patch.NewValueConstraint("", []any{"a"}, nil, nil, nil)
	broad, _ := patch.NewValueConstraint("", []any{"zzz"}, nil, nil, nil)
	pc := &patch.PatchPolicyConstraints{ValueConstraints: map[string]*patch.ValueConstraint{
		"/config/*": specific, // longer literal prefix — must win for /config/x
		"/c**":      broad,
	}}
	for i := range 100 {
		parsed := parseOps(t, p2Schema(), `[{"op":"replace","path":"/config/x","value":"a"}]`)
		if err := patch.ApplyPatchPolicyConstraints(parsed, pc, nil); err != nil {
			t.Fatalf("iteration %d: allowed value rejected — broad constraint won the pick: %v", i, err)
		}
	}
}

// TestSlashStarMatchesOneSegmentOnly: the "/*" special case treated it as
// match-everything; it must mean exactly one segment under root.
func TestSlashStarMatchesOneSegmentOnly(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/*", "/a", true},
		{"/*", "/name", true},
		{"/*", "/a/b", false},
		{"/*", "/a/b/c", false},
		{"**", "/a", true},
		{"**", "/a/b/c", true},
		{"/config/*", "/config/x", true},
		{"/config/*", "/config/x/y", false},
		{"/config/**", "/config/x/y", true},
	}
	for _, tc := range cases {
		if got := core.MatchWildcard(tc.pattern, tc.path); got != tc.want {
			t.Errorf("MatchWildcard(%q, %q) = %v; want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
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

	// A validator that tolerates nil still passes.
	schema2 := p2Schema()
	schema2.AllowedPaths["/name"].Validators = []core.Validator{func(any) error { return nil }}
	if _, err := patch.NewParser(schema2, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":null}]`)); err != nil {
		t.Fatalf("nil-tolerant validator rejected: %v", err)
	}
}
