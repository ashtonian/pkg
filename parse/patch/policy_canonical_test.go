//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/require"

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
				"/v": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: c.Type.Type, SQLColumn: "v"},
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
