//go:build unit

package parsehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	assert.Equal(t, strconv.Itoa(w.Body.Len()), w.Header().Get("Content-Length"))
	var body Body
	require.NoError(t, json.NewDecoder(w.Body).Decode(&body))
	assert.Equal(t, Body{Code: "E2116", Message: "invalid UUID", Details: map[string]any{"field": "ref"}}, body)
}

// A Details value JSON cannot hold (strconv.ParseFloat accepts "NaN") leaves
// the body, never the code or the entries that encode: the client still
// reads which field failed, and the hook learns which entry was dropped.
func TestWriteError_AnswersWithoutDetailsItCannotEncode(t *testing.T) {
	var reported []error
	pe := &core.ParseError{
		Code: filter.ECodeValueTypeMismatch.String(), Message: "price out of range",
		Details: map[string]any{"field": "price", "value": math.NaN(), "limit": math.Inf(1)},
	}
	w := httptest.NewRecorder()
	require.True(t, WriteError(w, pe, WithErrorHandler(func(err error) { reported = append(reported, err) })))

	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Equal(t, strconv.Itoa(w.Body.Len()), w.Header().Get("Content-Length"))
	assert.JSONEq(t, `{"code": "E2073", "message": "price out of range", "details": {"field": "price"}}`, w.Body.String())
	require.Len(t, reported, 1)
	assert.ErrorIs(t, reported[0], ErrUnencodableDetails)
	var unsupported *json.UnsupportedValueError
	assert.ErrorAs(t, reported[0], &unsupported)
	assert.ErrorContains(t, reported[0], `details["limit"]`)
	assert.ErrorContains(t, reported[0], `details["value"]`)
}

// flaky fails its MarshalJSON on the calls fail names, counting from 1.
type flaky struct {
	calls *int
	fail  map[int]bool
}

func (f flaky) MarshalJSON() ([]byte, error) {
	*f.calls++
	if f.fail[*f.calls] {
		return nil, errors.New("flaky marshal")
	}
	return []byte(`"ok"`), nil
}

// A value that fails only sometimes still leaves a report that names a cause,
// and the body never claims details it lost.
func TestWriteError_ReportsDetailsThatFailOnlySometimes(t *testing.T) {
	cases := []struct {
		name     string
		fail     map[int]bool
		wantBody string
		wantErr  string
	}{
		{"fails once, then encodes", map[int]bool{1: true}, `{"code":"E2073","message":"m","details":{"field":"price","v":"ok"}}`, "flaky marshal"},
		{"encodes alone, fails together", map[int]bool{1: true, 3: true}, `{"code":"E2073","message":"m"}`, "every other entry"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var reported []error
			calls := 0
			pe := &core.ParseError{Code: "E2073", Message: "m", Details: map[string]any{"field": "price", "v": flaky{calls: &calls, fail: tc.fail}}}
			w := httptest.NewRecorder()
			require.True(t, WriteError(w, pe, WithErrorHandler(func(err error) { reported = append(reported, err) })))

			assert.JSONEq(t, tc.wantBody, w.Body.String())
			require.Len(t, reported, 1)
			assert.ErrorIs(t, reported[0], ErrUnencodableDetails)
			assert.ErrorContains(t, reported[0], tc.wantErr)
			assert.NotContains(t, reported[0].Error(), "%!w")
		})
	}
}

// failingWriter refuses the body after taking the status, as a writer past
// net/http's buffers does once its client has left.
type failingWriter struct {
	header http.Header
	status int
}

func (f *failingWriter) Header() http.Header       { return f.header }
func (f *failingWriter) WriteHeader(status int)    { f.status = status }
func (f *failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestWriteError_ReportsAFailedWrite(t *testing.T) {
	var reported []error
	w := &failingWriter{header: http.Header{}}
	pe := &core.ParseError{Code: filter.ECodeInvalidUUID.String(), Message: "invalid UUID"}
	require.True(t, WriteError(w, pe, WithErrorHandler(func(err error) { reported = append(reported, err) })))

	assert.Equal(t, http.StatusBadRequest, w.status)
	require.Len(t, reported, 1)
	assert.ErrorIs(t, reported[0], io.ErrClosedPipe)
	assert.NotErrorIs(t, reported[0], ErrUnencodableDetails)

	// Without a hook the failure is dropped; the answer is the same.
	w = &failingWriter{header: http.Header{}}
	require.True(t, WriteError(w, pe))
	assert.Equal(t, http.StatusBadRequest, w.status)
}

func TestWriteError_LeavesOtherErrorsToTheCaller(t *testing.T) {
	w := httptest.NewRecorder()
	assert.False(t, WriteError(w, errors.New("connection refused")))
	assert.False(t, WriteError(w, nil))
	assert.Equal(t, 200, w.Code, "nothing was written")
	assert.Zero(t, w.Body.Len())
}
