//go:build integration

package httpx

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompressionTransport_AcceptEncoding(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("Accept-Encoding")
		w.Write([]byte("test"))
	}))
	defer server.Close()

	transport := must(NewCompressionTransport(http.DefaultTransport))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Without brotli imported, should only have gzip, deflate
	if receivedHeader != "gzip, deflate" {
		t.Errorf("expected Accept-Encoding 'gzip, deflate', got '%s'", receivedHeader)
	}
}

func TestCompressionTransport_CustomEncodings(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("Accept-Encoding")
		w.Write([]byte("test"))
	}))
	defer server.Close()

	transport := must(NewCompressionTransport(http.DefaultTransport, WithEncodings("gzip", "deflate")))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if receivedHeader != "gzip, deflate" {
		t.Errorf("expected Accept-Encoding 'gzip, deflate', got '%s'", receivedHeader)
	}
}

func TestCompressionTransport_GzipDecompression(t *testing.T) {
	body := "test response body"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		gw := gzip.NewWriter(&buf)
		gw.Write([]byte(body))
		gw.Close()

		w.Header().Set("Content-Encoding", "gzip")
		w.Write(buf.Bytes())
	}))
	defer server.Close()

	transport := must(NewCompressionTransport(http.DefaultTransport))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	result, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}

	if string(result) != body {
		t.Errorf("expected body '%s', got '%s'", body, string(result))
	}

	// Content-Encoding should be removed after decompression
	if resp.Header.Get("Content-Encoding") != "" {
		t.Error("Content-Encoding header should be removed after decompression")
	}
}

func TestCompressionTransport_NoCompression(t *testing.T) {
	body := "uncompressed body"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer server.Close()

	transport := must(NewCompressionTransport(http.DefaultTransport))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	result, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body failed: %v", err)
	}

	if string(result) != body {
		t.Errorf("expected body '%s', got '%s'", body, string(result))
	}
}

func TestCompressionTransport_PreserveExistingHeader(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("Accept-Encoding")
		w.Write([]byte("test"))
	}))
	defer server.Close()

	transport := must(NewCompressionTransport(http.DefaultTransport))
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	// Should preserve the existing header
	if receivedHeader != "gzip" {
		t.Errorf("expected Accept-Encoding 'gzip', got '%s'", receivedHeader)
	}
}
func TestCompressionTransport_Compress(t *testing.T) {
	transport := must(NewCompressionTransport(http.DefaultTransport))
	data := []byte("test data to compress")

	tests := []struct {
		encoding string
	}{
		{"gzip"},
		{"deflate"},
	}

	for _, tt := range tests {
		t.Run(tt.encoding, func(t *testing.T) {
			compressed, err := transport.Compress(data, tt.encoding)
			if err != nil {
				t.Fatalf("compress failed: %v", err)
			}

			if len(compressed) == 0 {
				t.Error("expected non-empty compressed data")
			}
		})
	}
}

func TestCompressionTransport_Compress_UnsupportedEncoding(t *testing.T) {
	transport := must(NewCompressionTransport(http.DefaultTransport))

	_, err := transport.Compress([]byte("data"), "unknown")
	if err == nil {
		t.Error("expected error for unsupported encoding")
	}
}

func TestWithGzipLevel(t *testing.T) {
	transport := must(NewCompressionTransport(http.DefaultTransport, WithGzipLevel(9)))
	data := []byte("test data for gzip compression")

	// Verify compression works with custom level
	compressed, err := transport.Compress(data, EncodingGzip)
	if err != nil {
		t.Fatalf("compression failed: %v", err)
	}
	if len(compressed) == 0 {
		t.Error("expected non-empty compressed data")
	}
}

func TestWithCompressionPool(t *testing.T) {
	pool := must(NewBufferPool(1024))
	transport := must(NewCompressionTransport(http.DefaultTransport, WithCompressionPool(pool)))
	if transport.Pool != pool {
		t.Error("expected custom pool to be set")
	}
}

func TestRegistry_Compressor(t *testing.T) {
	// Test that gzip and deflate are registered by default
	if !HasCompressor(EncodingGzip) {
		t.Error("expected gzip to be registered")
	}
	if !HasCompressor(EncodingDeflate) {
		t.Error("expected deflate to be registered")
	}

	// Brotli should not be registered unless imported
	// (This test runs in the main package without importing brotli)

	// Test GetCompressor
	c, ok := GetCompressor(EncodingGzip)
	if !ok {
		t.Error("expected to get gzip compressor")
	}
	if c == nil {
		t.Error("expected non-nil compressor")
	}

	// Test RegisteredEncodings
	encodings := RegisteredEncodings()
	if len(encodings) < 2 {
		t.Errorf("expected at least 2 registered encodings, got %d", len(encodings))
	}
}
