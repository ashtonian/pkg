package httpx

import (
	"maps"
	"slices"
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

// values returns the registered values ordered by name.
func (r *registry[V]) values() []V {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]V, 0, len(r.m))
	for _, name := range slices.Sorted(maps.Keys(r.m)) {
		out = append(out, r.m[name])
	}
	return out
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

// codecs holds a codec per content type.
var codecs registry[Codec]

// RegisterCodec registers a codec for its content type, replacing any
// earlier registration. Call it from init() in optional modules (e.g.
// codec/msgpack). It panics on a nil codec.
func RegisterCodec(c Codec) {
	if c == nil {
		panic("httpx: RegisterCodec with a nil codec")
	}
	codecs.store(c.ContentType(), c)
}

// GetCodec returns the registered codec for a content type, if any.
func GetCodec(contentType string) (Codec, bool) {
	return codecs.load(contentType)
}

// RegisteredCodecs returns the registered codecs sorted by content type, so
// the Accept header built from them is the same on every request.
func RegisteredCodecs() []Codec {
	return codecs.values()
}

// HasCodec reports whether a codec is registered for the content type.
func HasCodec(contentType string) bool {
	_, ok := codecs.load(contentType)
	return ok
}
