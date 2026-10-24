//go:build unit

package parsehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestWriteError_AnswersAParseErrorWithItsCodesStatus(t *testing.T) {
	pe := &core.ParseError{Code: filter.ECodeInvalidUUID.String(), Message: "invalid UUID", Details: map[string]any{"field": "ref"}}
	w := httptest.NewRecorder()
	require.True(t, WriteError(w, fmt.Errorf("list devices: %w", pe)), "a wrapped parse error is still one")
	assert.Equal(t, 400, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var body Body
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, Body{Code: "E2116", Message: "invalid UUID", Details: map[string]any{"field": "ref"}}, body)
}

func TestWriteError_LeavesOtherErrorsToTheCaller(t *testing.T) {
	w := httptest.NewRecorder()
	assert.False(t, WriteError(w, errors.New("connection refused")))
	assert.False(t, WriteError(w, nil))
	assert.Equal(t, 200, w.Code, "nothing was written")
	assert.Zero(t, w.Body.Len())
}
