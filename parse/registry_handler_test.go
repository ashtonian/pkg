//go:build unit

package parse_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse"
	"github.com/ashtonian/pkg/parse/filter"
)

// Schema discovery answers with the schema, or the platform error body the
// SDKs read ({code, message, details}).
func TestSchemaHandler(t *testing.T) {
	reg := parse.NewSchemaRegistry()
	require.NoError(t, parse.Register[isEntity](reg))
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/{resource}/schema", reg.SchemaHandler())

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/"+reg.ResourceNames()[0]+"/schema", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
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
}
