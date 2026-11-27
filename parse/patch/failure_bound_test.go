//go:build unit

package patch_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/patch"
)

type boundedRow struct {
	Tags []string `json:"tags" db:"tags" parse:"patch" enum:"a,b"`
}

// An operation reports its first failures and how many there were, not one
// error per failing element: a body under the size limit can hold
// thousands of elements, and every error is retained and joined into the
// text a handler logs.
func TestParse_BoundsAnOperationsFailures(t *testing.T) {
	const elements = 1000
	gen := core.NewSchemaGenerator()
	schema, err := patch.GeneratePatchSchema[boundedRow](gen)
	require.NoError(t, err)
	value := "[" + strings.Repeat(`"x",`, elements-1) + `"x"]`
	bodies := map[string]func() error{
		"json patch": func() error {
			_, err := patch.NewParser(schema, nil).Parse([]byte(`[{"op":"replace","path":"/tags","value":` + value + `}]`))
			return err
		},
		"merge patch": func() error {
			_, err := patch.ParseMergePatch([]byte(`{"tags":`+value+`}`), schema, nil)
			return err
		},
	}
	for name, parse := range bodies {
		t.Run(name, func(t *testing.T) {
			err := parse()
			require.Error(t, err)
			assert.Equal(t, 10, parseErrorCount(err))
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok)
			assert.Equal(t, patch.ECodePatchValueInvalid.String(), pe.Code)
			assert.Equal(t, elements, pe.Details[patch.DetailFailureCount])
			assert.Less(t, len(err.Error()), 4096)
		})
	}
}

// parseErrorCount counts the ParseErrors err joins, at any depth.
func parseErrorCount(err error) int {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		_, parseError := errors.AsType[*core.ParseError](err)
		if parseError {
			return 1
		}
		return 0
	}
	n := 0
	for _, e := range joined.Unwrap() {
		n += parseErrorCount(e)
	}
	return n
}
