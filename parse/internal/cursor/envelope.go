package cursor

import (
	"bytes"
	"encoding/base64"
	"fmt"

	"github.com/vmihailenco/msgpack/v5"
)

// envelope is the wire form of a cursor: a MessagePack map holding the
// format generation and the caller's payload, and nothing else.
type envelope[T any] struct {
	// Generation is the caller's cursor format version. Checked on Decode via
	// [ExpectGeneration]; a mismatch yields [ErrCursorGeneration].
	Generation uint32 `msgpack:"g,omitempty"`

	// Payload is the consumer's resume position.
	Payload T `msgpack:"p"`
}

type encodeConfig struct {
	generation uint32
}

// EncodeOption configures [Encode].
type EncodeOption func(*encodeConfig)

// WithGeneration stamps the cursor's format generation.
func WithGeneration(g uint32) EncodeOption { return func(c *encodeConfig) { c.generation = g } }

// Encode serializes payload into an opaque, URL-safe string via msgpack +
// base64url, wrapped in an envelope carrying the configured generation. The
// result round-trips through query strings without further escaping.
func Encode[T any](payload T, opts ...EncodeOption) (string, error) {
	var cfg encodeConfig
	for _, o := range opts {
		o(&cfg)
	}
	data, err := msgpack.Marshal(envelope[T]{Generation: cfg.generation, Payload: payload})
	if err != nil {
		return "", fmt.Errorf("%w: msgpack encode: %v", ErrInvalidCursor, err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

type decodeConfig struct {
	generation      uint32
	checkGeneration bool
}

// DecodeOption configures [Decode].
type DecodeOption func(*decodeConfig)

// ExpectGeneration rejects a cursor whose generation differs from g with
// [ErrCursorGeneration]. Omit to skip the check.
func ExpectGeneration(g uint32) DecodeOption {
	return func(c *decodeConfig) { c.generation = g; c.checkGeneration = true }
}

// Decode parses an opaque string back into its payload and verifies the
// configured generation. A malformed token is [ErrInvalidCursor]; a token of
// another generation is [ErrCursorGeneration]. Decode never panics.
//
// The MessagePack structure is checked before decoding: a declared container
// or payload length longer than the input that remains, or a map that names
// a key twice, is ErrInvalidCursor, so decoding allocates at most in
// proportion to the cursor's size. Bound the cursor's length before calling.
//
// The envelope must be a map, and no struct in the token may carry a member
// its Go type does not declare: the generation is the format's version, and
// an encoder of this generation writes nothing else.
//
// An empty cursor is caller-semantic (first page vs error), so Decode does not
// special-case it — check for the empty cursor before calling.
func Decode[T any](cur string, opts ...DecodeOption) (T, error) {
	var zero T
	var cfg decodeConfig
	for _, o := range opts {
		o(&cfg)
	}
	data, err := base64.RawURLEncoding.DecodeString(cur)
	if err != nil {
		return zero, fmt.Errorf("%w: base64 decode: %v", ErrInvalidCursor, err)
	}
	env, err := decodeGuarded[T](data)
	if err != nil {
		return zero, err
	}
	if cfg.checkGeneration && env.Generation != cfg.generation {
		return zero, ErrCursorGeneration
	}
	return env.Payload, nil
}

// decodeGuarded is [decodeEnvelope] behind a recover. The structural checks
// close every panic known in the decoder; a reflective third-party decoder
// fed request bytes still must not take the caller down with it.
func decodeGuarded[T any](data []byte) (env envelope[T], err error) {
	defer func() {
		if r := recover(); r != nil {
			env, err = envelope[T]{}, fmt.Errorf("%w: msgpack decode: %v", ErrInvalidCursor, r)
		}
	}()
	return decodeEnvelope[T](data)
}

func decodeEnvelope[T any](data []byte) (envelope[T], error) {
	var env envelope[T]
	if err := checkMsgpackBounds(data); err != nil {
		return env, fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	if !isMap(data[0]) {
		return env, fmt.Errorf("%w: the envelope is not a map", ErrInvalidCursor)
	}
	dec := msgpack.GetDecoder()
	dec.Reset(bytes.NewReader(data))
	dec.DisallowUnknownFields(true)
	err := dec.Decode(&env)
	msgpack.PutDecoder(dec)
	if err != nil {
		return env, fmt.Errorf("%w: msgpack decode: %v", ErrInvalidCursor, err)
	}
	return env, nil
}
