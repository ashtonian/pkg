package cursor

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"math"

	"github.com/vmihailenco/msgpack/v5"
)

// envelope is the wire form of a cursor: a MessagePack map holding the
// format generation and the caller's payload, and nothing else. Encode writes
// it through these tags and readEnvelope reads the same two members. The
// envelope is the same in every generation: a format change is the payload's.
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
// another generation is [ErrCursorGeneration], whatever its payload holds.
// Decode never panics.
//
// The whole token's MessagePack structure is checked first: a declared
// container or payload length longer than the input that remains, or a map
// that names a key twice, is ErrInvalidCursor, so decoding allocates at most
// in proportion to the cursor's size. Bound the cursor's length before
// calling.
//
// The envelope must be a map holding the payload, at most a generation (an
// integer below 2^32), and nothing else. The generation is compared before
// the payload is decoded. Then no struct in the payload may carry a member its
// Go type does not declare: the generation is the format's version, and an
// encoder of this generation writes nothing else, so a caller that adds,
// removes or retypes a payload member must mint a new generation.
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
	env, err := readEnvelope(data)
	if err != nil {
		return zero, fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	if cfg.checkGeneration && env.generation != cfg.generation {
		return zero, ErrCursorGeneration
	}
	return decodeGuarded[T](env.payload)
}

// rawEnvelope is an envelope whose payload is still encoded.
type rawEnvelope struct {
	generation uint32
	payload    []byte
}

var errGeneration = errors.New("the generation is not an integer from 0 to 2^32-1")

// readEnvelope walks the whole token, as boundsWalker checks every value, and
// splits its envelope into the generation and the payload's encoding, without
// decoding the payload. A member name is read as msgpack reads a struct's
// (memberName).
func readEnvelope(data []byte) (rawEnvelope, error) {
	var env rawEnvelope
	w := boundsWalker{data: data}
	n, err := w.mapHeader(0)
	if errors.Is(err, errNotMap) {
		return env, errors.New("the envelope is not a map")
	}
	if err != nil {
		return env, err
	}
	var hasGeneration, hasPayload bool
	for range n {
		keyStart := w.pos
		err = w.value(1)
		if err != nil {
			return env, err
		}
		key := data[keyStart:w.pos]
		valueStart := w.pos
		err = w.value(1)
		if err != nil {
			return env, err
		}
		value := data[valueStart:w.pos]

		name, ok := memberName(key)
		switch {
		case !ok:
			return env, fmt.Errorf("the envelope has a key of MessagePack type 0x%02x", key[0])
		case string(name) == "g" && !hasGeneration:
			env.generation, err = readGeneration(value)
			if err != nil {
				return env, err
			}
			hasGeneration = true
		case string(name) == "p" && !hasPayload:
			env.payload = value
			hasPayload = true
		case string(name) == "g", string(name) == "p":
			return env, fmt.Errorf("the envelope names %q twice", name)
		default:
			return env, fmt.Errorf("the envelope has no member %q", name)
		}
	}
	err = w.end()
	if err != nil {
		return env, err
	}
	if !hasPayload {
		return env, errors.New("the envelope holds no payload")
	}
	return env, nil
}

// readGeneration reads one encoded integer from 0 to 2^32-1, in any of
// MessagePack's integer encodings. msgpack would wrap a wider or negative
// value into a uint32; a generation that is not one is malformed.
func readGeneration(enc []byte) (uint32, error) {
	c, body := enc[0], enc[1:]
	switch {
	case c <= 0x7f: // positive fixint
		return uint32(c), nil
	case c >= 0xcc && c <= 0xcf: // uint8 to uint64
	case c >= 0xd0 && c <= 0xd3 && body[0]&0x80 == 0: // non-negative int8 to int64
	default:
		return 0, errGeneration
	}
	// The walk left exactly the integer's big-endian bytes in body.
	var v uint64
	for _, b := range body {
		v = v<<8 | uint64(b)
	}
	if v > math.MaxUint32 {
		return 0, errGeneration
	}
	return uint32(v), nil
}

// decodeGuarded is [decodePayload] behind a recover. The structural checks
// close every panic known in the decoder; a reflective third-party decoder
// fed request bytes still must not take the caller down with it.
func decodeGuarded[T any](data []byte) (payload T, err error) {
	defer func() {
		r := recover()
		if r != nil {
			var zero T
			payload, err = zero, fmt.Errorf("%w: msgpack decode: %v", ErrInvalidCursor, r)
		}
	}()
	return decodePayload[T](data)
}

// decodePayload decodes an envelope's payload, refusing a member that no
// struct of T declares.
func decodePayload[T any](data []byte) (T, error) {
	var payload T
	dec := msgpack.GetDecoder()
	dec.Reset(bytes.NewReader(data))
	dec.DisallowUnknownFields(true)
	err := dec.Decode(&payload)
	msgpack.PutDecoder(dec)
	if err != nil {
		var zero T
		return zero, fmt.Errorf("%w: msgpack decode: %w", ErrInvalidCursor, err)
	}
	return payload, nil
}
