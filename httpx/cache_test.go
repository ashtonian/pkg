//go:build unit

package httpx

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// origin is a scripted upstream: respond builds each response and every
// request is recorded.
type origin struct {
	mu       sync.Mutex
	requests []*http.Request
	respond  func(n int, r *http.Request) *http.Response
}

func (o *origin) RoundTrip(r *http.Request) (*http.Response, error) {
	o.mu.Lock()
	o.requests = append(o.requests, r.Clone(r.Context()))
	n := len(o.requests)
	o.mu.Unlock()
	resp := o.respond(n, r)
	if resp.Request == nil {
		resp.Request = r
	}
	return resp, nil
}

func (o *origin) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.requests)
}

func (o *origin) last() *http.Request {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.requests[len(o.requests)-1]
}

// reply builds a response dated now with the given status, headers and body.
func reply(status int, body string, kv ...string) *http.Response {
	h := http.Header{"Date": {time.Now().UTC().Format(http.TimeFormat)}}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

func newCache(t *testing.T, o *origin, opts ...CacheOption) *CacheTransport {
	t.Helper()
	ct := must(NewCacheTransport(o, nil, opts...))
	t.Cleanup(func() { _ = ct.Close() })
	return ct
}

// get performs a GET through rt and returns status, body and response
// headers.
func get(t *testing.T, rt http.RoundTripper, target string, kv ...string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	for i := 0; i+1 < len(kv); i += 2 {
		req.Header.Add(kv[i], kv[i+1])
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b), resp.Header
}

const itemsURL = "https://api.example.com/items"

func TestCacheFreshHitAndExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(n int, r *http.Request) *http.Response {
			if r.Header.Get("If-None-Match") == `"v1"` {
				return reply(http.StatusNotModified, "", "Cache-Control", "max-age=60", "ETag", `"v1"`)
			}
			return reply(200, "body-v1", "Cache-Control", "max-age=60", "ETag", `"v1"`)
		}}
		ct := newCache(t, o)

		get(t, ct, itemsURL)
		time.Sleep(30 * time.Second)
		status, body, h := get(t, ct, itemsURL)
		if o.count() != 1 || status != 200 || body != "body-v1" {
			t.Fatalf("fresh hit expected, origin saw %d requests", o.count())
		}
		if h.Get("Age") != "30" || h.Get("Cache-Status") != "httpx; hit" {
			t.Errorf("Age=%q Cache-Status=%q", h.Get("Age"), h.Get("Cache-Status"))
		}

		time.Sleep(31 * time.Second) // now stale
		status, body, _ = get(t, ct, itemsURL)
		if o.count() != 2 || o.last().Header.Get("If-None-Match") != `"v1"` {
			t.Fatalf("stale entry should be revalidated with its validator")
		}
		if status != 200 || body != "body-v1" {
			t.Fatalf("304 should serve the stored response, got %d %q", status, body)
		}
	})
}

func TestCacheValidatorOnlyResponsesRevalidateEveryTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(_ int, r *http.Request) *http.Response {
			if r.Header.Get("If-None-Match") == `"e"` {
				return reply(http.StatusNotModified, "", "ETag", `"e"`)
			}
			return reply(200, "payload", "ETag", `"e"`)
		}}
		ct := newCache(t, o)
		for range 3 {
			if _, body, _ := get(t, ct, itemsURL); body != "payload" {
				t.Fatalf("body = %q", body)
			}
		}
		if o.count() != 3 {
			t.Fatalf("each use must revalidate, origin saw %d", o.count())
		}
	})
}

