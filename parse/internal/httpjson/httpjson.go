// Package httpjson holds the mechanics of parse's JSON responses: a body is
// encoded whole before the status is sent, so a value that cannot be encoded
// can still be answered with a status of its own, then sent with its
// Content-Length. parsehttp answers through it; parse reads its media type.
package httpjson

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

// MediaType is the media type of the JSON parse reads and writes.
const MediaType = "application/json"

// Encode returns v as json.Encoder writes it, newline included, so a
// buffered response carries the bytes a streamed one would.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Send writes body as an application/json response with status and its
// Content-Length, and returns the error of a Write that fails once the status
// is sent. net/http buffers a small response and sends it after the handler
// returns, so a client that disconnects in that window fails no Write.
func Send(w http.ResponseWriter, status int, body []byte) error {
	w.Header().Set("Content-Type", MediaType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, err := w.Write(body)
	return err
}
