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

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/core"
	"github.com/ashtonian/pkg/parse/filter"
)

func TestWriteError_AnswersAParseErrorWithItsCodesStatus(t *testing.T) {
	pe := core.NewError(filter.ECodeInvalidUUID, "invalid UUID", core.WithField("ref"))
	w, req := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/devices", nil)
	require.True(t, WriteError(w, req, fmt.Errorf("list devices: %w", pe)), "a wrapped parse error is still one")
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
	pe := core.NewError(filter.ECodeValueTypeMismatch, "price out of range",
		core.WithField("price"), core.WithDetail("value", math.NaN()), core.WithDetail("limit", math.Inf(1)))
	w, req := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/devices", nil)
	require.True(t, WriteError(w, req, pe, WithErrorHandler(func(_ *http.Request, err error) { reported = append(reported, err) })))

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
			pe := core.NewError(core.CodeValueTypeMismatch, "m",
				core.WithField("price"), core.WithDetail("v", flaky{calls: &calls, fail: tc.fail}))
			w, req := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/devices", nil)
			require.True(t, WriteError(w, req, pe, WithErrorHandler(func(_ *http.Request, err error) { reported = append(reported, err) })))

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
	w, req := &failingWriter{header: http.Header{}}, httptest.NewRequest(http.MethodGet, "/devices", nil)
	pe := core.NewError(filter.ECodeInvalidUUID, "invalid UUID")
	require.True(t, WriteError(w, req, pe, WithErrorHandler(func(_ *http.Request, err error) { reported = append(reported, err) })))

	assert.Equal(t, http.StatusBadRequest, w.status)
	require.Len(t, reported, 1)
	assert.ErrorIs(t, reported[0], io.ErrClosedPipe)
	assert.NotErrorIs(t, reported[0], ErrUnencodableDetails)

	// Without a hook the failure is dropped; the answer is the same.
	w = &failingWriter{header: http.Header{}}
	require.True(t, WriteError(w, req, pe))
	assert.Equal(t, http.StatusBadRequest, w.status)
}

func TestWriteError_LeavesOtherErrorsToTheCaller(t *testing.T) {
	w, req := httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/devices", nil)
	assert.False(t, WriteError(w, req, errors.New("connection refused")))
	assert.False(t, WriteError(w, req, nil))
	assert.Equal(t, 200, w.Code, "nothing was written")
	assert.Zero(t, w.Body.Len())
}

type device struct {
	ID       string `json:"id"       db:"id"`
	Category string `json:"category" db:"category" parse:"patch" enum:"a,b"`
	Name     string `json:"name"     db:"name"     parse:"patch"`
}

// A patch error names its member and its operation in the body, so a client
// binds it to a form field or a JSON Patch operation without reading the
// message.
func TestWriteError_AnswersAPatchErrorsPathAndIndex(t *testing.T) {
	devices, err := parse.NewResource[device]("devices")
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPatch, "/devices/d1", nil)

	_, err = devices.ParseMergePatch([]byte(`{"name":"n","category":"nope"}`))
	w := httptest.NewRecorder()
	require.True(t, WriteError(w, req, err))
	var body Body
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "/category", body.Path)

	_, err = devices.ParsePatch([]byte(`[{"op":"replace","path":"/name","value":"n"},{"op":"replace","path":"/category","value":"nope"}]`))
	w = httptest.NewRecorder()
	require.True(t, WriteError(w, req, err))
	body = Body{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, "/category", body.Path)
	require.NotNil(t, body.Index)
	assert.Equal(t, 1, *body.Index)

	_, err = devices.ParsePatch([]byte(`[{"op":"replace","path":"/category","value":"nope"}]`))
	w = httptest.NewRecorder()
	require.True(t, WriteError(w, req, err))
	assert.Contains(t, w.Body.String(), `"index":0`, "the first operation is named too")
}

// A server that marshals a ParseError itself writes the members WriteError
// does: the module has one error body.
func TestWriteError_WritesWhatAParseErrorMarshals(t *testing.T) {
	index := 2
	pe := core.NewError(core.CodePatchValueInvalid, "m",
		core.WithPath("/a/b"), core.WithIndex(index), core.WithCause(errors.New("hidden")))
	marshalled, err := json.Marshal(pe)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	require.True(t, WriteError(w, httptest.NewRequest(http.MethodPatch, "/devices/d1", nil), pe))
	assert.JSONEq(t, string(marshalled), w.Body.String())
	assert.JSONEq(t, `{"code":"E2077","message":"m","path":"/a/b","index":2,"details":{"field":"a"}}`, w.Body.String())
}

// WriteJSON sends what it encoded with its length, returns a value it cannot
// encode with nothing written, and reports a failed write with its request.
func TestWriteJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	w := httptest.NewRecorder()
	require.NoError(t, WriteJSON(w, req, http.StatusCreated, map[string]int{"n": 1}))
	assert.Equal(t, http.StatusCreated, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, strconv.Itoa(w.Body.Len()), w.Header().Get("Content-Length"))
	assert.Equal(t, "{\"n\":1}\n", w.Body.String())

	w = httptest.NewRecorder()
	err := WriteJSON(w, req, http.StatusOK, map[string]float64{"n": math.NaN()})
	var unsupported *json.UnsupportedValueError
	assert.ErrorAs(t, err, &unsupported)
	assert.Zero(t, w.Body.Len())
	assert.Empty(t, w.Header())

	var reported []*http.Request
	fw := &failingWriter{header: http.Header{}}
	require.NoError(t, WriteJSON(fw, req, http.StatusOK, "x", WithErrorHandler(func(r *http.Request, err error) {
		assert.ErrorIs(t, err, io.ErrClosedPipe)
		reported = append(reported, r)
	})))
	assert.Equal(t, http.StatusOK, fw.status)
	assert.Equal(t, []*http.Request{req}, reported)
}
