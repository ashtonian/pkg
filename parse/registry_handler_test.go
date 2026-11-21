//go:build unit

package parse_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// errorRecorder is a WithErrorHandler hook that keeps what it is handed.
type errorRecorder struct {
	reqs []*http.Request
	errs []error
}

func (e *errorRecorder) handle(r *http.Request, err error) {
	e.reqs = append(e.reqs, r)
	e.errs = append(e.errs, err)
}

// Schema discovery answers with the schema, or the platform error body the
// SDKs read ({code, message, details}); neither is a failure to report.
func TestSchemaHandler(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.Register[isEntity](reg))
	var hook errorRecorder
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/{resource}/schema", reg.SchemaHandler(parse.WithErrorHandler(hook.handle)))
	mux.HandleFunc("GET /api/resources", reg.SchemaHandler(parse.WithErrorHandler(hook.handle)))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/is_entities/schema", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
	var schema parse.SchemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &schema))
	assert.Contains(t, schema.Filter.Fields, "name")

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope/schema", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var body struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, filter.ECodeSchemaNotFound.String(), body.Code)
	assert.Equal(t, "nope", body.Details["resource"])

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/resources", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"resources": ["is_entities"]}`, rec.Body.String())

	assert.Empty(t, hook.errs)
}

func TestSchemaListHandler(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.Register[isEntity](reg))
	var hook errorRecorder

	rec := httptest.NewRecorder()
	reg.SchemaListHandler(parse.WithErrorHandler(hook.handle)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schemas", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
	var list struct {
		Schemas []parse.SchemaResponse `json:"schemas"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Schemas, 1)
	assert.Equal(t, "is_entities", list.Schemas[0].Resource)
	assert.Empty(t, hook.errs)
}

// failingWriter is a connection the client has closed: the status goes
// out, the body cannot.
type failingWriter struct {
	header http.Header
	status int
}

func (f *failingWriter) Header() http.Header       { return f.header }
func (f *failingWriter) WriteHeader(status int)    { f.status = status }
func (f *failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

// A body that cannot be written after its status is sent changes nothing
// the client sees; it reaches the hook as the write error, not as E2120.
func TestSchemaHandlers_ReportWriteErrors(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.Register[isEntity](reg))
	for name, handler := range map[string]func(...parse.SchemaHandlerOption) http.HandlerFunc{
		"schema": reg.SchemaHandler,
		"list":   reg.SchemaListHandler,
	} {
		t.Run(name, func(t *testing.T) {
			var hook errorRecorder
			w := &failingWriter{header: http.Header{}}
			req := httptest.NewRequest(http.MethodGet, "/api/schemas", nil)
			handler(parse.WithErrorHandler(hook.handle)).ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.status)
			require.Len(t, hook.errs, 1)
			assert.Same(t, req, hook.reqs[0])
			assert.ErrorIs(t, hook.errs[0], io.ErrClosedPipe)
			assert.False(t, errors.Is(hook.errs[0], parse.ECodeSchemaEncodingFailed))
		})
	}
}

// Without a hook a write error is dropped; the handler still answers.
func TestSchemaHandler_WithoutErrorHandler(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.Register[isEntity](reg))
	w := &failingWriter{header: http.Header{}}
	reg.SchemaHandler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/schemas", nil))
	assert.Equal(t, http.StatusOK, w.status)
}
