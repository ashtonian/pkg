//go:build unit

package parse_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
)

// A merge patch into a map keyed by an integer or by a type with text
// methods applies as encoding/json decodes the members over the map.
func TestMergePatch_MapKeysDecodeAsEncodingJSON(t *testing.T) {
	r := parse.MustResource[keyedMaps]("keyed_maps")
	for _, tc := range keyedMapMerges {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := r.ParseMergePatch([]byte(tc.body))
			require.NoError(t, err)
			got, err := r.ApplyValidatedPatch(parsed, keyedMapsRow())
			require.NoError(t, err)
			assert.Equal(t, mergedByEncodingJSON(t, keyedMapsRow(), tc.body), got)
		})
	}
}

func TestMergePatch_MapKeysRefused(t *testing.T) {
	r := parse.MustResource[keyedMaps]("keyed_maps")
	for _, tc := range keyedMapRefusals {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.ParseMergePatch([]byte(tc.body))
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
		})
	}
}
