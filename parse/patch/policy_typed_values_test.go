//go:build unit

package patch_test

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type typedRole struct {
	Role string `json:"role"`
}

// typedValuesSchema has a document with no Go type (/doc), an interface
// field (/any) and a map of interfaces (/meta): the paths whose values a
// caller of ParseOps or ParseMergePatchMap may give as typed Go values.
func typedValuesSchema() *patch.PatchSchema {
	ops := []patch.PatchOp{patch.PatchOpReplace, patch.PatchOpAdd, patch.PatchOpRemove}
	return newSchema("items", 20, map[string]*patch.PathSchema{
		"/doc":     {AllowedOps: ops, DataType: core.FieldTypeJSON, SQLColumn: "doc"},
		"/doc/**":  {AllowedOps: ops, DataType: core.FieldTypeAny},
		"/any":     {AllowedOps: ops, DataType: core.FieldTypeAny, GoType: reflect.TypeFor[any](), SQLColumn: "any"},
		"/meta":    {AllowedOps: ops, DataType: core.FieldTypeObject, GoType: reflect.TypeFor[map[string]any](), SQLColumn: "meta"},
		"/meta/**": {AllowedOps: ops, DataType: core.FieldTypeAny},
	})
}

// A value constraint judges a typed Go value by its JSON document, which is
// what SQL stores: a map[string]string or a struct hides no member from it,
// wherever the value stands.
func TestPolicy_JudgesTypedValuesByTheirDocument(t *testing.T) {
	deny := func(path string) *patch.PatchPolicyConstraints {
		return mustPolicy(t, patch.ConstrainValues(path, patch.ValueConstraint{DeniedValues: []any{"admin"}}))
	}
	op := func(path string, value any) []patch.RawPatchOp {
		ops := []patch.RawPatchOp{{Op: "replace", Path: path, Value: value, HasValue: true}}
		return ops
	}
	for _, tc := range []struct {
		name   string
		ops    []patch.RawPatchOp
		merge  map[string]any
		policy string
	}{
		{"typed map below a document", op("/doc/labels", map[string]string{"role": "admin"}), nil, "/doc/labels/role"},
		{"struct below a document", op("/doc/inner", typedRole{"admin"}), nil, "/doc/inner/role"},
		{"typed slice of structs below a document", op("/doc/list", []typedRole{{"admin"}}), nil, "/doc/list/0/role"},
		{"typed map in a document with no Go type", op("/doc", map[string]any{"labels": map[string]string{"role": "admin"}}), nil, "/doc/labels/role"},
		{"typed map in an interface field", op("/any", map[string]string{"role": "admin"}), nil, "/any/role"},
		{"typed scalar in an interface field's member", op("/any", map[string]any{"role": typedText("admin")}), nil, "/any/role"},
		{"typed map merged into a map of interfaces", nil, map[string]any{"meta": map[string]any{"x": map[string]string{"role": "admin"}}}, "/meta/x/role"},
		{"typed map merged below an object member", nil, map[string]any{"meta": map[string]any{"x": map[string]any{"y": typedRole{"admin"}}}}, "/meta/x/y/role"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				parsed *patch.ParsedPatch
				err    error
			)
			if tc.merge != nil {
				parsed, err = patch.ParseMergePatchMap(tc.merge, typedValuesSchema(), nil)
			} else {
				parsed, err = patch.NewParser(typedValuesSchema(), nil).ParseOps(tc.ops)
			}
			require.NoError(t, err)
			err = patch.ApplyPatchPolicyConstraints(parsed, deny(tc.policy), nil)
			requireCode(t, err, "E2085")
		})
	}
}

type typedText string

// The value an untyped position holds is the document, so policy, SQL and
// apply read one form; a value JSON cannot hold is refused at parse.
func TestParse_UntypedPositionsHoldDocuments(t *testing.T) {
	parsed, err := patch.NewParser(typedValuesSchema(), nil).ParseOps([]patch.RawPatchOp{
		{Op: "replace", Path: "/doc/labels", Value: map[string]int{"n": 1}, HasValue: true},
		{Op: "replace", Path: "/any", Value: []typedRole{{"r"}}, HasValue: true},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"n": json.Number("1")}, parsed.Operations[0].Value)
	assert.Equal(t, []any{map[string]any{"role": "r"}}, parsed.Operations[1].Value)

	_, err = patch.NewParser(typedValuesSchema(), nil).ParseOps([]patch.RawPatchOp{
		{Op: "replace", Path: "/doc/x", Value: map[string]float64{"n": math.NaN()}, HasValue: true},
	})
	requireCode(t, err, "E2077")
}