func TestCacheNotModifiedMergesHeaders(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(n int, r *http.Request) *http.Response {
			if r.Header.Get("If-None-Match") == `"e"` {
				return reply(http.StatusNotModified, "", "Cache-Control", "max-age=100", "ETag", `"e"`,
					"X-Version", "2", "Content-Length", "0")
			}
			return reply(200, "payload", "Cache-Control", "max-age=10", "ETag", `"e"`,
				"X-Version", "1", "Content-Length", "7")
		}}
		ct := newCache(t, o)

		get(t, ct, itemsURL)
		time.Sleep(11 * time.Second)
		_, body, h := get(t, ct, itemsURL)
		if o.count() != 2 || body != "payload" {
			t.Fatalf("stale entry should revalidate and keep its body, origin saw %d, body %q", o.count(), body)
		}
		if h.Get("X-Version") != "2" || h.Get("Content-Length") != "7" {
			t.Errorf("304 fields must replace stored ones except framing: X-Version=%q Content-Length=%q",
				h.Get("X-Version"), h.Get("Content-Length"))
		}
		if h.Get("Cache-Status") != "httpx; fwd=stale; fwd-status=304" {
			t.Errorf("Cache-Status = %q", h.Get("Cache-Status"))
		}

		time.Sleep(50 * time.Second) // fresh again under the 304's max-age=100
		if _, _, h = get(t, ct, itemsURL); o.count() != 2 || h.Get("Cache-Status") != "httpx; hit" {
			t.Fatalf("the 304's freshness should apply, origin saw %d, Cache-Status %q", o.count(), h.Get("Cache-Status"))
		}
	})
}

func TestCacheResponseDirectives(t *testing.T) {
	tests := []struct {
		name      string
		headers   []string
		wantCalls int // origin requests for two GETs
	}{
		{"no-store is not stored", []string{"Cache-Control", "no-store, max-age=60"}, 2},
		{"no-cache is revalidated", []string{"Cache-Control", "no-cache, max-age=60", "ETag", `"x"`}, 2},
		{"Vary star is not stored", []string{"Cache-Control", "max-age=60", "Vary", "*"}, 2},
		{"no freshness and no validator is not stored", nil, 2},
		{"Expires gives freshness", []string{"Expires", "FUTURE"}, 1},
		{"s-maxage is ignored by a private cache", []string{"Cache-Control", "s-maxage=60"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := &origin{respond: func(int, *http.Request) *http.Response {
					kv := append([]string(nil), tt.headers...)
					for i := range kv {
						if kv[i] == "FUTURE" {
							kv[i] = time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
						}
					}
					return reply(200, "b", kv...)
				}}
				ct := newCache(t, o)
				get(t, ct, itemsURL)
				get(t, ct, itemsURL)
				if o.count() != tt.wantCalls {
					t.Fatalf("origin requests = %d, want %d", o.count(), tt.wantCalls)
				}
			})
		})
	}
}

func TestCacheRequestDirectives(t *testing.T) {
	tests := []struct {
		name      string
		after     time.Duration // between the storing GET and the probe
		probe     []string
		wantCalls int
	}{
		{"no-cache forces validation", 0, []string{"Cache-Control", "no-cache"}, 2},
		{"max-age=0 rejects any aged response", time.Second, []string{"Cache-Control", "max-age=0"}, 2},
		{"max-age above the age is a hit", 10 * time.Second, []string{"Cache-Control", "max-age=30"}, 1},
		{"min-fresh beyond the remaining lifetime validates", 50 * time.Second, []string{"Cache-Control", "min-fresh=30"}, 2},
		{"max-stale accepts a stale response", 70 * time.Second, []string{"Cache-Control", "max-stale=30"}, 1},
		{"max-stale too small validates", 100 * time.Second, []string{"Cache-Control", "max-stale=30"}, 2},
		{"no-store bypasses the cache", 0, []string{"Cache-Control", "no-store"}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := &origin{respond: func(int, *http.Request) *http.Response {
					return reply(200, "b", "Cache-Control", "max-age=60", "ETag", `"v"`)
				}}
				ct := newCache(t, o)
				get(t, ct, itemsURL)
				time.Sleep(tt.after)
				get(t, ct, itemsURL, tt.probe...)
				if o.count() != tt.wantCalls {
					t.Fatalf("origin requests = %d, want %d", o.count(), tt.wantCalls)
				}
			})
		})
	}
}

