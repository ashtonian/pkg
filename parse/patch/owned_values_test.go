//go:build unit

package patch_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type ownedRow struct {
	Meta map[string]any `json:"meta" db:"meta" parse:"patch"`
	Any  any            `json:"any"  db:"any"  parse:"patch"`
	List []any          `json:"list" db:"list" parse:"patch"`
}

// A parsed patch owns its values, and Apply gives each result values of its
// own: a caller changing what it parsed, or a handler changing a result,
// changes nothing the patch, another result or the SQL built from the patch
// holds.
func TestPatch_OwnsItsValues(t *testing.T) {
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[ownedRow](gen)
	require.NoError(t, err)
	applier, err := patch.NewApplier[ownedRow](gen, nil)
	require.NoError(t, err)

	meta := map[string]any{"a": "x"}
	dynamic := map[string]any{"b": []any{"y"}}
	list := []any{map[string]any{"c": "z"}}
	parsed, err := patch.NewParser(schema, nil).ParseOps([]patch.RawPatchOp{
		{Op: "replace", Path: "/meta", Value: meta, HasValue: true},
		{Op: "replace", Path: "/any", Value: dynamic, HasValue: true},
		{Op: "replace", Path: "/list", Value: list, HasValue: true},
	})
	require.NoError(t, err)
	meta["a"] = "changed"
	dynamic["b"].([]any)[0] = "changed"
	list[0].(map[string]any)["c"] = "changed"

	want := ownedRow{
		Meta: map[string]any{"a": "x"},
		Any:  map[string]any{"b": []any{"y"}},
		List: []any{map[string]any{"c": "z"}},
	}
	first, err := applier.Apply(parsed, ownedRow{})
	require.NoError(t, err)
	assert.Equal(t, want, first, "the patch holds what it parsed")

	first.Meta["a"] = "mutated"
	first.Any.(map[string]any)["b"].([]any)[0] = "mutated"
	first.List[0].(map[string]any)["c"] = "mutated"
	second, err := applier.Apply(parsed, ownedRow{})
	require.NoError(t, err)
	assert.Equal(t, want, second, "a result's values are its own")

	res, err := patch.BuildPatchSQL(parsed, filter.SQLiteDialect{})
	require.NoError(t, err)
	args := fmt.Sprint(res.Args)
	assert.NotContains(t, args, "changed")
	assert.NotContains(t, args, "mutated")

	// An array path with no Go type binds a byte slice element as it is.
	untyped := newSchema("rows", 10, map[string]*patch.PathSchema{
		"/raw": {AllowedOps: []patch.PatchOp{patch.PatchOpReplace}, DataType: core.FieldTypeArray, SQLColumn: "raw"},
	})
	blob := []byte("raw")
	parsed, err = patch.NewParser(untyped, nil).ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/raw", Value: []any{blob}, HasValue: true}})
	require.NoError(t, err)
	blob[0] = 'X'
	assert.Equal(t, []any{[]byte("raw")}, parsed.Operations[0].Value)
}

// A Go value JSON cannot hold, a map that holds itself, is refused at parse:
// as a value JSON cannot hold in a JSON Patch, and in a merge patch as an
// object nested past the depth limit.
func TestParse_RefusesACyclicValue(t *testing.T) {
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[ownedRow](gen)
	require.NoError(t, err)
	self := map[string]any{}
	self["self"] = self
	_, err = patch.NewParser(schema, nil).ParseOps([]patch.RawPatchOp{{Op: "replace", Path: "/any", Value: self, HasValue: true}})
	requireCode(t, err, patch.ECodePatchValueInvalid.String())
	_, err = patch.ParseMergePatchMap(map[string]any{"meta": self}, schema, nil)
	requireCode(t, err, patch.ECodePathTooDeep.String())
}

// A merge patch map's members are the patch's own once parsed.
func TestParseMergePatchMap_OwnsItsValues(t *testing.T) {
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[ownedRow](gen)
	require.NoError(t, err)
	list := []any{"x"}
	parsed, err := patch.ParseMergePatchMap(map[string]any{"any": list}, schema, nil)
	require.NoError(t, err)
	list[0] = "changed"
	assert.Equal(t, []any{"x"}, parsed.Operations[0].Value)
}
