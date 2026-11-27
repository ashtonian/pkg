//go:build unit

package parse_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/patch"
)

type policyDoc struct {
	ID     string         `json:"id" db:"id"`
	Attrs  map[string]any `json:"attrs" db:"attrs" parse:"patch"`
	Status *string        `json:"status" db:"status" parse:"patch"`
	Tags   []string       `json:"tags" db:"tags" parse:"patch"`
}

// Policy reaches a forbidden member, a denied element or a cleared field
// however the request reaches it: through its parent, a whole-array write, or
// a remove carrying a stray value.
func TestPatchPolicy_JudgesWhatIsWritten(t *testing.T) {
	docs, err := parse.NewResource[policyDoc](onSQLite, "docs")
	require.NoError(t, err)

	secret := mustPolicy(t, patch.ForbidPaths("/attrs/secret/**"))
	level := mustPolicy(t, patch.ConstrainValues("/attrs/level", patch.ValueConstraint{AllowedValues: []any{"low", "high"}}))
	tags := mustPolicy(t, patch.ConstrainValues("/tags", patch.ValueConstraint{Pattern: "^[a-z]+$", DeniedValues: []any{"admin"}}))
	status := mustPolicy(t, patch.ConstrainValues("/status", patch.ValueConstraint{AllowedValues: []any{"active", "inactive"}}))
	for _, tc := range []struct {
		name   string
		policy *patch.PatchPolicyConstraints
		merge  bool
		body   string
		code   string
	}{
		{"merge into the parent of a forbidden member", secret, true, `{"attrs":{"secret":"x"}}`, patch.ECodePatchPolicyForbidden.String()},
		{"replace the parent of a forbidden member", secret, false, `[{"op":"replace","path":"/attrs","value":{"secret":"x"}}]`, patch.ECodePatchPolicyForbidden.String()},
		{"merge a member outside allowed values", level, true, `{"attrs":{"level":"root"}}`, patch.ECodePatchPolicyValue.String()},
		{"append a denied element", tags, false, `[{"op":"array_append","path":"/tags","value":"admin"}]`, patch.ECodePatchPolicyValue.String()},
		{"replace an array holding a denied element", tags, false, `[{"op":"replace","path":"/tags","value":["admin","BAD!"]}]`, patch.ECodePatchPolicyValue.String()},
		{"merge an array holding a denied element", tags, true, `{"tags":["admin","BAD!"]}`, patch.ECodePatchPolicyValue.String()},
		{"remove a constrained field", status, false, `[{"op":"remove","path":"/status"}]`, patch.ECodePatchPolicyValue.String()},
		{"remove carrying an allowed value", status, false, `[{"op":"remove","path":"/status","value":"active"}]`, patch.ECodePatchPolicyValue.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.merge {
				_, err = docs.ParseMergePatch([]byte(tc.body), parse.WithPatchPolicy(tc.policy, nil))
			} else {
				_, err = docs.ParsePatch([]byte(tc.body), parse.WithPatchPolicy(tc.policy, nil))
			}
			requireCode(t, err, tc.code)
		})
	}
}

func mustPolicy(t *testing.T, opts ...patch.PolicyOption) *patch.PatchPolicyConstraints {
	t.Helper()
	pc, err := patch.NewPatchPolicyConstraints(opts...)
	require.NoError(t, err)
	return pc
}

// A remove clears the field whatever value the client attached.
func TestPatch_RemoveCarriesNoValue(t *testing.T) {
	docs, err := parse.NewResource[policyDoc](onSQLite, "docs")
	require.NoError(t, err)
	parsed, err := docs.ParsePatch([]byte(`[{"op":"remove","path":"/status","value":"active"}]`))
	require.NoError(t, err)
	assert.Nil(t, parsed.Operations[0].Value)

	plan, err := docs.BuildUpdate(parsed, parse.ByID("1"), parse.Scope{})
	require.NoError(t, err)
	assert.Contains(t, plan.Owner.SQL, `SET "status" = NULL`)
}
