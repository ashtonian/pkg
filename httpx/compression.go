package httpx

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

func init() {
	// Register stdlib compressors by default
	RegisterCompressor(EncodingGzip, func() Compressor {
		return newGzipCompressor(gzip.DefaultCompression)
	})
	RegisterCompressor(EncodingDeflate, func() Compressor {
		return newDeflateCompressor(flate.DefaultCompression)
	})
}

// Compressor handles compression and decompression for a specific encoding.
// Implement this interface to add custom compression algorithms.
type Compressor interface {
	// Compress compresses data, using the provided buffer for output.
	// Returns the compressed bytes (may be a slice of buf's backing array).
	Compress(data []byte, buf *bytes.Buffer) ([]byte, error)

	// Decompress wraps a reader with decompression.
	Decompress(r io.Reader) (io.Reader, error)
}

// CompressionTransport handles automatic compression/decompression.
type CompressionTransport struct {
	base        http.RoundTripper
	Pool        *BufferPool
	Encodings   []string              // Priority order for Accept-Encoding
	compressors map[string]Compressor // Registered compressors by encoding
	configErrs  []error
}

// CompressionOption configures a CompressionTransport.
type CompressionOption func(*CompressionTransport)

// NewCompressionTransport creates a new compression transport.
//
// By default, it accepts all registered encodings in priority order:
// brotli (if registered), gzip, deflate.
// The transport automatically decompresses complete response bodies. Partial
// representations retain their encoding and byte offsets; Range and HEAD
// requests do not receive an automatic Accept-Encoding header.
//
// To enable brotli support, import the compress/brotli submodule:
//
//	import _ "github.com/ashtonian/pkg/httpx/compress/brotli"
func NewCompressionTransport(base http.RoundTripper, opts ...CompressionOption) (*CompressionTransport, error) {
	if base == nil {
		return nil, invalidConfig("compression: base transport is nil")
	}

	t := &CompressionTransport{
		base:        base,
		Pool:        defaultPool,
		compressors: make(map[string]Compressor),
	}

	// Build default encodings list based on what's registered
	// Priority: brotli > gzip > deflate
	var encodings []string
	if HasCompressor(EncodingBrotli) {
		encodings = append(encodings, EncodingBrotli)
	}
	if HasCompressor(EncodingGzip) {
		encodings = append(encodings, EncodingGzip)
	}
	if HasCompressor(EncodingDeflate) {
		encodings = append(encodings, EncodingDeflate)
	}
	t.Encodings = encodings

	// Initialize compressors from registry for default encodings
	for _, enc := range encodings {
		if c, ok := GetCompressor(enc); ok {
			t.compressors[enc] = c
		}
	}

	for _, opt := range opts {
		opt(t)
	}
	if t.Pool == nil {
		t.configErrs = append(t.configErrs, invalidConfig("compression: WithCompressionPool requires a non-nil pool"))
	}
	if err := errors.Join(t.configErrs...); err != nil {
		return nil, err
	}

	return t, nil
}

// WithEncodings sets the accepted encodings in priority order.
// Only encodings that have registered compressors will be used.
func WithEncodings(encodings ...string) CompressionOption {
	return func(t *CompressionTransport) {
		var validEncodings []string
		for _, enc := range encodings {
			if c, ok := GetCompressor(enc); ok {
				t.compressors[enc] = c
				validEncodings = append(validEncodings, enc)
			} else if _, exists := t.compressors[enc]; exists {
				// Already have a custom compressor for this encoding
				validEncodings = append(validEncodings, enc)
			} else {
				t.configErrs = append(t.configErrs, invalidConfig("compression: WithEncodings: no compressor registered for %q", enc))
			}
		}
		t.Encodings = validEncodings
	}
}

// WithCompressor registers a custom compressor for an encoding.
// This can also be used to override built-in compressors.
func WithCompressor(encoding string, c Compressor) CompressionOption {
	return func(t *CompressionTransport) {
		if encoding == "" || c == nil {
			t.configErrs = append(t.configErrs, invalidConfig("compression: WithCompressor requires an encoding and a non-nil compressor"))
			return
		}
		t.compressors[encoding] = c
		// Add to encodings if not already present
		found := false
		for _, enc := range t.Encodings {
			if enc == encoding {
				found = true
				break
			}
		}
		if !found {
			t.Encodings = append([]string{encoding}, t.Encodings...)
		}
	}
}