func TestCacheMustRevalidateForbidsStale(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(int, *http.Request) *http.Response {
			return reply(200, "b", "Cache-Control", "max-age=10, must-revalidate", "ETag", `"v"`)
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL)
		time.Sleep(20 * time.Second)
		get(t, ct, itemsURL, "Cache-Control", "max-stale=3600")
		if o.count() != 2 {
			t.Fatal("must-revalidate must win over max-stale")
		}
	})
}

func TestCacheOnlyIfCached(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(int, *http.Request) *http.Response {
			return reply(200, "b", "Cache-Control", "max-age=60")
		}}
		ct := newCache(t, o)
		if status, _, _ := get(t, ct, itemsURL, "Cache-Control", "only-if-cached"); status != http.StatusGatewayTimeout {
			t.Fatalf("empty cache: status %d, want 504", status)
		}
		get(t, ct, itemsURL)
		if status, _, _ := get(t, ct, itemsURL, "Cache-Control", "only-if-cached"); status != 200 {
			t.Fatalf("stored: status %d, want 200", status)
		}
		if o.count() != 1 {
			t.Fatalf("only-if-cached must never reach the origin, saw %d", o.count())
		}
	})
}

func TestCacheAgeHeaderShortensFreshness(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(int, *http.Request) *http.Response {
			return reply(200, "b", "Cache-Control", "max-age=60", "Age", "60")
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL)
		get(t, ct, itemsURL)
		if o.count() != 2 {
			t.Fatal("a response already 60s old with max-age=60 is stale on arrival")
		}
	})
}

func TestCacheBypassesRangeHeadAndConditionalRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(_ int, r *http.Request) *http.Response {
			if r.Header.Get("Range") != "" {
				return reply(http.StatusPartialContent, "par", "Cache-Control", "max-age=60", "Content-Range", "bytes 0-2/9")
			}
			return reply(200, "full-body", "Cache-Control", "max-age=60", "ETag", `"full"`)
		}}
		ct := newCache(t, o)

		if status, body, _ := get(t, ct, itemsURL, "Range", "bytes=0-2"); status != 206 || body != "par" {
			t.Fatalf("range request: %d %q", status, body)
		}
		if status, body, _ := get(t, ct, itemsURL); status != 200 || body != "full-body" {
			t.Fatalf("a full request after a range must not see the partial body: %d %q", status, body)
		}
		if status, body, _ := get(t, ct, itemsURL, "Range", "bytes=0-2"); status != 206 || body != "par" {
			t.Fatalf("a range request must not be answered with the stored full body: %d %q", status, body)
		}
		get(t, ct, itemsURL, "If-None-Match", `"caller"`)
		if got := o.last().Header.Get("If-None-Match"); got != `"caller"` {
			t.Fatalf("caller precondition replaced: %q", got)
		}
		req, _ := http.NewRequest(http.MethodHead, itemsURL, nil)
		resp, err := ct.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if o.count() != 5 {
			t.Fatalf("range, conditional and HEAD requests all go to the origin; saw %d", o.count())
		}
	})
}

func TestCacheVary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(_ int, r *http.Request) *http.Response {
			return reply(200, "lang="+r.Header.Get("Accept-Language"), "Cache-Control", "max-age=60", "Vary", "Accept-Language")
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL, "Accept-Language", "en")
		if _, body, _ := get(t, ct, itemsURL, "Accept-Language", "fr"); body != "lang=fr" {
			t.Fatalf("a French request received %q", body)
		}
		if _, body, _ := get(t, ct, itemsURL, "Accept-Language", "fr"); body != "lang=fr" || o.count() != 2 {
			t.Fatalf("the French variant should now be stored: %q after %d requests", body, o.count())
		}
	})
}

