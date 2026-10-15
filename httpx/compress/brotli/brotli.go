// Package brotli provides brotli compression support for httpx.
//
// Import this package to enable brotli compression:
//
//	import _ "github.com/ashtonian/pkg/httpx/compress/brotli"
//
// This will automatically register brotli as a compression option with httpx.
// Once imported, the CompressionTransport will include "br" in Accept-Encoding
// headers and can decompress brotli-encoded responses.
package brotli

import (
	"bytes"
	"io"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/ashtonian/pkg/httpx"
)

func init() {
	httpx.RegisterCompressor(httpx.EncodingBrotli, func() httpx.Compressor {
		return New()
	})
}

// DefaultLevel is the default brotli compression level (0-11).
// Level 4 provides a good balance between speed and compression ratio.
const DefaultLevel = 4

// Compressor implements httpx.Compressor for brotli.
type Compressor struct {
	level int
	pool  sync.Pool
}

// New creates a new brotli compressor with the default compression level.
func New() *Compressor {
	return NewWithLevel(DefaultLevel)
}

// NewWithLevel creates a new brotli compressor with the specified level (0-11).
// Higher levels provide better compression but are slower.
func NewWithLevel(level int) *Compressor {
	return &Compressor{level: level}
}

// WithLevel returns a new compressor with the specified compression level.
// This implements the interface expected by httpx.WithBrotliLevel.
func (c *Compressor) WithLevel(level int) httpx.Compressor {
	return NewWithLevel(level)
}

// Compress compresses data using brotli.
func (c *Compressor) Compress(data []byte, buf *bytes.Buffer) ([]byte, error) {
	w := c.getWriter(buf)
	defer c.putWriter(w)

	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decompress wraps a reader with brotli decompression.
func (c *Compressor) Decompress(r io.Reader) (io.Reader, error) {
	return brotli.NewReader(r), nil
}

func (c *Compressor) getWriter(buf *bytes.Buffer) *brotli.Writer {
	if w := c.pool.Get(); w != nil {
		bw := w.(*brotli.Writer)
		bw.Reset(buf)
		return bw
	}
	return brotli.NewWriterLevel(buf, c.level)
}

func (c *Compressor) putWriter(w *brotli.Writer) {
	c.pool.Put(w)
}

// Ensure Compressor implements the interface.
var _ httpx.Compressor = (*Compressor)(nil)
