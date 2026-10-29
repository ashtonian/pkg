//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

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
