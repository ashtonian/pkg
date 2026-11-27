//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// A schema key and a policy pattern are JSON Pointers. A key without its
// leading slash would be accepted and never match a request path, and a
// policy would hold two keys for one path, one silently deciding.
func TestSchemaAndPolicy_RefuseAKeyWithoutItsSlash(t *testing.T) {
	ps := func() *patch.PathSchema {
		schema := &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "name"}
		return schema
	}
	for _, key := range []string{"name", "attrs/*", "**"} {
		_, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{key: ps()})
		require.Error(t, err, "NewPatchSchema %q", key)
		assert.Contains(t, err.Error(), "must begin with /")

		schema, err := patch.NewPatchSchema("items", nil)
		require.NoError(t, err)
		require.Error(t, schema.AddPath(key, ps()), "AddPath %q", key)

		_, err = patch.NewPatchPolicyConstraints(patch.ForbidPaths(key))
		require.Error(t, err, "ForbidPaths %q", key)
		_, err = patch.NewPatchPolicyConstraints(patch.ConstrainValues(key, patch.ValueConstraint{Pattern: "^a"}))
		require.Error(t, err, "ConstrainValues %q", key)
	}

	schema, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{"/name": ps()})
	require.NoError(t, err)
	_, err = patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/name","value":"x"}]`))
	require.NoError(t, err)
}
