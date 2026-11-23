//go:build unit

package httpx

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// versionedOrigin serves the current version of every document and moves to
// the next one on each unsafe request, so a reread shows whether the cache
// served what an earlier write replaced.
func versionedOrigin(location string) *origin {
	var version atomic.Int64
	version.Store(1)
	return &origin{respond: func(_ int, r *http.Request) *http.Response {
		if r.Method != http.MethodGet {
			version.Add(1)
			if location != "" {
				return reply(http.StatusCreated, "", "Location", location)
			}
			return reply(http.StatusNoContent, "")
		}
		return reply(200, "v"+strconv.FormatInt(version.Load(), 10), "Cache-Control", "max-age=600")
	}}
}

func sendUnsafe(t *testing.T, rt http.RoundTripper, method, target string) {
	t.Helper()
	req, _ := http.NewRequest(method, target, strings.NewReader("{}"))
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// A fragment never reaches the origin (RFC 9110 §7.1), so URLs that differ
// only by fragment name one stored response, and a write through any of them
// invalidates it.
func TestCacheKeyOmitsFragment(t *testing.T) {
	const doc = "https://api.example.com/doc"
	tests := []struct {
		name       string
		write      string // unsafe request target
		location   string // its response's Location
		wantOrigin int    // after three reads, the write and two rereads
	}{
		{"write without fragment", doc, "", 3},
		{"write with fragment", doc + "#details", "", 3},
		{"same-origin Location with fragment", "https://api.example.com/docs", "/doc#created", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := versionedOrigin(tt.location)
			ct := newCache(t, o)
			for _, target := range []string{doc + "#summary", doc + "#details", doc} {
				if _, body, _ := get(t, ct, target); body != "v1" {
					t.Fatalf("GET %s = %q, want v1", target, body)
				}
			}
			if o.count() != 1 {
				t.Errorf("three reads of one resource reached the origin %d times, want 1", o.count())
			}

			sendUnsafe(t, ct, http.MethodPut, tt.write)
			for _, target := range []string{doc + "#summary", doc} {
				if _, body, _ := get(t, ct, target); body != "v2" {
					t.Errorf("GET %s after the write = %q, want v2", target, body)
				}
			}
			if o.count() != tt.wantOrigin {
				t.Errorf("origin requests = %d, want %d: one refetch serves every fragment", o.count(), tt.wantOrigin)
			}
		})
	}
}

// Dropping the fragment leaves every part of the target the origin sees in
// the key: escaped and unescaped paths, and distinct queries, stay apart.
func TestCacheKeyKeepsTargetDistinctions(t *testing.T) {
	o := &origin{respond: func(_ int, r *http.Request) *http.Response {
		return reply(200, r.URL.RequestURI(), "Cache-Control", "max-age=600")
	}}
	ct := newCache(t, o)
	targets := []string{
		"https://api.example.com/a/b",
		"https://api.example.com/a%2Fb",
		"https://api.example.com/a/b?q=1",
		"https://api.example.com/a/b?q=2",
		"https://api.example.com/a/b?q=1#frag",
	}
	for _, target := range targets {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if _, body, _ := get(t, ct, target); body != req.URL.RequestURI() {
			t.Errorf("GET %s = %q, want %q", target, body, req.URL.RequestURI())
		}
	}
	if o.count() != 4 {
		t.Errorf("origin requests = %d, want 4 distinct targets", o.count())
	}
}
