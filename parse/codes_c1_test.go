//go:build unit

package parse_test

import (
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestParseCodesCatalog(t *testing.T) {
	codes := parse.Codes()
	require.NotEmpty(t, codes)
	seen := make(map[parse.Code]bool)
	for _, code := range codes {
		assert.False(t, seen[code], "duplicate code %s", code)
		seen[code] = true
		assert.NotEmpty(t, code.ErrorMessage(), code)
		assert.Contains(t, []int{400, 422, 500}, code.ErrorHTTPStatus(), code)
		assert.True(t, code.IsErrorUserFacing(), code)
	}
	assert.Contains(t, seen, parse.Code("E2061"))
	assert.Contains(t, seen, parse.Code("E2064"))
}

func TestCodeCatalog_MatchesItsWireSnapshot(t *testing.T) {
	type entry struct {
		Code    string `json:"code"`
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	codes := parse.Codes()
	slices.Sort(codes)
	got := make([]entry, len(codes))
	for i, code := range codes {
		got[i] = entry{code.String(), code.ErrorHTTPStatus(), code.ErrorMessage()}
	}
	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	want, err := os.ReadFile("testdata/error_codes.json")
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(encoded))
}

func TestParseError_AnswersItsCodesStatus(t *testing.T) {
	for _, code := range parse.Codes() {
		assert.Equal(t, code.ErrorHTTPStatus(), (&parse.ParseError{Code: code.String()}).ErrorHTTPStatus(), code)
	}
	assert.Equal(t, http.StatusUnprocessableEntity, (&parse.ParseError{Code: "E9999"}).ErrorHTTPStatus())
	assert.Equal(t, http.StatusBadRequest, (&parse.ParseError{Code: filter.ECodeInvalidUUID.String()}).ErrorHTTPStatus())
}
