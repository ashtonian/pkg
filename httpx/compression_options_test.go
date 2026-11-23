//go:build unit

package httpx

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// isolateCompressors restores the compressor registry when the test ends.
func isolateCompressors(t *testing.T) {
	t.Helper()
	compressorFactories.mu.Lock()
	saved := maps.Clone(compressorFactories.m)
	compressorFactories.mu.Unlock()
	t.Cleanup(func() {
		compressorFactories.mu.Lock()
		compressorFactories.m = saved
		compressorFactories.mu.Unlock()
	})
}

// countingGzip delegates to gzip and counts Decompress calls.
type countingGzip struct{ decompressed *atomic.Int32 }

func (countingGzip) Compress(data []byte, buf *bytes.Buffer) ([]byte, error) {
	return newGzipCompressor(gzip.DefaultCompression).Compress(data, buf)
}

func (c countingGzip) Decompress(r io.Reader) (io.Reader, error) {
	c.decompressed.Add(1)
	return gzip.NewReader(r)
}

// encodedOrigin answers every request with body encoded as coding, and
// records the Accept-Encoding it received.
func encodedOrigin(t *testing.T, coding, body string, acceptEncoding *string) http.RoundTripper {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	encoded := buf.Bytes()
	return stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		*acceptEncoding = r.Header.Get("Accept-Encoding")
		h := http.Header{"Content-Encoding": {coding}}
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(bytes.NewReader(encoded)), Request: r}, nil
	})
}

