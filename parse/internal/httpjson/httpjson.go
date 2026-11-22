// Package httpjson writes JSON responses whole: encoded before the status is
// sent, so a value that cannot be encoded can still be answered with a status
// of its own, and sent with its Content-Length.
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

// ErrorBody is the HTTP error body parse answers with, as the TS SDK's
// ApiError reads it; parsehttp exports it as Body.
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// EncodeError returns ErrorBody{code, message} encoded: the answer for a
// response whose own body cannot be encoded.
func EncodeError(code, message string) []byte {
	body, err := Encode(ErrorBody{Code: code, Message: message})
	if err != nil {
		// Two strings always encode; reaching here is a defect in encoding/json.
		panic("httpjson: encoding an error body: " + err.Error())
	}
	return body
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
