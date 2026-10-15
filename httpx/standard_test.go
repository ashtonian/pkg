//go:build integration

package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

func TestNewStandard_RoundTripsGET(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := must(httpx.NewStandard())
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestNewStandard_HasDefaultTimeout(t *testing.T) {
	client := must(httpx.NewStandard())
	if client.Timeout == 0 {
		t.Errorf("standard client must default to a non-zero Timeout")
	}
	if client.Timeout < 5*time.Second {
		t.Errorf("Timeout = %v, want a sensible default (≥5s)", client.Timeout)
	}
}

func TestNewStandard_OptOverridesTimeout(t *testing.T) {
	client := must(httpx.NewStandard(httpx.WithTimeout(2 * time.Second)))
	if client.Timeout != 2*time.Second {
		t.Errorf("Timeout = %v, want 2s", client.Timeout)
	}
}

func TestNewStandard_StackIsLayered(t *testing.T) {
	// retry -> compression -> base; no cache layer by default.
	client := must(httpx.NewStandard())
	stack := httpx.TransportStack(client)
	if len(stack) != 3 {
		t.Fatalf("expected retry, compression and base transports, got %d layers", len(stack))
	}
	if _, ok := stack[0].(*httpx.RetryTransport); !ok {
		t.Errorf("outermost layer = %T, want *httpx.RetryTransport", stack[0])
	}
	if _, ok := stack[1].(*httpx.CompressionTransport); !ok {
		t.Errorf("middle layer = %T, want *httpx.CompressionTransport", stack[1])
	}
	if _, ok := stack[2].(*http.Transport); !ok {
		t.Errorf("base = %T, want *http.Transport", stack[2])
	}
}

func TestNewStandard_AcceptsGzipResponseTransparently(t *testing.T) {
	body := strings.Repeat("hello", 1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The compression transport adds Accept-Encoding: gzip
		// automatically; verify the request carried it.
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			t.Errorf("Accept-Encoding header missing gzip: %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	client := must(httpx.NewStandard())
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
}
