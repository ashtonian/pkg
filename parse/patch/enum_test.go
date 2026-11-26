//go:build unit

package patch_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

// The filter and patch grammars hold an enum field to the same values, as
// ParseEnumTag gives them: a quoted value keeps its edge spaces, so a value
// one grammar accepts the other accepts too, and one it refuses the other
// refuses.
func TestEnum_FilterAndPatchAgree(t *testing.T) {
	type doc struct {
		Mode string `json:"mode" parse:"filter,patch" enum:"' padded',b"`
	}
	gen := core.NewSchemaGenerator()
	fs, err := filter.GenerateFilterSchema[doc](gen)
	require.NoError(t, err)
	ps, err := patch.GeneratePatchSchema[doc](gen)
	require.NoError(t, err)
	for value, declared := range map[string]bool{" padded": true, "b": true, "padded": false, " b": false} {
		quoted, err := json.Marshal(value)
		require.NoError(t, err)
		_, filterErr := filter.NewFilterParser(fs, nil).Parse(`{"mode":` + string(quoted) + `}`)
		_, patchErr := patch.NewParser(ps, nil).Parse([]byte(`[{"op":"replace","path":"/mode","value":` + string(quoted) + `}]`))
		if declared {
			assert.NoError(t, filterErr, "filter %q", value)
			assert.NoError(t, patchErr, "patch %q", value)
			continue
		}
		require.Error(t, filterErr, "filter %q", value)
		requireCode(t, patchErr, patch.ECodePatchValueInvalid.String())
	}
}

// enum on an array field constrains each element, as on the filter side.
func TestGeneratePatchSchema_EnumOnArrayChecksElements(t *testing.T) {
	type doc struct {
		Kinds []string `json:"kinds" parse:"patch" enum:"a, b"`
	}
	schema, err := patch.GeneratePatchSchema[doc](core.NewSchemaGenerator())
	require.NoError(t, err)
	p := patch.NewParser(schema, nil)
	_, err = p.Parse([]byte(`[{"op":"replace","path":"/kinds","value":["a","b"]}]`))
	assert.NoError(t, err)
	_, err = p.Parse([]byte(`[{"op":"replace","path":"/kinds","value":["a","c"]}]`))
	assert.ErrorContains(t, err, `"c" is not one of: a, b`)
}

// A merge-patch null, or a JSON Patch replace with null, clears a nullable
// enum field: enum bounds values, not their absence.
func TestGeneratePatchSchema_NullClearsANullableEnumField(t *testing.T) {
	type doc struct {
		Mode  *string  `json:"mode"  parse:"patch" enum:"a,b"`
		Kinds []string `json:"kinds" parse:"patch" enum:"a,b"`
	}
	schema, err := patch.GeneratePatchSchema[doc](core.NewSchemaGenerator())
	require.NoError(t, err)
	for _, body := range []string{`{"mode":null}`, `{"kinds":null}`} {
		_, err = patch.ParseMergePatch([]byte(body), schema, nil)
		assert.NoError(t, err, body)
	}
	_, err = patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/mode","value":null}]`))
	assert.NoError(t, err)
	_, err = patch.ParseMergePatch([]byte(`{"mode":"c"}`), schema, nil)
	assert.ErrorContains(t, err, `"c" is not one of: a, b`)
}
