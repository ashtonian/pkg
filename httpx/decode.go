package httpx

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"reflect"
)

// DefaultMaxResponseBytes bounds how much of a response body the decoding
// helpers read.
const DefaultMaxResponseBytes int64 = 10 << 20

// DecodeOption configures the decoding helpers ([Get], [Post], [Put],
// [Patch], [Do] and [DecodeResponse]).
type DecodeOption func(*decodeConfig)

type decodeConfig struct {
	maxBytes int64
}

// WithMaxResponseBytes caps the response body a helper reads before
// decoding. A larger body fails with ErrResponseTooLarge instead of being
// decoded from a prefix. Default DefaultMaxResponseBytes.
func WithMaxResponseBytes(n int64) DecodeOption {
	return func(c *decodeConfig) { c.maxBytes = n }
}

func newDecodeConfig(opts []DecodeOption) (decodeConfig, error) {
	c := decodeConfig{maxBytes: DefaultMaxResponseBytes}
	for _, opt := range opts {
		opt(&c)
	}
	if c.maxBytes < 1 {
		return c, invalidConfig("decode: WithMaxResponseBytes must be >= 1, got %d", c.maxBytes)
	}
	return c, nil
}

// ReadAtMost reads r to its end when it holds at most limit bytes. A larger
// input fails with ErrResponseTooLarge rather than returning a prefix, so a
// truncated representation is never mistaken for a whole one. limit must be
// at least 1.
func ReadAtMost(r io.Reader, limit int64) ([]byte, error) {
	if limit < 1 {
		return nil, invalidConfig("ReadAtMost: limit must be >= 1, got %d", limit)
	}
	data, complete, err := readUpTo(r, limit)
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrResponseTooLarge, limit)
	}
	return data, nil
}

// readBody reads resp.Body up to limit bytes and replaces it with a reader
// over what was read, so the caller can still inspect it. When the body is
// larger, it returns ErrResponseTooLarge and leaves resp.Body reading the
// whole body from the start; the caller must close it.
func readBody(resp *http.Response, limit int64) ([]byte, error) {
	data, complete, err := readUpTo(resp.Body, limit)
	if err != nil {
		_ = resp.Body.Close() // the read already failed; that error is the one to report
		return nil, err
	}
	if !complete {
		resp.Body = prefixedBody{Reader: io.MultiReader(bytes.NewReader(data), resp.Body), Closer: resp.Body}
		return nil, fmt.Errorf("%w: more than %d bytes", ErrResponseTooLarge, limit)
	}
	_ = resp.Body.Close() // fully read; a close error cannot change what was received
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return data, nil
}

// decodeInto decodes data into a new T. When T is a pointer type the
// pointee is allocated and the codec handed a plain pointer to it, so
// Do[*pb.Message] decodes into a *pb.Message rather than a **pb.Message, and
// a defined pointer type (type ItemPtr *Item) decodes into an *Item that is
// then converted to it.
func decodeInto[T any](codec Codec, data []byte) (T, error) {
	var result T
	rt := reflect.TypeFor[T]()
	if rt.Kind() != reflect.Pointer {
		if err := codec.Decode(data, &result); err != nil {
			return result, err
		}
		return result, nil
	}
	target := reflect.New(rt.Elem())
	if err := codec.Decode(data, target.Interface()); err != nil {
		return result, err
	}
	return target.Convert(rt).Interface().(T), nil
}
