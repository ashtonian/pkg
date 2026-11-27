//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/internal/patchtest"
	"github.com/ashtonian/pkg/parse/patch"
)

// Removing a struct member encoding/json writes whatever it holds leaves a
// document the type never writes, which SQL would store and Apply would
// not: JSON Patch refuses it, as a merge patch does. A member the type
// omits when zero is removed.
func TestParse_RefusesRemovingAMemberTheTypeWrites(t *testing.T) {
	gen := core.NewSchemaGenerator()
	schema := patchtest.MemberPatchSchema(t, gen)
	parser := patch.NewParser(schema, nil)
	_, err := parser.Parse([]byte(`[{"op":"remove","path":"/dims/w"}]`))
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
	_, err = patch.ParseMergePatch([]byte(`{"dims":{"w":null}}`), schema, nil)
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
	for _, member := range []string{"note", "at"} {
		_, err = parser.Parse([]byte(`[{"op":"remove","path":"/dims/` + member + `"}]`))
		require.NoError(t, err, member)
	}
}

// A ParsedPatch no parser produced is refused, never applied or compiled.
func TestApplier_RefusesUnparsedPatches(t *testing.T) {
	gen := core.NewSchemaGenerator()
	applier, err := patch.NewApplier[patchtest.ApplyRow](gen, nil)
	require.NoError(t, err)
	byHand := []*patch.ParsedPatch{
		nil,
		{Operations: []*patch.PatchOperation{{Op: patch.PatchOpReplace, Path: "/name", Value: "x"}}},
	}
	for _, p := range byHand {
		_, err := applier.Apply(p, patchtest.ApplyRow{})
		require.ErrorIs(t, err, patch.ErrUnparsed, "Apply: %v", err)
		_, err = patch.BuildPatchSQL(p, filter.SQLiteDialect{})
		require.ErrorIs(t, err, patch.ErrUnparsed, "BuildPatchSQL: %v", err)
		err = patch.ApplyPatchPolicyConstraints(p, nil, nil)
		require.ErrorIs(t, err, patch.ErrUnparsed, "ApplyPatchPolicyConstraints: %v", err)
	}
}
