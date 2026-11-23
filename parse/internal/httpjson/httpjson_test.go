//go:build unit

package httpjson

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Buffering changes no byte of a response: json.Encoder's escaping and its
// trailing newline stay.
func TestEncode_WritesWhatAStreamingEncoderWould(t *testing.T) {
	v := map[string]any{"html": "<a href=x>", "n": 1}
	var streamed bytes.Buffer
	require.NoError(t, json.NewEncoder(&streamed).Encode(v))

	body, err := Encode(v)

	require.NoError(t, err)
	assert.Equal(t, streamed.Bytes(), body)
}

func TestSend_WritesTheBodyWithItsLength(t *testing.T) {
	w := httptest.NewRecorder()
	require.NoError(t, Send(w, http.StatusNotFound, []byte("{}\n")))
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))
	assert.Equal(t, "3", w.Header().Get("Content-Length"))
	assert.Equal(t, "{}\n", w.Body.String())
}

type refusingWriter struct{ *httptest.ResponseRecorder }

func (*refusingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestSend_ReturnsTheWriteError(t *testing.T) {
	w := &refusingWriter{httptest.NewRecorder()}
	assert.ErrorIs(t, Send(w, http.StatusOK, []byte("{}\n")), io.ErrClosedPipe)
	assert.Equal(t, http.StatusOK, w.Code)
}
