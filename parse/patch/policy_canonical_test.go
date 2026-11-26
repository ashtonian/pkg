//go:build unit

package patch_test

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/ordertest"
	"github.com/ashtonian/pkg/parse/patch"
)

// A value constraint lists values as its path's type holds them: a value
// written there is allowed, or denied, by every spelling of it a policy
// lists, and by no spelling of another value.
func TestPolicy_ListedValuesCompareInTheirPathsType(t *testing.T) {
	for _, c := range ordertest.Cases() {
		t.Run(c.Name, func(t *testing.T) {
			schema := newSchema("items", 10, map[string]*patch.PathSchema{
				"/v": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: c.Type.Type, GoType: numberType(c.Type.NumberKind), SQLColumn: "v"},
			})
			for i, x := range c.Values {
				written, err := c.Stored(x)
				require.NoError(t, err)
				parsed, err := patch.NewParser(schema, nil).ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/v", Value: written, HasValue: true}})
				require.NoError(t, err, "%#v", written)
				for j, y := range c.Values {
					for _, listed := range y.Spellings {
						allow := mustPolicy(t, patch.ConstrainValues("/v", patch.ValueConstraint{AllowedValues: []any{listed}}))
						deny := mustPolicy(t, patch.ConstrainValues("/v", patch.ValueConstraint{DeniedValues: []any{listed}}))
						allowed := patch.ApplyPatchPolicyConstraints(parsed, allow, nil)
						denied := patch.ApplyPatchPolicyConstraints(parsed, deny, nil)
						if i == j {
							require.NoError(t, allowed, "%#v allowed by %#v", written, listed)
							require.Error(t, denied, "%#v denied by %#v", written, listed)
							continue
						}
						require.Error(t, allowed, "%#v not allowed by %#v", written, listed)
						require.NoError(t, denied, "%#v not denied by %#v", written, listed)
					}
				}
			}
		})
	}
}

// numberType is the Go type of a number kind's path, nil for no kind.
func numberType(kind reflect.Kind) reflect.Type {
	switch kind {
	case reflect.Int64:
		return reflect.TypeFor[int64]()
	case reflect.Float64:
		return reflect.TypeFor[float64]()
	case reflect.Float32:
		return reflect.TypeFor[float32]()
	}
	return nil
}

// A float32 path's bounds and listed values compare at float32 precision,
// the value the path writes: a write of 0.1 is within Max 0.1 and Min 0.1,
// and denied by DeniedValues {0.1}, as it is allowed by AllowedValues
// {0.1}.
func TestPolicy_Float32PathComparesAtItsPrecision(t *testing.T) {
	schema := newSchema("items", 10, map[string]*patch.PathSchema{
		"/v": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeFloat, GoType: reflect.TypeFor[float32](), SQLColumn: "v"},
	})
	write := func(v any) *patch.ParsedPatch {
		parsed, err := patch.NewParser(schema, nil).ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/v", Value: v, HasValue: true}})
		require.NoError(t, err)
		return parsed
	}
	bound := 0.1
	within := mustPolicy(t, patch.ConstrainValues("/v", patch.ValueConstraint{Min: &bound, Max: &bound}))
	allow := mustPolicy(t, patch.ConstrainValues("/v", patch.ValueConstraint{AllowedValues: []any{0.1}}))
	deny := mustPolicy(t, patch.ConstrainValues("/v", patch.ValueConstraint{DeniedValues: []any{0.1}}))

	require.NoError(t, patch.ApplyPatchPolicyConstraints(write(0.1), within, nil), "0.1 is within [0.1, 0.1]")
	require.NoError(t, patch.ApplyPatchPolicyConstraints(write(0.1), allow, nil), "0.1 is allowed")
	require.Error(t, patch.ApplyPatchPolicyConstraints(write(0.1), deny, nil), "0.1 is denied")
	require.Error(t, patch.ApplyPatchPolicyConstraints(write(0.2), within, nil), "0.2 is above 0.1")
	require.NoError(t, patch.ApplyPatchPolicyConstraints(write(0.2), deny, nil), "0.2 is not denied")
}
