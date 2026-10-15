package httpx

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
)

func init() {
	// Register stdlib codecs by default
	RegisterCodec(JSON)
	RegisterCodec(XML)
}

// Built-in codec singletons (stdlib only).
var (
	JSON Codec = jsonCodec{}
	XML  Codec = xmlCodec{}
)

// jsonCodec is the default JSON codec.
type jsonCodec struct{}

func (jsonCodec) ContentType() string             { return ContentTypeJSON }
func (jsonCodec) Encode(v any) ([]byte, error)    { return json.Marshal(v) }
func (jsonCodec) Decode(data []byte, v any) error { return json.Unmarshal(data, v) }

// xmlCodec is the default XML codec.
type xmlCodec struct{}

func (xmlCodec) ContentType() string             { return ContentTypeXML }
func (xmlCodec) Encode(v any) ([]byte, error)    { return xml.Marshal(v) }
func (xmlCodec) Decode(data []byte, v any) error { return xml.Unmarshal(data, v) }

// EncodeRequest encodes v into req's body, replacing any body req had: it
// closes the old one and sets Body, GetBody and ContentLength together, so a
// retry or a 307/308 redirect replays the new payload.
func EncodeRequest(req *http.Request, codec Codec, v any) error {
	data, err := codec.Encode(v)
	if err != nil {
		return err
	}
	if req.Body != nil {
		// The replaced body is discarded unread; its close error says
		// nothing about the request being built.
		_ = req.Body.Close()
	}
	req.ContentLength = int64(len(data))
	req.GetBody = func() (io.ReadCloser, error) {
		if len(data) == 0 {
			return http.NoBody, nil
		}
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	req.Body, _ = req.GetBody()
	req.Header.Set("Content-Type", codec.ContentType())
	return nil
}

// DecodeResponse reads resp.Body up to WithMaxResponseBytes (default
// DefaultMaxResponseBytes) and decodes it into v. Afterwards resp.Body holds
// the bytes read. A larger body fails with ErrResponseTooLarge and resp.Body
// still reads the whole body; the caller must close it.
func DecodeResponse(resp *http.Response, codec Codec, v any, opts ...DecodeOption) error {
	cfg, err := newDecodeConfig(opts)
	if err != nil {
		return err
	}
	data, err := readBody(resp, cfg.maxBytes)
	if err != nil {
		return err
	}
	return codec.Decode(data, v)
}
