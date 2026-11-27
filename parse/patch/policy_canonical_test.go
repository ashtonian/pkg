//go:build unit

package patch_test

import (
	"reflect"
	"testing"
	"time"

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

// PostgreSQL holds a timestamp column at the microsecond, rounding a finer
// value half to even, so a deny list refuses every write held as a listed
// value there, while an allow list admits only the listed instant.
func TestPolicy_DeniedTimestampRefusesTheMicrosecondPostgreSQLHolds(t *testing.T) {
	schema := newSchema("items", 10, map[string]*patch.PathSchema{
		"/at": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeTimestamp, GoType: reflect.TypeFor[time.Time](), SQLColumn: "at"},
	})
	listed := []any{"2026-01-02T03:04:05Z", time.Date(2026, time.January, 2, 4, 4, 5, 0, time.FixedZone("", 3600))}
	for _, tc := range []struct {
		written any
		denied  bool
	}{
		{"2026-01-02T03:04:05Z", true},
		{"2026-01-02T03:04:05.0000004Z", true},
		{"2026-01-02T03:04:05.0000005Z", true},
		{"2026-01-02T03:04:04.9999996Z", true},
		{time.Date(2026, time.January, 2, 3, 4, 5, 400, time.UTC), true},
		{"2026-01-02T03:04:05.0000006Z", false},
		{"2026-01-02T03:04:04.9999994Z", false},
		{"2026-01-02T03:04:06Z", false},
	} {
		parsed, err := patch.NewParser(schema, nil).ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/at", Value: tc.written, HasValue: true}})
		require.NoError(t, err, "%#v", tc.written)
		for _, l := range listed {
			deny := mustPolicy(t, patch.ConstrainValues("/at", patch.ValueConstraint{DeniedValues: []any{l}}))
			err = patch.ApplyPatchPolicyConstraints(parsed, deny, nil)
			if tc.denied {
				require.Error(t, err, "%#v denied by %#v", tc.written, l)
			} else {
				require.NoError(t, err, "%#v not denied by %#v", tc.written, l)
			}
		}
	}
	allow := mustPolicy(t, patch.ConstrainValues("/at", patch.ValueConstraint{AllowedValues: listed[:1]}))
	parsed, err := patch.NewParser(schema, nil).ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/at", Value: "2026-01-02T03:04:05.0000004Z", HasValue: true}})
	require.NoError(t, err)
	require.Error(t, patch.ApplyPatchPolicyConstraints(parsed, allow, nil), "an allow list admits the listed instant only")
}
