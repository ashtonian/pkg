//go:build unit

package parsehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
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

func TestWriteError_AnswersUnencodableDetailsWithoutThem(t *testing.T) {
	pe := &core.ParseError{
		Code: filter.ECodeInvalidUUID.String(), Message: "price out of range",
		Details: map[string]any{"value": math.NaN()},
	}
	var reported []error
	w := httptest.NewRecorder()
	require.True(t, WriteError(w, pe, WithErrorHandler(func(err error) { reported = append(reported, err) })))
	assert.Equal(t, 400, w.Code, "the code's status is kept")
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	var body Body
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, Body{Code: "E2116", Message: "price out of range"}, body, "the details are dropped, not truncated")
	require.Len(t, reported, 1)
	assert.ErrorIs(t, reported[0], ErrUnencodableDetails)
	var unsupported *json.UnsupportedValueError
	assert.ErrorAs(t, reported[0], &unsupported, "the hook can see what would not encode")
}

func TestWriteError_ReportsAWriteThatFailsAfterTheStatus(t *testing.T) {
	pe := &core.ParseError{Code: filter.ECodeInvalidUUID.String(), Message: "invalid UUID"}
	var reported []error
	w := &failingWriter{header: http.Header{}}
	require.True(t, WriteError(w, pe, WithErrorHandler(func(err error) { reported = append(reported, err) })))
	assert.Equal(t, 400, w.status)
	require.Len(t, reported, 1)
	assert.ErrorContains(t, reported[0], "connection reset")
	assert.NotErrorIs(t, reported[0], ErrUnencodableDetails, "an unencodable answer and a failed write are different failures")
}

// failingWriter refuses the body after the status line, as a dropped
// connection does.
type failingWriter struct {
	header http.Header
	status int
}

func (f *failingWriter) Header() http.Header { return f.header }

func (f *failingWriter) WriteHeader(status int) { f.status = status }

func (f *failingWriter) Write([]byte) (int, error) { return 0, errors.New("connection reset") }