// WithBrotliLevel sets the brotli compression level (0-11).
// Requires brotli to be registered via import _ "github.com/ashtonian/pkg/httpx/compress/brotli"
func WithBrotliLevel(level int) CompressionOption {
	return func(t *CompressionTransport) {
		if level < 0 || level > 11 {
			t.configErrs = append(t.configErrs, invalidConfig("compression: WithBrotliLevel must be within [0, 11], got %d", level))
			return
		}
		c, ok := GetCompressor(EncodingBrotli)
		if !ok {
			t.configErrs = append(t.configErrs, invalidConfig("compression: WithBrotliLevel: brotli is not registered (import github.com/ashtonian/pkg/httpx/compress/brotli)"))
			return
		}
		lc, ok := c.(interface{ WithLevel(int) Compressor })
		if !ok {
			t.configErrs = append(t.configErrs, invalidConfig("compression: WithBrotliLevel: registered brotli compressor does not support levels"))
			return
		}
		t.compressors[EncodingBrotli] = lc.WithLevel(level)
	}
}

// WithGzipLevel sets the gzip compression level: gzip.HuffmanOnly (-2),
// gzip.DefaultCompression (-1), or 0 through 9.
func WithGzipLevel(level int) CompressionOption {
	return func(t *CompressionTransport) {
		if !validDeflateLevel(level) {
			t.configErrs = append(t.configErrs, invalidConfig("compression: WithGzipLevel must be within [%d, %d], got %d", gzip.HuffmanOnly, gzip.BestCompression, level))
			return
		}
		t.compressors[EncodingGzip] = newGzipCompressor(level)
	}
}

// WithDeflateLevel sets the deflate compression level: flate.HuffmanOnly
// (-2), flate.DefaultCompression (-1), or 0 through 9.
func WithDeflateLevel(level int) CompressionOption {
	return func(t *CompressionTransport) {
		if !validDeflateLevel(level) {
			t.configErrs = append(t.configErrs, invalidConfig("compression: WithDeflateLevel must be within [%d, %d], got %d", flate.HuffmanOnly, flate.BestCompression, level))
			return
		}
		t.compressors[EncodingDeflate] = newDeflateCompressor(level)
	}
}

// validDeflateLevel reports whether level is accepted by compress/flate
// (and therefore by gzip and zlib, which share its level range).
func validDeflateLevel(level int) bool {
	return level >= flate.HuffmanOnly && level <= flate.BestCompression
}

// WithCompressionPool sets the buffer pool.
func WithCompressionPool(pool *BufferPool) CompressionOption {
	return func(t *CompressionTransport) {
		t.Pool = pool
	}
}

// RoundTrip implements http.RoundTripper.
func (t *CompressionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := cloneRequest(req)

	// Negotiate complete representations only; range offsets refer to the
	// encoded bytes, and HEAD carries no body to decode.
	negotiate := clone.Method != http.MethodHead && len(headerValuesFold(clone.Header, "Range")) == 0
	if negotiate && clone.Header.Get("Accept-Encoding") == "" && len(t.Encodings) > 0 {
		clone.Header.Set("Accept-Encoding", strings.Join(t.Encodings, ", "))
	}

	resp, err := t.base.RoundTrip(clone)
	if err != nil {
		return nil, err
	}

	if bodiless(clone, resp) {
		return resp, nil
	}
	if resp.StatusCode == http.StatusPartialContent || resp.Header.Get("Content-Range") != "" {
		return resp, nil // byte ranges refer to the encoded representation
	}
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	c, ok := t.compressors[encoding]
	if !ok {
		return resp, nil // identity, stacked or unknown codings reach the caller as sent
	}

	// Decoding starts on the first Read, as net/http does for gzip: headers
	// return without waiting for body bytes, and a malformed stream fails
	// the read rather than the round trip.
	resp.Body = &decompressReader{body: resp.Body, compressor: c, encoding: encoding}
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Length")
	resp.ContentLength = -1
	resp.Uncompressed = true

	return resp, nil
}

// bodiless reports whether resp carries no body to decode, whatever its
// Content-Encoding says (RFC 9110 §6.4.1).
func bodiless(req *http.Request, resp *http.Response) bool {
	switch {
	case req.Method == http.MethodHead, resp.Body == nil, resp.Body == http.NoBody:
		return true
	case resp.StatusCode == http.StatusNoContent, resp.StatusCode == http.StatusResetContent, resp.StatusCode == http.StatusNotModified:
		return true
	}
	return resp.StatusCode >= 100 && resp.StatusCode < 200
}

