//go:build integration

package httpx

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompressionPreservesPartialRepresentations(t *testing.T) {
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write([]byte(strings.Repeat("payload", 100))); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	wire := compressed.Bytes()[:10]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept-Encoding"); got != "" {
			t.Errorf("range request advertised compression: %q", got)
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-9/%d", compressed.Len()))
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusPartialContent)
		w.Write(wire)
	}))
	defer srv.Close()
	base := http.DefaultTransport.(*http.Transport).Clone()
	defer base.CloseIdleConnections()
	client := &http.Client{Transport: must(NewCompressionTransport(base))}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || !bytes.Equal(body, wire) || resp.Header.Get("Content-Encoding") != "gzip" || resp.Uncompressed || resp.ContentLength != 10 {
		t.Fatalf("partial representation changed: bytes=%d encoding=%q length=%d uncompressed=%t error=%v", len(body), resp.Header.Get("Content-Encoding"), resp.ContentLength, resp.Uncompressed, err)
	}
}