func TestCachePartitionsByIdentity(t *testing.T) {
	tests := []struct {
		name   string
		header string
		opts   []CacheOption
	}{
		{"authorization", "Authorization", nil},
		{"cookie", "Cookie", nil},
		{"configured identity header", "X-Tenant-Id", []CacheOption{WithCacheIdentityHeaders("x-tenant-id")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := &origin{respond: func(_ int, r *http.Request) *http.Response {
					return reply(200, "for "+r.Header.Get(tt.header), "Cache-Control", "private, max-age=60")
				}}
				ct := newCache(t, o, tt.opts...)
				get(t, ct, itemsURL, tt.header, "alice")
				if _, body, _ := get(t, ct, itemsURL, tt.header, "bob"); body != "for bob" {
					t.Fatalf("bob received %q", body)
				}
				if _, body, _ := get(t, ct, itemsURL, tt.header, "alice"); body != "for alice" || o.count() != 2 {
					t.Fatalf("alice's stored response: %q after %d requests", body, o.count())
				}
			})
		})
	}
}

func TestCachePreservesStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(int, *http.Request) *http.Response {
			return reply(http.StatusNotFound, "missing", "Cache-Control", "max-age=60")
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL)
		req, _ := http.NewRequest(http.MethodGet, itemsURL, nil)
		resp, err := ct.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 || resp.Status != "404 Not Found" || o.count() != 1 {
			t.Fatalf("stored 404 served as %d %q", resp.StatusCode, resp.Status)
		}
	})
}

// failingBody yields some bytes and then an error.
type failingBody struct{ r io.Reader }

func (b *failingBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err == io.EOF {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}
func (b *failingBody) Close() error { return nil }

func TestCacheSurfacesBodyReadErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(int, *http.Request) *http.Response {
			r := reply(200, "", "Cache-Control", "max-age=60")
			r.Body = &failingBody{r: strings.NewReader("abc")}
			return r
		}}
		ct := newCache(t, o)
		req, _ := http.NewRequest(http.MethodGet, itemsURL, nil)
		resp, err := ct.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		if string(b) != "abc" || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("truncated body must surface its error: %q, %v", b, err)
		}
		again, _ := http.NewRequest(http.MethodGet, itemsURL, nil)
		resp, err = ct.RoundTrip(again)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if o.count() != 2 {
			t.Fatal("a truncated response must not be stored")
		}
	})
}

func TestCacheOversizedBodyPassesThrough(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(int, *http.Request) *http.Response {
			return reply(200, "0123456789", "Cache-Control", "max-age=60")
		}}
		ct := newCache(t, o, WithMaxCacheBodyBytes(4))
		if _, body, _ := get(t, ct, itemsURL); body != "0123456789" {
			t.Fatalf("oversized body altered: %q", body)
		}
		get(t, ct, itemsURL)
		if o.count() != 2 {
			t.Fatal("a body over the limit must not be stored")
		}
	})
}

func TestCacheUnsafeMethodsInvalidate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(_ int, r *http.Request) *http.Response {
			if r.Method == http.MethodPost {
				return reply(http.StatusCreated, "", "Location", "/items/7")
			}
			return reply(200, "b", "Cache-Control", "max-age=60")
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL)
		get(t, ct, itemsURL+"/7")

		req, _ := http.NewRequest(http.MethodPost, itemsURL, strings.NewReader("{}"))
		resp, err := ct.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()

		get(t, ct, itemsURL)
		get(t, ct, itemsURL+"/7")
		if o.count() != 5 {
			t.Fatalf("POST must invalidate the target and its Location; origin saw %d", o.count())
		}
	})
}

func TestCacheStaleIfError(t *testing.T) {
	tests := []struct {
		name      string
		cc        string
		opts      []CacheOption
		wantStale bool
	}{
		{"client permission", "max-age=10", []CacheOption{WithStaleIfError(time.Minute)}, true},
		{"origin permission", "max-age=10, stale-if-error=60", nil, true},
		{"must-revalidate forbids it", "max-age=10, must-revalidate", []CacheOption{WithStaleIfError(time.Minute)}, false},
		{"outside the window", "max-age=10", []CacheOption{WithStaleIfError(time.Second)}, false},
		{"entry without a validator", "max-age=10", []CacheOption{WithStaleIfError(time.Minute)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := &origin{respond: func(n int, _ *http.Request) *http.Response {
					if n == 1 {
						if tt.name == "entry without a validator" {
							return reply(200, "good", "Cache-Control", tt.cc)
						}
						return reply(200, "good", "Cache-Control", tt.cc, "ETag", `"g"`)
					}
					return reply(http.StatusServiceUnavailable, "down")
				}}
				ct := newCache(t, o, tt.opts...)
				get(t, ct, itemsURL)
				time.Sleep(20 * time.Second)
				status, body, _ := get(t, ct, itemsURL)
				if tt.wantStale != (status == 200 && body == "good") {
					t.Fatalf("got %d %q, want stale=%v", status, body, tt.wantStale)
				}
			})
		})
	}
}

