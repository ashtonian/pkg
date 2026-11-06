//go:build integration

package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenClientCacheAndRedirects(t *testing.T) {
	var calls, refreshes atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("redirect leaked token")
		}
		w.WriteHeader(401)
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, other.URL, http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Cache-Control", "max-age=60")
		io.WriteString(w, "accepted")
	}))
	defer server.Close()
	source, err := NewTokenSource("old", time.Time{}, func(context.Context) (string, time.Time, error) { refreshes.Add(1); return "new", time.Time{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	store, err := NewMemoryCache()
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(WithTokenAuth(server.URL, source), WithCache(store))
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	for _, transport := range TransportStack(client) {
		if cache, ok := transport.(*CacheTransport); ok {
			defer cache.Close()
		}
	}
	for range 2 {
		resp, err := client.Get(server.URL + "/items")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || string(body) != "accepted" {
			t.Fatal(resp.StatusCode, string(body), err)
		}
	}
	if calls.Load() != 2 || refreshes.Load() != 1 || store.Len() != 1 {
		t.Fatalf("calls=%d refreshes=%d entries=%d", calls.Load(), refreshes.Load(), store.Len())
	}
	resp, err := client.Get(server.URL + "/redirect")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || refreshes.Load() != 1 {
		t.Fatalf("foreign 401 triggered refresh: %d %d", resp.StatusCode, refreshes.Load())
	}
}
