//go:build unit || concurrency

package patch_test

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

// A path added after the schema has parsed is matched by the next parse,
// exact or wildcard, and the schema changes only through AddPath.
func TestPatchSchema_AddPathAfterFirstUse(t *testing.T) {
	schema, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{
		"/a": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "a"},
	})
	require.NoError(t, err)
	parser := patch.NewParser(schema, nil)
	_, err = parser.Parse([]byte(`[{"op":"replace","path":"/a","value":"x"}]`))
	require.NoError(t, err)

	require.NoError(t, schema.AddPath("/b/*", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpAdd}, DataType: core.FieldTypeAny}))
	require.NoError(t, schema.AddPath("/c", &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "c"}))
	_, err = parser.Parse([]byte(`[{"op":"add","path":"/b/k","value":1},{"op":"replace","path":"/c","value":"y"}]`))
	assert.NoError(t, err)

	assert.ErrorContains(t, schema.AddPath("/c", &patch.PathSchema{}), `path "/c" is declared twice`)
	var keys []string
	for key := range schema.Paths() {
		keys = append(keys, key)
	}
	assert.Equal(t, []string{"/a", "/b/*", "/c"}, keys)
}

// A schema is checked when it is built: a key's tokens are literals, * or
// **, every path has a schema and every operation is known.
func TestPatchSchema_RefusesBadPaths(t *testing.T) {
	for key, ps := range map[string]*patch.PathSchema{
		"/internal_*": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}},
		"/a":          nil,
		"/b":          {AllowedOps: []patch.PatchOp{"upsert"}},
	} {
		_, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{key: ps})
		assert.Error(t, err, key)
	}
	schema, err := patch.NewPatchSchema("items", nil)
	require.NoError(t, err)
	assert.Error(t, schema.AddPath("/x_*", &patch.PathSchema{}))
}

// Parses and AddPath may run at once: each parse sees one version of the
// paths, and a path added during it is there for the next.
func TestPatchSchema_ConcurrentAddPath(t *testing.T) {
	schema, err := patch.NewPatchSchema("items", map[string]*patch.PathSchema{
		"/a": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeString, SQLColumn: "a"},
	})
	require.NoError(t, err)
	parser := patch.NewParser(schema, nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			key := "/p" + string(rune('a'+i)) + "/*"
			assert.NoError(t, schema.AddPath(key, &patch.PathSchema{AllowedOps: []patch.PatchOp{patch.PatchOpAdd}, DataType: core.FieldTypeAny}))
		})
		wg.Go(func() {
			_, err := parser.Parse([]byte(`[{"op":"replace","path":"/a","value":"x"}]`))
			assert.NoError(t, err)
		})
	}
	wg.Wait()
	_, err = parser.Parse([]byte(`[{"op":"add","path":"/ph/k","value":1}]`))
	assert.NoError(t, err)
}
