// Package httpjson encodes a JSON response before writing it, so a value that
// cannot be encoded can still be answered with a status of its own, and
// reports a write that fails once the status is sent.
package httpjson

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

// Encode encodes v as json.Encoder writes it, newline included, so a
// buffered response carries the same bytes a streamed one would.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Send writes body as an application/json response with the given status. The
// status line is sent before the body, so the error it returns is the
// ResponseWriter's, for the caller to report; it cannot be answered.
func Send(w http.ResponseWriter, status int, body []byte) error {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, err := w.Write(body)
	return err
}