func TestCacheStaleWhileRevalidateCoalesces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		o := &origin{respond: func(n int, _ *http.Request) *http.Response {
			if n == 1 {
				return reply(200, "v1", "Cache-Control", "max-age=10, stale-while-revalidate=60", "ETag", `"1"`)
			}
			<-release // background revalidations stay in flight
			return reply(200, "v2", "Cache-Control", "max-age=10, stale-while-revalidate=60", "ETag", `"2"`)
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL)
		time.Sleep(20 * time.Second)

		for range 5 {
			req, _ := http.NewRequest(http.MethodGet, itemsURL, nil)
			resp, err := ct.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(b) != "v1" {
				t.Fatalf("stale body expected while revalidating, got %q", b)
			}
			req.Header.Set("X-Reused", "yes") // callers may reuse a request once it returns
		}
		synctest.Wait()
		if o.count() != 2 {
			t.Fatalf("5 stale hits must share one revalidation; origin saw %d", o.count())
		}
		close(release)
		synctest.Wait()
		if _, body, _ := get(t, ct, itemsURL); body != "v2" {
			t.Fatalf("revalidated response not stored: %q", body)
		}
	})
}

func TestCacheCloseStopsRevalidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(n int, r *http.Request) *http.Response {
			if n == 1 {
				return reply(200, "v1", "Cache-Control", "max-age=1, stale-while-revalidate=60", "ETag", `"1"`)
			}
			<-r.Context().Done()
			return reply(http.StatusServiceUnavailable, "")
		}}
		ct := must(NewCacheTransport(o, nil))
		get(t, ct, itemsURL)
		time.Sleep(5 * time.Second)
		get(t, ct, itemsURL) // starts a revalidation that blocks until cancelled
		if err := ct.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCacheStaleWhileRevalidateWithoutValidator(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(n int, _ *http.Request) *http.Response {
			return reply(200, fmt.Sprintf("v%d", n), "Cache-Control", "max-age=10, stale-while-revalidate=60")
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL)
		time.Sleep(20 * time.Second)
		if _, body, _ := get(t, ct, itemsURL); body != "v1" {
			t.Fatalf("stale body expected, got %q", body)
		}
		synctest.Wait()
		if _, body, _ := get(t, ct, itemsURL); body != "v2" || o.count() != 2 {
			t.Fatalf("background refresh should store v2: %q after %d requests", body, o.count())
		}
	})
}

// A 304 whose header fields now forbid storing (no-store, Vary: *) answers
// the request but leaves nothing reusable.
func TestCacheRevalidationReappliesStoragePolicy(t *testing.T) {
	for _, policy := range []string{"no-store", "vary-star"} {
		t.Run(policy, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				o := &origin{respond: func(n int, _ *http.Request) *http.Response {
					if n != 2 {
						return reply(200, "old", "Cache-Control", "max-age=0", "ETag", `"v1"`)
					}
					if policy == "no-store" {
						return reply(304, "", "Cache-Control", "no-store, max-age=60")
					}
					return reply(304, "", "Cache-Control", "max-age=60", "Vary", "*")
				}}
				ct := newCache(t, o)
				for range 3 {
					get(t, ct, itemsURL)
				}
				if o.count() != 3 {
					t.Errorf("a response the 304 made unstorable was reused: %d origin calls, want 3", o.count())
				}
			})
		})
	}
}

