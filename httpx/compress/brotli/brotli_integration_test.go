//go:build integration

package brotli_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/andybalholm/brotli"
	"github.com/ashtonian/pkg/httpx"
	"github.com/ashtonian/pkg/httpx/compress/brotli"
)

func TestBrotliHTTPInteroperability(t *testing.T) {
	payload := strings.Repeat("a compressible HTTP body ", 100)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "br") || r.Header.Get("Content-Encoding") != "br" {
			t.Error("missing brotli negotiation or request encoding")
		}
		body, err := httpx.ReadAtMost(wire.NewReader(r.Body), 1<<20)
		if err != nil {
			t.Error(err)
			http.Error(w, "decode request", 400)
			return
		}
		if string(body) != payload {
			t.Error("upstream decoder received different request bytes")
		}
		w.Header().Set("Content-Encoding", "br")
		writer := wire.NewWriter(w)
		if _, err := writer.Write(body); err != nil {
			t.Error(err)
		}
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	client, err := httpx.NewClient(httpx.WithCompression(), httpx.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	compressor := brotli.New()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			// Share the compressor to exercise pooled-writer ownership under -race.
			var buffer bytes.Buffer
			encoded, err := compressor.Compress([]byte(payload), &buffer)
			if err != nil {
				t.Error(err)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader(encoded))
			if err != nil {
				t.Error(err)
				return
			}
			req.Header.Set("Content-Encoding", "br")
			resp, err := client.Do(req)
			if err != nil {
				t.Error(err)
				return
			}
			defer resp.Body.Close()
			body, err := httpx.ReadAtMost(resp.Body, 1<<20)
			if err != nil || string(body) != payload || !resp.Uncompressed || resp.Header.Get("Content-Encoding") != "" {
				t.Errorf("decoded response: length=%d uncompressed=%v err=%v", len(body), resp.Uncompressed, err)
			}
		})
	}
	wg.Wait()
}

func TestBrotliDecodedBodyLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Type", "application/json")
		writer := wire.NewWriter(w)
		if _, err := writer.Write([]byte(`{"message":"` + strings.Repeat("a", 4096) + `"}`)); err != nil {
			t.Error(err)
		}
		if err := writer.Close(); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	client, err := httpx.NewClient(httpx.WithCompression(), httpx.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	_, resp, err := httpx.Get[map[string]string](context.Background(), client, srv.URL, httpx.WithMaxResponseBytes(100))
	if resp != nil {
		defer resp.Body.Close()
	}
	if !errors.Is(err, httpx.ErrResponseTooLarge) {
		t.Fatalf("decompressed limit: %v", err)
	}
}
