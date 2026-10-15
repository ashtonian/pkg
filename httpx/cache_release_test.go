//go:build unit

package httpx

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestCacheFullValidationRetiresPreviousRepresentation(t *testing.T) {
	for _, policy := range []string{"no-store", "vary-star", "oversized"} {
		t.Run(policy, func(t *testing.T) {
			o := &origin{respond: func(n int, _ *http.Request) *http.Response {
				if n == 1 {
					return reply(200, "old", "Cache-Control", "max-age=3600", "ETag", `"v1"`)
				}
				switch policy {
				case "no-store":
					return reply(200, "replacement", "Cache-Control", "no-store")
				case "vary-star":
					return reply(200, "replacement", "Cache-Control", "max-age=60", "Vary", "*")
				default:
					return reply(200, "replacement", "Cache-Control", "max-age=60")
				}
			}}
			ct := newCache(t, o, WithMaxCacheBodyBytes(3))
			get(t, ct, itemsURL)
			get(t, ct, itemsURL, "Cache-Control", "no-cache")
			if _, got, _ := get(t, ct, itemsURL); got != "replacement" || o.count() != 3 {
				t.Fatalf("superseded representation reused: body=%q origin calls=%d", got, o.count())
			}
		})
	}
}

func TestCacheBackgroundFullValidationRetiresPreviousRepresentation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		o := &origin{respond: func(n int, _ *http.Request) *http.Response {
			if n == 1 {
				return reply(200, "old", "Cache-Control", "max-age=0, stale-while-revalidate=60", "ETag", `"v1"`)
			}
			return reply(200, "replacement", "Cache-Control", "no-store")
		}}
		ct := newCache(t, o)
		get(t, ct, itemsURL)
		get(t, ct, itemsURL)
		synctest.Wait()
		if _, got, _ := get(t, ct, itemsURL); got != "replacement" || o.count() != 3 {
			t.Fatalf("background refresh left superseded response: body=%q origin calls=%d", got, o.count())
		}
	})
}

// A custom store observes every publication, including a value removed
// immediately afterwards. An invalidated fetch must never reach Set.
type publicationStore struct {
	CacheStore
	mu        sync.Mutex
	published []string
}

func (s *publicationStore) Set(key string, value *CachedResponse, ttl time.Duration) {
	s.mu.Lock()
	s.published = append(s.published, string(value.Body))
	s.mu.Unlock()
	s.CacheStore.Set(key, value, ttl)
}

func TestCacheInvalidatedFetchNeverPublishes(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	o := &origin{respond: func(n int, r *http.Request) *http.Response {
		if r.Method == http.MethodPost {
			return reply(204, "")
		}
		if n == 1 {
			close(started)
			<-release
			return reply(200, "old", "Cache-Control", "max-age=60")
		}
		return reply(200, "new", "Cache-Control", "max-age=60")
	}}
	store := &publicationStore{CacheStore: must(NewMemoryCache())}
	ct := must(NewCacheTransport(o, store))
	defer ct.Close()
	done := make(chan struct{})
	go func() { defer close(done); get(t, ct, itemsURL) }()
	<-started
	req, _ := http.NewRequest(http.MethodPost, itemsURL, nil)
	resp, err := ct.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	close(release)
	<-done
	store.mu.Lock()
	published := append([]string(nil), store.published...)
	store.mu.Unlock()
	if len(published) != 0 {
		t.Errorf("invalidated fetch published %q before deletion", published)
	}
	if _, got, _ := get(t, ct, itemsURL); got != "new" {
		t.Fatalf("post-invalidation body=%q", got)
	}
}

func TestCacheIdentityIncludesNonCanonicalHeaderKeys(t *testing.T) {
	for _, name := range []string{"Authorization", "Cookie", "X-Tenant-ID"} {
		t.Run(name, func(t *testing.T) {
			o := &origin{respond: func(_ int, r *http.Request) *http.Response {
				return reply(200, r.Header[strings.ToLower(name)][0], "Cache-Control", "max-age=60")
			}}
			ct := newCache(t, o, WithCacheIdentityHeaders("X-Tenant-ID"))
			for _, identity := range []string{"first", "second", "first"} {
				req, _ := http.NewRequest(http.MethodGet, itemsURL, nil)
				req.Header[strings.ToLower(name)] = []string{identity}
				resp, err := ct.RoundTrip(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || string(body) != identity {
					t.Fatalf("%s=%q received %q: %v", name, identity, body, err)
				}
			}
			if o.count() != 2 {
				t.Fatalf("origin calls=%d, want one per identity", o.count())
			}
		})
	}
}

func TestCacheNonCanonicalRequestDirectivesAndVary(t *testing.T) {
	o := &origin{respond: func(int, *http.Request) *http.Response {
		return reply(200, "body", "Cache-Control", "max-age=60", "Vary", "Accept-Language")
	}}
	ct := newCache(t, o)
	for _, headers := range []http.Header{
		{"accept-language": {"en"}},
		{"accept-language": {"fr"}},
		{"accept-language": {"fr"}, "cache-control": {"no-store"}},
	} {
		req, _ := http.NewRequest(http.MethodGet, itemsURL, nil)
		req.Header = headers
		resp, err := ct.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if o.count() != 3 {
		t.Fatalf("non-canonical request fields bypassed policy: origin calls=%d", o.count())
	}
}
