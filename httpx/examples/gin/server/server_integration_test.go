//go:build integration

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

func TestGinCompressionAndConditionalResponses(t *testing.T) {
	srv := httptest.NewServer(newRouter(demoToken))
	defer srv.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	var collectionETag string
	for _, tc := range []struct{ accept, encoding string }{
		{"gzip", "gzip"}, {"gzip;q=0", ""}, {"notgzip", ""}, {"br, gzip;q=0.5", "gzip"}, {"*;q=1, gzip;q=0", ""},
	} {
		t.Run(tc.accept, func(t *testing.T) {
			resp, body := request(t, client, http.MethodGet, srv.URL+"/api/users", nil, map[string]string{"Accept-Encoding": tc.accept})
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Encoding") != tc.encoding {
				t.Fatalf("status/encoding = %d/%q", resp.StatusCode, resp.Header.Get("Content-Encoding"))
			}
			if !strings.Contains(resp.Header.Get("Cache-Control"), "private") {
				t.Fatalf("authenticated collection is not private: %s", resp.Header.Get("Cache-Control"))
			}
			if tc.encoding == "gzip" {
				reader, err := gzip.NewReader(bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				body, err = io.ReadAll(reader)
				if err != nil {
					t.Fatal(err)
				}
				if err := reader.Close(); err != nil {
					t.Fatal(err)
				}
			}
			var users []User
			if err := json.Unmarshal(body, &users); err != nil {
				t.Fatal(err)
			}
			if len(users) != 3 || users[0].ID != 1 {
				t.Fatalf("unexpected collection: %+v", users)
			}
			etag := resp.Header.Get("ETag")
			if !strings.HasPrefix(etag, "W/") {
				t.Fatalf("expected weak ETag, got %q", etag)
			}
			if collectionETag != "" && collectionETag != etag {
				t.Fatal("ETag changed across encodings")
			}
			collectionETag = etag
		})
	}

	resp, body := request(t, client, http.MethodGet, srv.URL+"/api/users", nil, map[string]string{
		"Accept-Encoding": "gzip", "If-None-Match": collectionETag,
	})
	if resp.StatusCode != http.StatusNotModified || len(body) != 0 || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("304 must be bodyless: status=%d length=%d encoding=%q", resp.StatusCode, len(body), resp.Header.Get("Content-Encoding"))
	}
	if resp.Header.Get("ETag") != collectionETag || resp.Header.Get("Cache-Control") == "" {
		t.Fatal("304 omitted validator/freshness metadata")
	}

	resp, body = request(t, client, http.MethodDelete, srv.URL+"/api/users/3", nil, map[string]string{"Accept-Encoding": "gzip"})
	if resp.StatusCode != http.StatusNoContent || len(body) != 0 || resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("204 must be bodyless: status=%d length=%d encoding=%q", resp.StatusCode, len(body), resp.Header.Get("Content-Encoding"))
	}
}

func TestGinHTTPXCacheRetryAndConcurrentCRUD(t *testing.T) {
	srv := httptest.NewServer(newRouter(demoToken))
	defer srv.Close()
	client, err := httpx.NewClient(
		httpx.WithAuth(srv.URL, httpx.NewBearerTokenProvider(demoToken)),
		httpx.WithCompression(), httpx.WithCache(nil),
		httpx.WithRetry(httpx.WithMaxAttempts(3), httpx.WithBackoff(time.Millisecond, time.Millisecond, 1)),
		httpx.WithTimeout(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	user, resp, err := httpx.Get[User](ctx, client, srv.URL+"/api/users/1")
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil || resp.StatusCode != http.StatusOK || user.Name != "Alice" {
		t.Fatalf("initial get: %+v %v", user, err)
	}
	_, resp, err = httpx.Get[User](ctx, client, srv.URL+"/api/users/1")
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil || !strings.Contains(resp.Header.Get("Cache-Status"), "; hit") {
		t.Fatalf("cache hit: %v %v", resp, err)
	}
	resp, _ = request(t, client, http.MethodGet, srv.URL+"/api/users/1", nil, map[string]string{"Cache-Control": "no-cache"})
	if !strings.Contains(resp.Header.Get("Cache-Status"), "fwd-status=304") {
		t.Fatal("conditional revalidation did not return the stored body")
	}
	for _, attempt := range []int{1, 2, 4, 5} {
		resp, body := request(t, client, http.MethodGet, srv.URL+"/api/flaky", nil, nil)
		if resp.StatusCode != http.StatusOK || string(body) != fmt.Sprintf(`{"attempt":%d}`, attempt) {
			t.Fatalf("retry: status=%d body=%s", resp.StatusCode, body)
		}
	}
	// Real requests exercise JSON rendering outside the store lock. The race
	// detector guards the old mutable-pointer leak from Get/List/Update.
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			resp, _ := request(t, client, http.MethodPut, srv.URL+"/api/users/1", strings.NewReader(`{"name":"Updated","email":"updated@example.com"}`), map[string]string{"Content-Type": "application/json"})
			if resp.StatusCode != http.StatusOK {
				t.Errorf("PUT status %d", resp.StatusCode)
			}
		})
		wg.Go(func() {
			resp, _ := request(t, client, http.MethodGet, srv.URL+"/api/users", nil, map[string]string{"Cache-Control": "no-store"})
			if resp.StatusCode != http.StatusOK {
				t.Errorf("GET status %d", resp.StatusCode)
			}
		})
	}
	wg.Wait()
}

func request(t *testing.T, client *http.Client, method, url string, body io.Reader, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+demoToken)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := httpx.ReadAtMost(resp.Body, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}
