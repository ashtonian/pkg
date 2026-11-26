//go:build unit

package patch_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// Path and Paths hand out copies: a schema is safe for concurrent use, and
// a caller changing what they return would otherwise change, and race,
// every parse that reads the path. AddPath is how a schema changes.
func TestPatchSchema_PathAndPathsReturnCopies(t *testing.T) {
	schema, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{
		"/name": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "name"},
	})
	require.NoError(t, err)

	ps, ok := schema.Path("/name")
	require.True(t, ok)
	ps.AllowedOps[0] = patch.PatchOpRemove
	ps.SQLColumn = "other"
	for _, p := range schema.Paths() {
		p.AllowedOps = nil
	}

	parsed, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":"x"}]`))
	require.NoError(t, err)
	require.Equal(t, "name", parsed.Operations[0].Schema.SQLColumn)
}

// The schema keeps its own copy of every path schema it is given, so the
// caller changing one afterwards changes no parse.
func TestPatchSchema_KeepsCopiesOfGivenPaths(t *testing.T) {
	given := &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "name"}
	schema, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{"/name": given})
	require.NoError(t, err)
	added := &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "nick"}
	require.NoError(t, schema.AddPath("/nick", added))

	for _, ps := range []*patch.PathSchema{given, added} {
		ps.AllowedOps[0] = patch.PatchOpRemove
		ps.SQLColumn = "other"
	}

	parsed, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":"x"},{"op":"replace","path":"/nick","value":"y"}]`))
	require.NoError(t, err)
	require.Equal(t, "name", parsed.Operations[0].Schema.SQLColumn)
	require.Equal(t, "nick", parsed.Operations[1].Schema.SQLColumn)
}

// UpdatePath changes a declared path for the parses that start after it,
// checked as NewPatchSchema checks one.
func TestPatchSchema_UpdatePath(t *testing.T) {
	schema, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{
		"/name": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "name"},
	})
	require.NoError(t, err)
	refuse := errors.New("refused")
	var kept *patch.PathSchema
	require.NoError(t, schema.UpdatePath("/name", func(ps *patch.PathSchema) {
		ps.Validators = append(ps.Validators, func(any) error { return refuse })
		kept = ps
	}))
	kept.Validators = nil

	_, err = patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":"x"}]`))
	require.ErrorIs(t, err, refuse)

	require.Error(t, schema.UpdatePath("/missing", func(*patch.PathSchema) {}))
	require.Error(t, schema.UpdatePath("/name", func(ps *patch.PathSchema) { ps.AllowedOps = []patch.PatchOp{"bogus"} }))
	ps, _ := schema.Path("/name")
	require.Equal(t, []patch.PatchOp{patch.PatchOpReplace}, ps.AllowedOps, "a refused update changes nothing")
}
