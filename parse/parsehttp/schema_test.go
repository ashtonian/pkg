//go:build unit

package parsehttp

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
)

type widget struct {
	ID   string `json:"id"   db:"id"`
	Name string `json:"name" db:"name" parse:"filter"`
}

func widgetRegistry(t *testing.T) *parse.SchemaRegistry {
	t.Helper()
	widgets, err := parse.NewResource[widget](onSQLite, "widgets")
	require.NoError(t, err)
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.RegisterResource(reg, widgets))
	return reg
}

// errorRecorder is a WithErrorHandler hook that keeps what it is handed.
type errorRecorder struct {
	reqs []*http.Request
	errs []error
}

func (e *errorRecorder) handle(r *http.Request, err error) {
	e.reqs = append(e.reqs, r)
	e.errs = append(e.errs, err)
}

// Schema discovery answers with the schema, or the error body the SDKs read;
// neither is a failure to report.
func TestSchemaHandler(t *testing.T) {
	reg := widgetRegistry(t)
	var hook errorRecorder
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/{resource}/schema", SchemaHandler(reg, WithErrorHandler(hook.handle)))
	mux.HandleFunc("GET /api/resources", SchemaHandler(reg, WithErrorHandler(hook.handle)))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/widgets/schema", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
	var schema parse.SchemaResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &schema))
	assert.Contains(t, schema.Filter.Fields, "name")

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope/schema", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	var body Body
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, parse.ECodeSchemaNotFound.String(), body.Code)
	assert.Equal(t, "nope", body.Details["resource"])

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/resources", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"resources": ["widgets"]}`, rec.Body.String())

	assert.Empty(t, hook.errs)
}

func TestSchemaListHandler(t *testing.T) {
	reg := widgetRegistry(t)
	var hook errorRecorder

	rec := httptest.NewRecorder()
	SchemaListHandler(reg, WithErrorHandler(hook.handle)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/schemas", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
	var list struct {
		Schemas []parse.SchemaResponse `json:"schemas"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list.Schemas, 1)
	assert.Equal(t, "widgets", list.Schemas[0].Resource)
	assert.Empty(t, hook.errs)
}

// A body that cannot be written after its status is sent changes nothing
// the client sees; it reaches the hook as the write error, not as E2120.
func TestSchemaHandlers_ReportWriteErrors(t *testing.T) {
	reg := widgetRegistry(t)
	for name, handler := range map[string]func(*parse.SchemaRegistry, ...Option) http.HandlerFunc{
		"schema": SchemaHandler,
		"list":   SchemaListHandler,
	} {
		t.Run(name, func(t *testing.T) {
			var hook errorRecorder
			w := &failingWriter{header: http.Header{}}
			req := httptest.NewRequest(http.MethodGet, "/api/schemas", nil)
			handler(reg, WithErrorHandler(hook.handle)).ServeHTTP(w, req)

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
	w := &failingWriter{header: http.Header{}}
	SchemaHandler(widgetRegistry(t)).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/schemas", nil))
	assert.Equal(t, http.StatusOK, w.status)
}

var errUnencodable = errors.New("unencodable member")

// unencodableSchema stands in for a schema document that cannot be encoded;
// no document SchemaFor builds can fail, so the test reaches the handlers'
// writer directly.
type unencodableSchema struct{}

func (unencodableSchema) MarshalJSON() ([]byte, error) { return nil, errUnencodable }

// A document that fails to encode is answered 500/E2120 before any of it is
// written, and its cause reaches the hook, never the client.
func TestSchemaHandler_EncodingFailure(t *testing.T) {
	var hook errorRecorder
	o := newOptions([]Option{WithErrorHandler(hook.handle)})
	req := httptest.NewRequest(http.MethodGet, "/api/widgets/schema", nil)
	rec := httptest.NewRecorder()

	o.serveSchema(rec, req, http.StatusOK, unencodableSchema{})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
	var body Body
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, Body{Code: "E2120", Message: "schema encoding failed"}, body)

	require.Len(t, hook.errs, 1)
	assert.Same(t, req, hook.reqs[0])
	assert.ErrorIs(t, hook.errs[0], parse.ECodeSchemaEncodingFailed)
	assert.ErrorIs(t, hook.errs[0], errUnencodable)
	var marshalErr *json.MarshalerError
	assert.ErrorAs(t, hook.errs[0], &marshalErr)
}
