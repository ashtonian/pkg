//go:build unit

package httpx

import (
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

// The request's max-age and min-fresh bind every way a stored response can
// reach the caller: a hit, stale-while-revalidate and stale-if-error. Stale
// permissions only cover a response past its lifetime.
//
// Each stored response carries Age: 30, so under max-age=60 it has 30 s of
// freshness left until the clock moves.
func TestCacheRequestFreshnessBindsEveryReusePath(t *testing.T) {
	const (
		plain = "max-age=60"
		sie   = "max-age=60, stale-if-error=90"
		swr   = "max-age=60, stale-while-revalidate=90"

		stale40 = 40 * time.Second // age 70: stale by 10 s
	)
	tests := []struct {
		name       string
		stored     string
		opts       []CacheOption
		after      time.Duration
		probe      string // request Cache-Control
		origin     int    // the origin's answer to the probe: 200, 304 or 503
		wantStatus int
		wantBody   string
		wantCache  string // Cache-Status
		wantCalls  int    // origin requests, including the storing one
	}{
		// The request's requirements hold: ordinary reuse.
		{"min-fresh within the remaining lifetime is a hit", plain, nil, 0, "min-fresh=20", 503, 200, "cached", "httpx; hit", 1},
		{"max-age above the age is a hit", plain, nil, 0, "max-age=40", 503, 200, "cached", "httpx; hit", 1},

		// min-fresh=45 asks for more than the 30 s left.
		{"min-fresh: successful validation", plain, nil, 0, "min-fresh=45", 304, 200, "cached", "httpx; fwd=stale; fwd-status=304", 2},
		{"min-fresh: origin failure", plain, nil, 0, "min-fresh=45", 503, 503, "down", "", 2},
		{"min-fresh: origin stale-if-error does not relax it", sie, nil, 0, "min-fresh=45", 503, 503, "down", "", 2},
		{"min-fresh: configured stale-if-error does not relax it", plain, []CacheOption{WithStaleIfError(90 * time.Second)}, 0, "min-fresh=45", 503, 503, "down", "", 2},
		{"min-fresh: origin stale-while-revalidate validates first", swr, nil, 0, "min-fresh=45", 200, 200, "refreshed", "httpx; fwd=stale; stored", 2},
		{"min-fresh: configured stale-while-revalidate validates first", plain, []CacheOption{WithStaleWhileRevalidate(90 * time.Second)}, 0, "min-fresh=45", 200, 200, "refreshed", "httpx; fwd=stale; stored", 2},

		// max-age=20 refuses a response 30 s old.
		{"max-age: stale-if-error does not relax it", sie, nil, 0, "max-age=20", 503, 503, "down", "", 2},
		{"max-age: stale-while-revalidate validates first", swr, nil, 0, "max-age=20", 200, 200, "refreshed", "httpx; fwd=stale; stored", 2},

		// Past its lifetime, a response needs a stale permission.
		{"stale: stale-if-error serves a request that permits it", sie, nil, stale40, "", 503, 200, "cached", "httpx; hit; detail=stale-if-error", 2},
		{"stale: stale-while-revalidate serves a request that permits it", swr, nil, stale40, "", 200, 200, "cached", "httpx; hit; detail=stale-while-revalidate", 2},
		{"stale: max-stale serves it directly", plain, nil, stale40, "max-stale=20", 503, 200, "cached", "httpx; hit", 1},
		{"stale: max-age above the age permits stale-if-error", sie, nil, stale40, "max-age=100", 503, 200, "cached", "httpx; hit; detail=stale-if-error", 2},
		{"stale: max-age below the age forbids stale-if-error", sie, nil, stale40, "max-age=60", 503, 503, "down", "", 2},
		{"stale: min-fresh outranks max-stale", plain, nil, stale40, "min-fresh=5, max-stale=60", 503, 503, "down", "", 2},
		{"stale: min-fresh forbids stale-if-error", sie, nil, stale40, "min-fresh=0", 503, 503, "down", "", 2},
		{"stale: no-cache forbids stale-if-error", sie, nil, stale40, "no-cache", 503, 503, "down", "", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := &origin{respond: func(n int, _ *http.Request) *http.Response {
					switch {
					case n == 1:
						return reply(200, "cached", "Cache-Control", tt.stored, "Age", "30", "ETag", `"v1"`)
					case tt.origin == http.StatusNotModified:
						return reply(http.StatusNotModified, "", "Cache-Control", plain, "ETag", `"v1"`)
					case tt.origin == http.StatusOK:
						return reply(200, "refreshed", "Cache-Control", plain, "ETag", `"v2"`)
					}
					return reply(http.StatusServiceUnavailable, "down")
				}}
				ct := newCache(t, o, tt.opts...)
				get(t, ct, itemsURL)
				time.Sleep(tt.after)

				var probe []string
				if tt.probe != "" {
					probe = []string{"Cache-Control", tt.probe}
				}
				status, body, h := get(t, ct, itemsURL, probe...)
				synctest.Wait() // let a background refresh reach the origin
				if status != tt.wantStatus || body != tt.wantBody {
					t.Errorf("got %d %q, want %d %q", status, body, tt.wantStatus, tt.wantBody)
				}
				if got := h.Get("Cache-Status"); got != tt.wantCache {
					t.Errorf("Cache-Status %q, want %q", got, tt.wantCache)
				}
				if o.count() != tt.wantCalls {
					t.Errorf("origin requests = %d, want %d", o.count(), tt.wantCalls)
				}
			})
		})
	}
}

// Stale-if-error judges the response when the validation fails: time spent
// waiting on the origin counts against the window.
func TestCacheStaleIfErrorMeasuresAgeAtTheFailure(t *testing.T) {
	for _, tt := range []struct {
		name      string
		wait      time.Duration // origin delay before its 503
		wantStale bool
	}{
		{"failure inside the window", 10 * time.Second, true},
		{"failure after the window closed", 30 * time.Second, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := &origin{respond: func(n int, _ *http.Request) *http.Response {
					if n == 1 {
						return reply(200, "cached", "Cache-Control", "max-age=60, stale-if-error=30", "ETag", `"v1"`)
					}
					time.Sleep(tt.wait)
					return reply(http.StatusServiceUnavailable, "down")
				}}
				ct := newCache(t, o)
				get(t, ct, itemsURL)
				time.Sleep(70 * time.Second) // stale by 10 s when the validation starts
				status, body, h := get(t, ct, itemsURL)
				if gotStale := status == 200 && body == "cached"; gotStale != tt.wantStale {
					t.Fatalf("got %d %q, want stale=%v", status, body, tt.wantStale)
				}
				if tt.wantStale && h.Get("Age") != "80" {
					t.Errorf("Age %q, want the age when served, 80", h.Get("Age"))
				}
			})
		})
	}
}
