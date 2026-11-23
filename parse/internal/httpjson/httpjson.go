// Package httpjson holds parse's JSON media type and response encoding,
// without the transport: parse reads a list body's media type through it,
// and parsehttp encodes every response with it before sending any of it.
package httpjson

import (
	"bytes"
	"encoding/json"
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
