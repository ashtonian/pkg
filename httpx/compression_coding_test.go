//go:build unit

package httpx

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// encodedResponse returns a transport answering with body under the given
// Content-Encoding.
func encodedResponse(status int, encoding string, body io.ReadCloser) http.RoundTripper {
	return stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		if encoding != "" {
			h.Set("Content-Encoding", encoding)
		}
		return &http.Response{StatusCode: status, Header: h, Body: body, Request: r}, nil
	})
}

func roundTripBody(t *testing.T, rt http.RoundTripper, method string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, "https://api.example.com/x", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

const payload = "the quick brown fox jumps over the lazy dog"

func TestDeflateCodingIsZlib(t *testing.T) {
	var zlibbed bytes.Buffer
	zw := zlib.NewWriter(&zlibbed)
	zw.Write([]byte(payload))
	zw.Close()
	ct := must(NewCompressionTransport(encodedResponse(200, EncodingDeflate, io.NopCloser(&zlibbed))))
	if _, got := roundTripBody(t, ct, http.MethodGet); got != payload {
		t.Fatalf("zlib deflate body decoded to %q", got)
	}

	out, err := ct.Compress([]byte(payload), EncodingDeflate)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zlib.NewReader(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("deflate output must be zlib: %v", err)
	}
	if got, _ := io.ReadAll(zr); string(got) != payload {
		t.Fatalf("stdlib zlib read %q", got)
	}
}

func TestDeflateAcceptsRawDeflate(t *testing.T) {
	var raw bytes.Buffer
	fw, _ := flate.NewWriter(&raw, flate.DefaultCompression)
	fw.Write([]byte(payload))
	fw.Close()
	ct := must(NewCompressionTransport(encodedResponse(200, EncodingDeflate, io.NopCloser(&raw))))
	if _, got := roundTripBody(t, ct, http.MethodGet); got != payload {
		t.Fatalf("raw deflate body decoded to %q", got)
	}
}

func TestCompressionMatchesEncodingCaseInsensitively(t *testing.T) {
	var gz bytes.Buffer
	gw := gzip.NewWriter(&gz)
	gw.Write([]byte(payload))
	gw.Close()
	ct := must(NewCompressionTransport(encodedResponse(200, " GZIP ", io.NopCloser(&gz))))
	resp, got := roundTripBody(t, ct, http.MethodGet)
	if got != payload || resp.Header.Get("Content-Encoding") != "" || !resp.Uncompressed {
		t.Fatalf("body %q, Content-Encoding %q, Uncompressed %v", got, resp.Header.Get("Content-Encoding"), resp.Uncompressed)
	}
}

func TestCompressionPassesBodilessResponses(t *testing.T) {
	tests := []struct {
		name   string
		method string
		status int
	}{
		{"HEAD", http.MethodHead, 200},
		{"204", http.MethodGet, http.StatusNoContent},
		{"205", http.MethodGet, http.StatusResetContent},
		{"304", http.MethodGet, http.StatusNotModified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := must(NewCompressionTransport(encodedResponse(tt.status, EncodingGzip, io.NopCloser(strings.NewReader("")))))
			resp, got := roundTripBody(t, ct, tt.method)
			if got != "" || resp.Header.Get("Content-Encoding") != EncodingGzip {
				t.Fatalf("a bodiless response must pass through untouched: body %q, Content-Encoding %q", got, resp.Header.Get("Content-Encoding"))
			}
		})
	}
}

// watchedBody counts reads.
type watchedBody struct {
	io.Reader
	reads atomic.Int32
}

func (b *watchedBody) Read(p []byte) (int, error) { b.reads.Add(1); return b.Reader.Read(p) }
func (b *watchedBody) Close() error               { return nil }

func TestCompressionDecodesOnFirstRead(t *testing.T) {
	body := &watchedBody{Reader: strings.NewReader("not gzip at all")}
	ct := must(NewCompressionTransport(encodedResponse(200, EncodingGzip, body)))
	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/x", nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatalf("headers must return before the body is decoded, got %v", err)
	}
	defer resp.Body.Close()
	if body.reads.Load() != 0 {
		t.Fatal("RoundTrip read the body")
	}
	if _, err := io.ReadAll(resp.Body); err == nil || !strings.Contains(err.Error(), "decompress gzip") {
		t.Fatalf("a malformed stream fails the read, got %v", err)
	}
}
