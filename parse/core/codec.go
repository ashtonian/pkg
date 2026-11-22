package core

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
)

// Codec serializes request documents and typed patch round trips.
// StdlibJSON is the default; WithCodec selects another Encode/Decode
// implementation. Cursors use an independent, fixed wire format.
type Codec interface {
	Encode(v any) ([]byte, error)
	Decode(data []byte, v any) error
}

// StdlibJSON is the default Codec: the standard library's JSON, with
// encoding/json's data model (field names, omitempty, durations as
// nanoseconds), so a document reads back the way responses are written.
// Decode is stricter than encoding/json's Unmarshal: a
// duplicate object member name, invalid UTF-8 or data after the value is an
// error, so a request cannot say a field twice and have one value win. It
// preserves numbers exactly: a number landing in an `any` becomes a
// json.Number rather than a float64, so integers beyond ±2^53 reach
// validation intact.
type StdlibJSON struct{}

func (StdlibJSON) Encode(v any) ([]byte, error) { return json.Marshal(v) }

func (StdlibJSON) Decode(data []byte, v any) error {
	return jsonv2.Unmarshal(data, v, requestJSON)
}

// requestJSON is how StdlibJSON decodes: encoding/json's options with
// duplicate names and invalid UTF-8 refused, and numbers in an `any` kept
// as their literal.
var requestJSON = jsonv2.JoinOptions(
	json.DefaultOptionsV1(),
	jsontext.AllowDuplicateNames(false),
	jsontext.AllowInvalidUTF8(false),
	jsonv2.WithUnmarshalers(jsonv2.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *any) error {
		if dec.PeekKind() != '0' {
			return errors.ErrUnsupported
		}
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		*v = json.Number(raw)
		return nil
	})),
)

// defaultCodec is the package-level codec used when no codec is configured
// on a ParserConfig. Unexported — consumers either set
// ParserConfig.Codec explicitly or rely on GetCodec()'s fallback.
var defaultCodec Codec = StdlibJSON{}