func TestCompressionOptionsComposeInAnyOrder(t *testing.T) {
	isolateCompressors(t)
	RegisterCompressor("x-registered", func() Compressor { return newGzipCompressor(gzip.DefaultCompression) })

	custom := func(n *atomic.Int32) CompressionOption { return WithCompressor("gzip", countingGzip{n}) }
	for _, tt := range []struct {
		name       string
		options    func(n *atomic.Int32) [][]CompressionOption // each ordering of the same options
		coding     string
		wantAccept string
		wantCustom bool
	}{
		{
			name: "an overridden built-in",
			options: func(n *atomic.Int32) [][]CompressionOption {
				return [][]CompressionOption{
					{custom(n), WithEncodings("gzip")},
					{WithEncodings("gzip"), custom(n)},
				}
			},
			coding: "gzip", wantAccept: "gzip", wantCustom: true,
		},
		{
			name: "an override in another case",
			options: func(n *atomic.Int32) [][]CompressionOption {
				return [][]CompressionOption{
					{WithCompressor("GZIP", countingGzip{n}), WithEncodings("deflate", "Gzip")},
					{WithEncodings("deflate", "Gzip"), WithCompressor("GZIP", countingGzip{n})},
				}
			},
			coding: "GZip", wantAccept: "deflate, gzip", wantCustom: true,
		},
		{
			name: "a registered optional coding beside an override",
			options: func(n *atomic.Int32) [][]CompressionOption {
				return [][]CompressionOption{
					{custom(n), WithEncodings("x-registered", "gzip")},
					{WithEncodings("x-registered", "gzip"), custom(n)},
				}
			},
			coding: "x-registered", wantAccept: "x-registered, gzip", wantCustom: false,
		},
		{
			name: "an override without WithEncodings",
			options: func(n *atomic.Int32) [][]CompressionOption {
				return [][]CompressionOption{{WithCompressor("x-custom", countingGzip{n})}}
			},
			coding: "x-custom", wantAccept: "x-custom, gzip, deflate", wantCustom: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var n atomic.Int32
			for i, opts := range tt.options(&n) {
				n.Store(0)
				var accept string
				transport, err := NewCompressionTransport(encodedOrigin(t, tt.coding, "payload", &accept), opts...)
				if err != nil {
					t.Fatalf("ordering %d: %v", i, err)
				}
				resp, err := (&http.Client{Transport: transport}).Get(itemEndpoint)
				if err != nil {
					t.Fatalf("ordering %d: %v", i, err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || string(body) != "payload" {
					t.Fatalf("ordering %d: body %q, %v", i, body, err)
				}
				if accept != tt.wantAccept {
					t.Fatalf("ordering %d: Accept-Encoding %q, want %q", i, accept, tt.wantAccept)
				}
				if got := n.Load() == 1; got != tt.wantCustom {
					t.Fatalf("ordering %d: custom decoder used = %v, want %v", i, got, tt.wantCustom)
				}
			}
		})
	}
}

func TestCompressionLevelsSurviveWithEncodings(t *testing.T) {
	for _, opts := range [][]CompressionOption{
		{WithGzipLevel(gzip.BestSpeed), WithDeflateLevel(2), WithEncodings("deflate", "gzip")},
		{WithEncodings("deflate", "gzip"), WithGzipLevel(gzip.BestSpeed), WithDeflateLevel(2)},
	} {
		transport, err := NewCompressionTransport(http.DefaultTransport, opts...)
		if err != nil {
			t.Fatal(err)
		}
		gz, _ := transport.compressors[EncodingGzip].(*gzipCompressor)
		df, _ := transport.compressors[EncodingDeflate].(*deflateCompressor)
		if gz == nil || gz.level != gzip.BestSpeed || df == nil || df.level != 2 {
			t.Fatalf("compressors %v", transport.compressors)
		}
	}

	var n atomic.Int32
	for _, tt := range []struct {
		opts []CompressionOption
		want Compressor
	}{
		{[]CompressionOption{WithGzipLevel(gzip.BestSpeed), WithCompressor("gzip", countingGzip{&n})}, countingGzip{&n}},
		{[]CompressionOption{WithCompressor("gzip", countingGzip{&n}), WithGzipLevel(gzip.BestSpeed)}, nil},
	} {
		transport, err := NewCompressionTransport(http.DefaultTransport, tt.opts...)
		if err != nil {
			t.Fatal(err)
		}
		got := transport.compressors[EncodingGzip]
		if tt.want != nil && got != tt.want {
			t.Fatalf("the later WithCompressor lost: %T", got)
		}
		if gz, ok := got.(*gzipCompressor); tt.want == nil && (!ok || gz.level != gzip.BestSpeed) {
			t.Fatalf("the later WithGzipLevel lost: %T", got)
		}
	}
}

func TestCompressionRejectsUnavailableEncodings(t *testing.T) {
	isolateCompressors(t)
	for _, tt := range []struct {
		name string
		opts []CompressionOption
	}{
		{"unregistered", []CompressionOption{WithEncodings("gzip", "x-missing")}},
		{"empty", []CompressionOption{WithEncodings("gzip", " ")}},
		{"listed twice", []CompressionOption{WithEncodings("gzip", "GZIP")}},
		{"a supplied coding replaced by another list", []CompressionOption{WithEncodings("x-custom"), WithEncodings("x-missing"), WithCompressor("x-custom", countingGzip{new(atomic.Int32)})}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewCompressionTransport(http.DefaultTransport, tt.opts...); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("err = %v, want ErrInvalidConfig", err)
			}
		})
	}
	transport, err := NewCompressionTransport(http.DefaultTransport, WithEncodings())
	if err != nil || len(transport.Encodings) != 0 {
		t.Fatalf("an empty list advertises nothing: %v, %v", transport, err)
	}
	if _, err := transport.Compress([]byte("x"), "GZIP"); err == nil {
		t.Fatal("Compress used gzip, which this transport was neither given nor advertises")
	}
	transport, err = NewCompressionTransport(http.DefaultTransport, WithEncodings("gzip"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Compress([]byte("x"), "GZIP"); err != nil {
		t.Fatalf("Compress compares encodings case-insensitively: %v", err)
	}
	if got := strings.Join(transport.Encodings, ","); got != "gzip" {
		t.Fatalf("Encodings %q", got)
	}
}
