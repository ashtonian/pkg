//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type codecDims struct {
	W int `json:"w"`
}

type codecRow struct {
	ID   string    `json:"id" db:"id"`
	Dims codecDims `json:"dims" db:"dims" parse:"patch"`
}

// A typed document's members are checked on the encoding the codec decodes:
// a member the type does not declare is refused, not silently dropped.
func TestTypedValueRefusesAnUnknownMember(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[codecRow](core.NewSchemaGenerator())
	require.NoError(t, err)
	parser := patch.NewParser(schema, nil)
	_, err = parser.Parse([]byte(`[{"op":"replace","path":"/dims","value":{"w":1}}]`))
	require.NoError(t, err)
	_, err = parser.Parse([]byte(`[{"op":"replace","path":"/dims","value":{"w":1,"h":2}}]`))
	assert.ErrorContains(t, err, `unknown field "h"`)
}
