//go:build integration

package httpx_test

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

// Adding transport middleware must preserve http.Client policy and the caller's
// custom transport (including its TLS configuration), without mutating the client.
func TestComposeExistingStandardClient(t *testing.T) {
	var resourceAttempts, redirectCalls atomic.Int32
	const body = "standard client response"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom-Transport") != "present" {
			t.Error("request bypassed the supplied transport")
		}
		switch r.URL.Path {
		case "/start":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "demo", Path: "/", Secure: true})
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/resource", http.StatusFound)
		case "/resource":
			cookie, err := r.Cookie("session")
			if err != nil || cookie.Value != "demo" {
				t.Error("standard cookie jar did not supply the session cookie")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			attempt := resourceAttempts.Add(1)
			if attempt <= 2 && r.Header.Get("X-Redirect-Policy") != "checked" {
				t.Error("redirect policy was not applied before retry")
			}
			if attempt == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("ETag", `"revision-1"`)
			if attempt == 3 {
				if r.Header.Get("If-None-Match") != `"revision-1"` {
					t.Error("cache did not issue a conditional request")
				}
				w.Header().Set("Cache-Control", "max-age=60")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Cache-Control", "max-age=0, must-revalidate")
			writer := gzip.NewWriter(w)
			if _, err := io.WriteString(writer, body); err != nil {
				t.Error(err)
			}
			if err := writer.Close(); err != nil {
				t.Error(err)
			}
		case "/stop":
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, "/blocked", http.StatusFound)
		case "/blocked":
			t.Error("middleware bypassed the client's redirect refusal")
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	custom := &compositionTransport{base: srv.Client().Transport}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	existing := &http.Client{
		Transport: custom,
		Jar:       jar,
		Timeout:   5 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirectCalls.Add(1)
			if req.URL.Path == "/blocked" {
				return http.ErrUseLastResponse
			}
			req.Header.Set("X-Redirect-Policy", "checked")
			return nil
		},
	}
	transport, err := httpx.NewChain(
		httpx.WithCompressionT(),
		httpx.WithRetryT(httpx.WithMaxAttempts(3), httpx.WithBackoff(time.Millisecond, time.Millisecond, 1)),
	).Build(existing.Transport)
	if err != nil {
		t.Fatal(err)
	}
	cache, err := httpx.NewCacheTransport(transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := *existing // Retain Timeout, CheckRedirect and Jar.
	client.Transport = cache
	defer client.CloseIdleConnections()
	defer cache.Close()

	for i, path := range []string{"/start", "/resource", "/resource"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(resp.Body)
		if err := errors.Join(readErr, resp.Body.Close()); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || string(data) != body || resp.Header.Get("Content-Encoding") != "" {
			t.Fatalf("request %d: status=%d body=%q encoding=%q", i, resp.StatusCode, data, resp.Header.Get("Content-Encoding"))
		}
		if i == 1 && !strings.Contains(resp.Header.Get("Cache-Status"), "fwd-status=304") {
			t.Fatalf("second request did not merge the 304: %s", resp.Header.Get("Cache-Status"))
		}
		if i == 2 && !strings.Contains(resp.Header.Get("Cache-Status"), "; hit") {
			t.Fatalf("third request did not use the refreshed cache entry: %s", resp.Header.Get("Cache-Status"))
		}
	}
	if custom.calls.Load() != 4 || resourceAttempts.Load() != 3 || redirectCalls.Load() != 1 {
		t.Fatalf("transport calls=%d resource attempts=%d redirects=%d", custom.calls.Load(), resourceAttempts.Load(), redirectCalls.Load())
	}
	if existing.Transport != custom || client.Jar != jar || client.Timeout != existing.Timeout {
		t.Fatal("composition replaced client policy or mutated the original client")
	}
	resp, err := client.Get(srv.URL + "/stop")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || redirectCalls.Load() != 2 {
		t.Fatalf("redirect refusal lost: status=%d redirects=%d", resp.StatusCode, redirectCalls.Load())
	}
}

func TestComposePreservesClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	existing := &http.Client{Transport: srv.Client().Transport, Timeout: 50 * time.Millisecond}
	client := *existing
	transport, err := httpx.NewChain(
		httpx.WithCompressionT(), httpx.WithRetryT(httpx.WithMaxAttempts(3)),
	).Build(existing.Transport)
	if err != nil {
		t.Fatal(err)
	}
	client.Transport = transport
	defer client.CloseIdleConnections()
	_, err = client.Get(srv.URL)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("client timeout was lost: %v", err)
	}
}

type compositionTransport struct {
	base  http.RoundTripper
	calls atomic.Int32
}

func (t *compositionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Custom-Transport", "present")
	return t.base.RoundTrip(clone)
}

func (t *compositionTransport) CloseIdleConnections() {
	if transport, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		transport.CloseIdleConnections()
	}
}
