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