// Compress compresses data using the specified encoding.
func (t *CompressionTransport) Compress(data []byte, encoding string) ([]byte, error) {
	c, ok := t.compressors[encoding]
	if !ok {
		return nil, fmt.Errorf("httpx: unsupported encoding: %s", encoding)
	}

	buf := t.Pool.Get()
	defer t.Pool.Put(buf)

	compressed, err := c.Compress(data, buf)
	if err != nil {
		return nil, err
	}

	// Copy result since buffer goes back to pool
	result := make([]byte, len(compressed))
	copy(result, compressed)
	return result, nil
}

// decompressReader decodes body on demand. mu serialises Read and the
// decompressor's Close; Close closes body first so a blocked Read returns.
type decompressReader struct {
	body       io.ReadCloser
	compressor Compressor
	encoding   string

	mu  sync.Mutex
	r   io.Reader // nil until the first Read
	err error
}

func (d *decompressReader) Read(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.r == nil && d.err == nil {
		r, err := d.compressor.Decompress(d.body)
		if err != nil {
			// A failed constructor may return a typed nil reader; keep d.r nil.
			d.err = fmt.Errorf("httpx: decompress %s: %w", d.encoding, err)
		} else {
			d.r = r
		}
	}
	if d.err != nil {
		return 0, d.err
	}
	return d.r.Read(p)
}

func (d *decompressReader) Close() error {
	err := d.body.Close()
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.r.(io.Closer); ok {
		// Releases decoder resources; stream integrity was already reported by Read.
		_ = c.Close()
	}
	return err
}

// gzipCompressor implements Compressor for gzip with writer pooling.
type gzipCompressor struct {
	level int
	pool  sync.Pool
}

func newGzipCompressor(level int) *gzipCompressor {
	return &gzipCompressor{level: level}
}

func (c *gzipCompressor) Compress(data []byte, buf *bytes.Buffer) ([]byte, error) {
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

func (c *gzipCompressor) Decompress(r io.Reader) (io.Reader, error) {
	return gzip.NewReader(r)
}

func (c *gzipCompressor) getWriter(buf *bytes.Buffer) *gzip.Writer {
	if w := c.pool.Get(); w != nil {
		gw := w.(*gzip.Writer)
		gw.Reset(buf)
		return gw
	}
	w, _ := gzip.NewWriterLevel(buf, c.level)
	return w
}

func (c *gzipCompressor) putWriter(w *gzip.Writer) {
	c.pool.Put(w)
}

// deflateCompressor implements the HTTP "deflate" coding: the zlib format
// (RFC 1950) around DEFLATE data (RFC 9110 §8.4.1.2), with writer pooling.
type deflateCompressor struct {
	level int
	pool  sync.Pool
}

func newDeflateCompressor(level int) *deflateCompressor {
	return &deflateCompressor{level: level}
}

func (c *deflateCompressor) Compress(data []byte, buf *bytes.Buffer) ([]byte, error) {
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

// Decompress reads the zlib format. A body without a zlib header is read as
// raw DEFLATE, which some servers send for "deflate"; browsers and curl
// accept both.
func (c *deflateCompressor) Decompress(r io.Reader) (io.Reader, error) {
	br := bufio.NewReader(r)
	if header, err := br.Peek(2); err == nil && isZlibHeader(header) {
		return zlib.NewReader(br)
	}
	return flate.NewReader(br), nil
}

// isZlibHeader reports whether h starts a zlib stream: compression method 8
// and a header checksum that is a multiple of 31 (RFC 1950 §2.2).
func isZlibHeader(h []byte) bool {
	return h[0]&0x0f == 8 && (uint16(h[0])<<8|uint16(h[1]))%31 == 0
}

func (c *deflateCompressor) getWriter(buf *bytes.Buffer) *zlib.Writer {
	if w := c.pool.Get(); w != nil {
		zw := w.(*zlib.Writer)
		zw.Reset(buf)
		return zw
	}
	w, _ := zlib.NewWriterLevel(buf, c.level) // level validated at construction
	return w
}

func (c *deflateCompressor) putWriter(w *zlib.Writer) {
	c.pool.Put(w)
}

// Ensure built-in compressors implement the interface.
var (
	_ Compressor = (*gzipCompressor)(nil)
	_ Compressor = (*deflateCompressor)(nil)
)

// CloseIdleConnections forwards to the wrapped transport.
func (t *CompressionTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *CompressionTransport) Unwrap() http.RoundTripper { return t.base }
