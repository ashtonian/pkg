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

// Encode returns v as json.Encoder writes it, newline included, so a
// buffered response carries the bytes a streamed one would.
func Encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ErrorBody returns the error body {code, message}: the answer for a
// response whose own body cannot be encoded.
func ErrorBody(code, message string) []byte {
	body, err := Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, err := w.Write(body)
	return err
}
