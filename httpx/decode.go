package httpx

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strings"
)

// DefaultMaxResponseBytes bounds how much of a response body the decoding
// helpers read.
const DefaultMaxResponseBytes int64 = 10 << 20

// DecodeOption configures the decoding helpers ([Get], [Post], [Put],
// [Patch], [Do], [DecodeResponse] and [DecodeProblem]).
type DecodeOption func(*decodeConfig)

type decodeConfig struct {
	maxBytes          int64
	problemErrors     bool
	problemMediaTypes []string
	selectsCodecs     bool
	responseCodecs    []Codec
	// selected is responseCodecs validated and keyed by media type.
	selected []selectedCodec
}

type selectedCodec struct {
	key   string
	codec Codec
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
	for i, value := range c.problemMediaTypes {
		mediaType, parameters, err := mime.ParseMediaType(value)
		_, subtype, hasSubtype := strings.Cut(mediaType, "/")
		if err != nil || !hasSubtype || subtype == "" || strings.Contains(mediaType, "*") || len(parameters) != 0 || (mediaType != ContentTypeJSON && !strings.HasSuffix(mediaType, "+json")) {
			return c, invalidConfig("decode: problem media type must be a JSON media type without parameters")
		}
		c.problemMediaTypes[i] = mediaType
	}
	if c.selectsCodecs {
		if len(c.responseCodecs) == 0 {
			return c, invalidConfig("decode: WithResponseCodecs needs at least one codec")
		}
		c.selected = make([]selectedCodec, 0, len(c.responseCodecs))
		for _, codec := range c.responseCodecs {
			if codec == nil {
				return c, invalidConfig("decode: WithResponseCodecs with a nil codec")
			}
			key, err := codecKey(codec)
			if err != nil {
				return c, invalidConfig("decode: WithResponseCodecs: %v", err)
			}
			if _, taken := c.codecFor(key); taken {
				return c, invalidConfig("decode: WithResponseCodecs names %s twice", key)
			}
			c.selected = append(c.selected, selectedCodec{key: key, codec: codec})
		}
	}
	return c, nil
}

// WithResponseCodecs selects the codecs a typed request helper may decode a
// successful response with, in the order its automatic Accept lists them.
// A response in another media type returns ErrNoCodec, even when a codec is
// registered for it. Without this option the helpers use every registered
// codec. Repeated options add to the selection. A nil codec, a media type
// named twice in any spelling, or an empty selection is ErrInvalidConfig.
// An explicit Accept header on the request is sent unchanged; the selection
// still decides which responses decode. DecodeResponse and DecodeProblem,
// which are given their decoding policy, ignore it.
func WithResponseCodecs(selection ...Codec) DecodeOption {
	selection = slices.Clone(selection)
	return func(c *decodeConfig) {
		c.selectsCodecs = true
		c.responseCodecs = append(c.responseCodecs, selection...)
	}
}

// codecFor returns the codec this call decodes a response of media type key
// with: the selected one, or without a selection the registered one.
func (c *decodeConfig) codecFor(key string) (Codec, bool) {
	if !c.selectsCodecs {
		return codecs.load(key)
	}
	for _, s := range c.selected {
		if s.key == key {
			return s.codec, true
		}
	}
	return nil, false
}

// accept returns the Accept value a typed helper sends on a request that
// names none: the media types it can decode, then, with problem errors on,
// the problem media types it accepts, each listed once.
func (c *decodeConfig) accept() string {
	if !c.selectsCodecs && !c.problemErrors {
		return codecs.acceptValue()
	}
	var types []string
	if c.selectsCodecs {
		for _, s := range c.selected {
			types = append(types, s.key)
		}
	} else {
		types = codecs.keyList()
	}
	if c.problemErrors {
		for _, problem := range append([]string{ContentTypeProblemJSON}, c.problemMediaTypes...) {
			if !slices.Contains(types, problem) {
				types = append(types, problem)
			}
		}
	}
	return strings.Join(types, ", ")
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
// whole body from the start; the caller must close it. When a read fails it
// closes the body, releasing its connection, and leaves resp.Body replaying
// the bytes received and then failing with the same error, so a prefix
// cannot pass for the whole body.
func readBody(resp *http.Response, limit int64) ([]byte, error) {
	data, complete, err := readUpTo(resp.Body, limit)
	if err != nil {
		_ = resp.Body.Close() // the read already failed; that error is the one to report
		resp.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), errReader{err}))
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

// decodeInto decodes data into a new T through a pointer to it, as
// DecodeResponse(resp, codec, &v) does, so the codec decides what a pointer T
// holds: JSON null leaves it nil, and the protobuf codec allocates the
// message.
func decodeInto[T any](codec Codec, data []byte) (T, error) {
	var result T
	if err := codec.Decode(data, &result); err != nil {
		var zero T
		return zero, err
	}
	return result, nil
}
