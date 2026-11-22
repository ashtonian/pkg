//go:build unit

package patch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type orderDims struct {
	W int `json:"w"`
	H int `json:"h"`
}

type orderRow struct {
	ID   string     `json:"id" db:"id"`
	Dims orderDims  `json:"dims" db:"dims" parse:"patch"`
	Opt  *orderDims `json:"opt" db:"opt" parse:"patch"`
}

// A typed struct merge refused for more than one member reports the same
// first failure every time, whatever order its maps iterate in.
func TestTypedMergeFailureIsDeterministic(t *testing.T) {
	schema, err := patch.GeneratePatchSchema[orderRow](core.NewSchemaGenerator())
	require.NoError(t, err)
	for _, body := range []string{
		`{"dims":{"w":null,"h":null}}`, // both members the type always emits
		`{"opt":{}}`,                   // a possibly absent struct missing both
	} {
		messages := map[string]bool{}
		for range 200 {
			_, err := patch.ParseMergePatch([]byte(body), schema, nil)
			require.Error(t, err, body)
			messages[err.Error()] = true
		}
		assert.Len(t, messages, 1, "%s: %v", body, messages)
	}
}
