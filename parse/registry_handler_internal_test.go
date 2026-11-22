//go:build unit

package parse

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ashtonian/pkg/parse/internal/httpjson"
)

var errUnencodable = errors.New("unencodable member")

// unencodableSchema stands in for a schema document that cannot be encoded;
// no document SchemaFor builds can fail, so the test reaches the handler's
// writer directly.
type unencodableSchema struct{}

func (unencodableSchema) MarshalJSON() ([]byte, error) { return nil, errUnencodable }

// A document that fails to encode is answered 500/E2120 before any of it is
// written, and its cause reaches the hook, never the client.
func TestSchemaHandler_EncodingFailure(t *testing.T) {
	var reported []error
	var reportedReq *http.Request
	h := newSchemaHandler([]SchemaHandlerOption{WithErrorHandler(func(r *http.Request, err error) {
		reportedReq = r
		reported = append(reported, err)
	})})
	req := httptest.NewRequest(http.MethodGet, "/api/widgets/schema", nil)
	rec := httptest.NewRecorder()

	h.respond(rec, req, http.StatusOK, unencodableSchema{})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, strconv.Itoa(rec.Body.Len()), rec.Header().Get("Content-Length"))
	var body httpjson.ErrorBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, httpjson.ErrorBody{Code: "E2120", Message: "schema encoding failed"}, body)

	require.Len(t, reported, 1)
	assert.Same(t, req, reportedReq)
	assert.ErrorIs(t, reported[0], ECodeSchemaEncodingFailed)
	assert.ErrorIs(t, reported[0], errUnencodable)
	var marshalErr *json.MarshalerError
	assert.ErrorAs(t, reported[0], &marshalErr)
}
