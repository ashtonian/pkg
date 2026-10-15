//go:build integration

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ashtonian/pkg/httpx"
)

func TestRunComposesRetryCompressionAndETag(t *testing.T) {
	var attempts atomic.Int32
	const payload = "hello over standard HTTP"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.RequestURI() != "/resource?demo=1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.RequestURI())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Error("compression middleware did not offer gzip")
		}
		switch attempts.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("ETag", `"revision-1"`)
			w.Header().Set("Cache-Control", "max-age=0, must-revalidate")
			writer := gzip.NewWriter(w)
			if _, err := io.WriteString(writer, payload); err != nil {
				t.Error(err)
			}
			if err := writer.Close(); err != nil {
				t.Error(err)
			}
		case 3:
			if got := r.Header.Get("If-None-Match"); got != `"revision-1"` {
				t.Errorf("conditional request has If-None-Match=%q", got)
			}
			w.Header().Set("ETag", `"revision-1"`)
			w.Header().Set("Cache-Control", "max-age=60")
			w.WriteHeader(http.StatusNotModified)
		default:
			t.Error("unexpected extra network request")
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	if err := run(t.Context(), srv.URL+"/resource?demo=1", &out); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 || strings.Count(out.String(), "HTTP 200;") != 2 || strings.Count(out.String(), payload) != 2 {
		t.Fatalf("output=%q attempts=%d", out.String(), attempts.Load())
	}
	if !strings.Contains(out.String(), "fwd-status=304") {
		t.Fatalf("output did not report conditional revalidation: %q", out.String())
	}
}

func TestRunUsesFreshCacheAndPrintsRawBytes(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "max-age=60")
		_, _ = io.WriteString(w, "not a typed API response") // fixture; the client observes write failures
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := run(t.Context(), srv.URL, &out); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || !strings.Contains(out.String(), "httpx; hit") || strings.Count(out.String(), "not a typed API response") != 2 {
		t.Fatalf("output=%q requests=%d", out.String(), requests.Load())
	}
}

func TestRunRejectsFailureResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http failure", http.StatusUnauthorized, "no access", "HTTP 401"},
		{"oversized body", http.StatusOK, strings.Repeat("x", (1<<20)+1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body) // fixture; the client observes write failures
			}))
			defer srv.Close()
			var out bytes.Buffer
			err := run(t.Context(), srv.URL, &out)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			if tc.name == "oversized body" && !errors.Is(err, httpx.ErrResponseTooLarge) {
				t.Fatalf("error=%v, want ErrResponseTooLarge", err)
			}
			if out.Len() != 0 {
				t.Fatalf("failure printed a successful response: %q", out.String())
			}
		})
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := run(ctx, "https://api.example.com/resource", io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}
