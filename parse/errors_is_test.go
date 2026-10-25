//go:build unit

package parse_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
	"github.com/ashtonian/pkg/parse/patch"
)

type isEntity struct {
	ID   string `json:"id"   parse:"filter"`
	Name string `json:"name" parse:"filter,patch"`
}

// Code identity survives wrapping alongside cause, path and operation index.
func TestParseError_IsByCode(t *testing.T) {
	p, err := parse.NewResource[isEntity]("is_entities")
	require.NoError(t, err)

	_, err = p.ParseFilter(`{"absent":1}`)
	wrapped := fmt.Errorf("list devices: %w", err)
	assert.ErrorIs(t, wrapped, filter.ECodeUnknownField)
	assert.NotErrorIs(t, wrapped, filter.ECodeInvalidOperator)

	_, err = p.ParsePatch([]byte(`[{"op":"replace","path":"/nope","value":1}]`))
	var pe *core.ParseError
	require.ErrorAs(t, fmt.Errorf("update: %w", err), &pe)
	assert.ErrorIs(t, err, patch.ECodePatchFieldNotFound)
	assert.Equal(t, "/nope", pe.Path)
	require.NotNil(t, pe.Index)
	assert.Equal(t, 0, *pe.Index)

	cause := errors.New("decoder failed")
	coded := &core.ParseError{Code: filter.ECodeInvalidJSON.String(), Cause: cause}
	assert.ErrorIs(t, coded, cause, "the cause stays reachable")
	assert.ErrorIs(t, coded, filter.ECodeInvalidJSON)
	assert.NotErrorIs(t, &core.ParseError{}, &core.ParseError{}, "an empty code matches nothing")
}
