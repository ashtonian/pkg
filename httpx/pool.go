package httpx

import (
	"bytes"
	"sync"
)

// BufferPool is a sync.Pool-based buffer pool for reducing allocations.
type BufferPool struct {
	pool sync.Pool
	size int
}

// NewBufferPool creates a buffer pool whose buffers start with size bytes of
// capacity; size must be >= 0.
func NewBufferPool(size int) (*BufferPool, error) {
	if size < 0 {
		return nil, invalidConfig("buffer pool: size must be >= 0, got %d", size)
	}
	return newBufferPool(size), nil
}

func newBufferPool(size int) *BufferPool {
	return &BufferPool{
		size: size,
		pool: sync.Pool{
			New: func() any {
				return bytes.NewBuffer(make([]byte, 0, size))
			},
		},
	}
}

// Get retrieves a buffer from the pool.
// The buffer is reset and ready for use.
func (p *BufferPool) Get() *bytes.Buffer {
	buf := p.pool.Get().(*bytes.Buffer)
	buf.Reset()
	return buf
}

// Put returns a buffer to the pool.
// Buffers that have grown significantly larger than the initial size
// are discarded to avoid holding excessive memory.
func (p *BufferPool) Put(b *bytes.Buffer) {
	if b == nil {
		return
	}
	// Discard buffers that have grown too large (4x initial size)
	if b.Cap() > p.size*4 {
		return
	}
	p.pool.Put(b)
}

// Size returns the initial buffer size for this pool.
func (p *BufferPool) Size() int {
	return p.size
}

// defaultPool is the package-internal default buffer pool used by
// transports when none is configured. 32KB buffers — a sensible default
// for HTTP response bodies.
//
// Unexported per RFC-0145 — consumers either configure their own pool or
// call DefaultBufferPool() to get the package singleton.
var defaultPool = newBufferPool(32 * 1024)

// DefaultBufferPool returns the package's shared default BufferPool. The
// returned pool is the singleton — callers MUST treat it as read-only.
// Consumers that need to mutate (drain, replace) should construct their
// own via NewBufferPool.
func DefaultBufferPool() *BufferPool {
	return defaultPool
}
