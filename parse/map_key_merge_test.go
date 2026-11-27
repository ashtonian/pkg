//go:build unit

package parse_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/internal/parsetest"
)

// A merge patch into a map keyed by an integer or by a type with text
// methods applies as encoding/json decodes the members over the map.
func TestMergePatch_MapKeysDecodeAsEncodingJSON(t *testing.T) {
	r := parse.MustResource[parsetest.KeyedMaps](onSQLite, "keyed_maps")
	for _, tc := range parsetest.KeyedMapMerges() {
		t.Run(tc.Name, func(t *testing.T) {
			parsed, err := r.ParseMergePatch([]byte(tc.Body))
			require.NoError(t, err)
			got, err := r.ApplyValidatedPatch(parsed, parsetest.KeyedMapsRow())
			require.NoError(t, err)
			assert.Equal(t, parsetest.MergedByEncodingJSON(t, parsetest.KeyedMapsRow(), tc.Body), got)
		})
	}
}

func TestMergePatch_MapKeysRefused(t *testing.T) {
	r := parse.MustResource[parsetest.KeyedMaps](onSQLite, "keyed_maps")
	for _, tc := range keyedMapRefusals() {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.ParseMergePatch([]byte(tc.body))
			pe, ok := errors.AsType[*core.ParseError](err)
			require.True(t, ok, "%v is no ParseError", err)
			assert.Equal(t, core.CodePatchValueInvalid.String(), pe.Code)
		})
	}
}
