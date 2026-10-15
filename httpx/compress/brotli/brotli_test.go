//go:build unit

package brotli

import (
	"bytes"
	"io"
	"testing"

	"github.com/ashtonian/pkg/httpx"
)

func TestBrotliRegistered(t *testing.T) {
	// The init() function should have registered brotli
	if !httpx.HasCompressor(httpx.EncodingBrotli) {
		t.Error("expected brotli to be registered")
	}
}

func TestBrotliCompressor(t *testing.T) {
	c := New()

	data := []byte("test data for brotli compression - repeated content for better compression ratio")
	var buf bytes.Buffer

	compressed, err := c.Compress(data, &buf)
	if err != nil {
		t.Fatalf("compress failed: %v", err)
	}

	if len(compressed) == 0 {
		t.Error("expected non-empty compressed data")
	}

	// Verify decompression
	dr, err := c.Decompress(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("decompress failed: %v", err)
	}

	decompressed, err := io.ReadAll(dr)
	if err != nil {
		t.Fatalf("read decompressed failed: %v", err)
	}

	if !bytes.Equal(data, decompressed) {
		t.Errorf("decompressed data doesn't match original")
	}
}

func TestBrotliWithLevel(t *testing.T) {
	c := NewWithLevel(11) // Maximum compression

	data := []byte("test data for high compression")
	var buf bytes.Buffer

	compressed, err := c.Compress(data, &buf)
	if err != nil {
		t.Fatalf("compress failed: %v", err)
	}

	if len(compressed) == 0 {
		t.Error("expected non-empty compressed data")
	}
}

func TestWithLevelMethod(t *testing.T) {
	c := New()
	c2 := c.WithLevel(6)

	if c2 == nil {
		t.Error("expected non-nil compressor from WithLevel")
	}

	// Ensure it implements httpx.Compressor
	var _ httpx.Compressor = c2
}
