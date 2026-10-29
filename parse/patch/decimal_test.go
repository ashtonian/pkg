//go:build unit

package patch_test

import (
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type invoice struct {
	Total decimal.Decimal  `json:"total" db:"total" parse:"patch"`
	Tip   *decimal.Decimal `json:"tip"   db:"tip"   parse:"patch"`
}

func TestDecimal_PatchSchema(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[invoice](core.NewSchemaGenerator())
	require.NoError(t, err)
	for path, ops := range map[string][]patch.PatchOp{
		"/total": {patch.PatchOpReplace},
		"/tip":   {patch.PatchOpReplace, patch.PatchOpRemove},
	} {
		ps := schema.AllowedPaths[path]
		require.NotNil(t, ps, path)
		assert.Equal(t, core.FieldTypeDecimal, ps.DataType, path)
		assert.Equal(t, ops, ps.AllowedOps, path)
	}
}

func TestDecimal_PatchValues(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[invoice](core.NewSchemaGenerator())
	require.NoError(t, err)
	parser := patch.NewParser(schema, nil)
	tests := []struct {
		body string
		want any
	}{
		{`[{"op":"replace","path":"/total","value":"10.50"}]`, "10.5"},
		{`[{"op":"replace","path":"/total","value":10.5}]`, "10.5"},
		{`[{"op":"replace","path":"/total","value":"2.5e-1"}]`, "0.25"},
		{`[{"op":"replace","path":"/tip","value":null}]`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.body, func(t *testing.T) {
			parsed, err := parser.Parse([]byte(tt.body))
			require.NoError(t, err)
			require.Len(t, parsed.Operations, 1)
			assert.Equal(t, tt.want, parsed.Operations[0].Value)
		})
	}
	for _, body := range []string{
		`[{"op":"replace","path":"/total","value":"ten"}]`,
		`[{"op":"replace","path":"/total","value":null}]`,
		`[{"op":"replace","path":"/total","value":{"value":"1"}}]`,
	} {
		_, err := parser.Parse([]byte(body))
		requireCode(t, err, patch.ECodePatchValueInvalid.String())
	}
}

// Bounds and lists compare a decimal path's value exactly, by value: never
// as text, and never through a float.
func TestDecimal_PolicyConstraints(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[invoice](core.NewSchemaGenerator())
	require.NoError(t, err)
	parser := patch.NewParser(schema, nil)
	bounded, err := patch.NewValueConstraint("", nil, []any{"13.00"}, new(0.1), new(100.0))
	require.NoError(t, err)
	pc := &patch.PatchPolicyConstraints{ValueConstraints: map[string]*patch.ValueConstraint{"/total": bounded}}
	tests := []struct {
		value   string
		allowed bool
	}{
		{`"0.1"`, true},
		{`"0.09999999999999999"`, false},
		{`"0.10000000000000001"`, true},
		{`"9.5"`, true},
		{`"100.00"`, true},
		{`"100.000000000000000001"`, false},
		{`"-1"`, false},
		{`13`, false},
		{`"1.3e1"`, false},
		{`"13.000001"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/total","value":` + tt.value + `}]`))
			require.NoError(t, err)
			err = patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
			if tt.allowed {
				assert.NoError(t, err)
			} else {
				requireCode(t, err, patch.ECodePatchPolicyValue.String())
			}
		})
	}

	allowed, err := patch.NewValueConstraint("", []any{"0.5", 2}, nil, nil, nil)
	require.NoError(t, err)
	pc = &patch.PatchPolicyConstraints{ValueConstraints: map[string]*patch.ValueConstraint{"/tip": allowed}}
	for value, ok := range map[string]bool{`"0.50"`: true, `"2.0"`: true, `2`: true, `"5e-1"`: true, `"0.25"`: false} {
		parsed, err := parser.Parse([]byte(`[{"op":"replace","path":"/tip","value":` + value + `}]`))
		require.NoError(t, err)
		err = patch.ApplyPatchPolicyConstraints(parsed, pc, nil)
		if ok {
			assert.NoError(t, err, value)
		} else {
			requireCode(t, err, patch.ECodePatchPolicyValue.String())
		}
	}
}