// A * anywhere in a Vary list makes the response unstorable.
func TestCacheVaryListWithWildcardIsNotStored(t *testing.T) {
	o := &origin{respond: func(int, *http.Request) *http.Response {
		return reply(200, "one", "Cache-Control", "max-age=60", "Vary", "Accept, *")
	}}
	ct := newCache(t, o)
	get(t, ct, itemsURL)
	get(t, ct, itemsURL)
	if o.count() != 2 {
		t.Errorf("Vary: Accept, * was reused: %d origin calls, want 2", o.count())
	}
}

// A revalidation in flight when an unsafe request invalidates the URI does
// not restore what it read.
func TestCacheRefreshDoesNotOutliveAnInvalidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		o := &origin{respond: func(n int, r *http.Request) *http.Response {
			if r.Method == http.MethodPost {
				return reply(204, "")
			}
			if n == 2 {
				close(started)
				<-release
				return reply(304, "", "Cache-Control", "max-age=60")
			}
			body := "old"
			if n > 2 {
				body = "new"
			}
			return reply(200, body, "Cache-Control", "max-age=1, stale-while-revalidate=60", "ETag", `"v1"`)
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL)
		time.Sleep(2 * time.Second)
		get(t, ct, itemsURL) // serves stale, revalidates in the background
		<-started
		post, _ := http.NewRequest(http.MethodPost, itemsURL, nil)
		resp, err := ct.RoundTrip(post)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		close(release)
		synctest.Wait()
		if _, body, _ := get(t, ct, itemsURL); body != "new" {
			t.Errorf("the revalidation restored %q after the POST invalidated it", body)
		}
	})
}

// The request's Host is its authority: virtual hosts behind one URL never
// share a response.
func TestCacheKeysOnTheEffectiveAuthority(t *testing.T) {
	o := &origin{respond: func(_ int, r *http.Request) *http.Response {
		return reply(200, r.Host, "Cache-Control", "max-age=60", "Vary", "Host")
	}}
	ct := newCache(t, o)
	for _, host := range []string{"tenant-a.example", "tenant-b.example", "tenant-a.example"} {
		r, _ := http.NewRequest(http.MethodGet, itemsURL, nil)
		r.Host = host
		resp, err := ct.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != host {
			t.Errorf("Host %q received %q", host, body)
		}
	}
	if o.count() != 2 {
		t.Errorf("%d origin calls, want one per host", o.count())
	}
}

// A GET with a body goes to the origin with its body; the cache neither
// answers it nor stores the answer.
func TestCacheBypassesBodyBearingGets(t *testing.T) {
	o := &origin{respond: func(int, *http.Request) *http.Response {
		return reply(200, "cached", "Cache-Control", "max-age=60")
	}}
	ct := newCache(t, o)
	get(t, ct, itemsURL)
	r, _ := http.NewRequest(http.MethodGet, itemsURL, strings.NewReader("query"))
	resp, err := ct.RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if o.count() != 2 {
		t.Fatalf("a body-bearing GET was answered from the cache (%d origin calls)", o.count())
	}
}

// Byte limits and ages are compared without arithmetic that can wrap.
func TestCacheArithmeticSaturates(t *testing.T) {
	t.Run("largest body limit", func(t *testing.T) {
		o := &origin{respond: func(int, *http.Request) *http.Response {
			return reply(200, "payload", "Cache-Control", "max-age=60")
		}}
		ct := newCache(t, o, WithMaxCacheBodyBytes(math.MaxInt64))
		if _, body, _ := get(t, ct, itemsURL); body != "payload" {
			t.Errorf("body = %q", body)
		}
		if _, body, _ := get(t, ct, itemsURL); body != "payload" || o.count() != 1 {
			t.Errorf("stored body = %q after %d origin calls", body, o.count())
		}
	})
	t.Run("ancient age", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			o := &origin{respond: func(int, *http.Request) *http.Response {
				time.Sleep(time.Nanosecond)
				return reply(200, "ancient", "Cache-Control", "max-age=60", "Age", "9223372037")
			}}
			ct := newCache(t, o)
			get(t, ct, itemsURL)
			get(t, ct, itemsURL)
			if o.count() != 2 {
				t.Errorf("a saturated Age read as fresh: %d origin calls, want 2", o.count())
			}
		})
	})
}
