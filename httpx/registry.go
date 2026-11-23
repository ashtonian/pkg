package httpx

import (
	"fmt"
	"maps"
	"mime"
	"slices"
	"strings"
	"sync"
)

// registry is the name-keyed store behind the register-on-import seam:
// optional modules register from init(), requests read concurrently.
type registry[V any] struct {
	mu sync.RWMutex
	m  map[string]V
}

func (r *registry[V]) store(name string, v V) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = make(map[string]V)
	}
	r.m[name] = v
}

func (r *registry[V]) load(name string) (V, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.m[name]
	return v, ok
}

// names returns the registered names in sorted order.
func (r *registry[V]) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Sorted(maps.Keys(r.m))
}

// compressorFactories holds a factory per encoding, so every transport gets
// its own compressor instance.
var compressorFactories registry[func() Compressor]

// RegisterCompressor registers a compressor factory for an encoding,
// replacing any earlier registration. Call it from init() in optional
// modules (e.g. compress/brotli). It panics on a nil factory.
func RegisterCompressor(encoding string, factory func() Compressor) {
	if factory == nil {
		panic("httpx: RegisterCompressor with a nil factory for " + encoding)
	}
	compressorFactories.store(encoding, factory)
}

// GetCompressor returns a new compressor instance for the encoding, if registered.
func GetCompressor(encoding string) (Compressor, bool) {
	factory, ok := compressorFactories.load(encoding)
	if !ok {
		return nil, false
	}
	return factory(), true
}

// RegisteredEncodings returns the registered encoding names, sorted.
func RegisteredEncodings() []string {
	return compressorFactories.names()
}

// HasCompressor reports whether a compressor is registered for the encoding.
func HasCompressor(encoding string) bool {
	_, ok := compressorFactories.load(encoding)
	return ok
}

// codecs holds a codec per media type.
var codecs codecRegistry

// codecRegistry keys codecs by mediaTypeKey, so equivalent spellings of one
// media type share an entry. accept is the Accept value built from its keys,
// rebuilt on registration so requests read it without allocating.
type codecRegistry struct {
	mu     sync.RWMutex
	m      map[string]Codec
	keys   []string // sorted; replaced, never modified, on registration
	accept string
}

func (r *codecRegistry) store(key string, c Codec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = make(map[string]Codec)
	}
	r.m[key] = c
	r.keys = slices.Sorted(maps.Keys(r.m))
	r.accept = strings.Join(r.keys, ", ")
}

// keyList returns a copy of the registered media types, sorted.
func (r *codecRegistry) keyList() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.keys)
}

func (r *codecRegistry) load(key string) (Codec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.m[key]
	return c, ok
}

func (r *codecRegistry) values() []Codec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Codec, 0, len(r.keys))
	for _, key := range r.keys {
		out = append(out, r.m[key])
	}
	return out
}

func (r *codecRegistry) acceptValue() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.accept
}

// RegisterCodec registers a codec for the media type its ContentType names,
// replacing any earlier registration of that media type in any spelling:
// type and subtype match case-insensitively and parameters are ignored. Call
// it from init() in optional modules (e.g. codec/msgpack). It panics on a nil
// codec and on a ContentType that is not a concrete type/subtype.
func RegisterCodec(c Codec) {
	if c == nil {
		panic("httpx: RegisterCodec with a nil codec")
	}
	key, err := codecKey(c)
	if err != nil {
		panic("httpx: RegisterCodec: " + err.Error())
	}
	codecs.store(key, c)
}

// GetCodec returns the codec registered for a media type, if any. The
// lookup follows RegisterCodec: case-insensitive, parameters ignored. A
// malformed value names no codec.
func GetCodec(contentType string) (Codec, bool) {
	key, err := mediaTypeKey(contentType)
	if err != nil {
		return nil, false
	}
	return codecs.load(key)
}

// RegisteredCodecs returns the registered codecs sorted by media type, so
// the Accept header built from them is the same on every request.
func RegisteredCodecs() []Codec {
	return codecs.values()
}

// HasCodec reports whether a codec is registered for the media type.
func HasCodec(contentType string) bool {
	_, ok := GetCodec(contentType)
	return ok
}

// mediaTypeKey returns a media type's type/subtype, lowercase and without
// parameters, the form HTTP compares case-insensitively (RFC 9110 §8.3.1).
// A value mime.ParseMediaType rejects, malformed parameters included, or
// one without a subtype has no key: a header two parsers could read
// differently never selects a decoder.
func mediaTypeKey(value string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return "", err
	}
	if _, subtype, ok := strings.Cut(mediaType, "/"); !ok || subtype == "" {
		return "", fmt.Errorf("media type %q has no subtype", value)
	}
	return mediaType, nil
}

// codecKey returns the media type a codec decodes: a concrete type/subtype,
// so it can be named in Accept and matched against a response Content-Type.
func codecKey(c Codec) (string, error) {
	contentType := c.ContentType()
	key, err := mediaTypeKey(contentType)
	switch {
	case err != nil:
		return "", fmt.Errorf("codec content type %q: %w", contentType, err)
	case strings.Contains(key, "*"):
		return "", fmt.Errorf("codec content type %q is a media range, not a media type", contentType)
	}
	return key, nil
}
