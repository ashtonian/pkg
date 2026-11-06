//go:build unit

package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

type controlledCacheStore struct {
	CacheStore
	get func(context.Context, string) (*CachedResponse, bool, error)
	set func(context.Context, string, *CachedResponse, time.Duration) error
	del func(context.Context, string) error
}

func (s *controlledCacheStore) Get(ctx context.Context, key string) (*CachedResponse, bool, error) {
	if s.get != nil {
		return s.get(ctx, key)
	}
	return s.CacheStore.Get(ctx, key)
}
func (s *controlledCacheStore) Set(ctx context.Context, key string, value *CachedResponse, ttl time.Duration) error {
	if s.set != nil {
		return s.set(ctx, key, value, ttl)
	}
	return s.CacheStore.Set(ctx, key, value, ttl)
}
func (s *controlledCacheStore) Delete(ctx context.Context, key string) error {
	if s.del != nil {
		return s.del(ctx, key)
	}
	return s.CacheStore.Delete(ctx, key)
}

func TestPersistentCacheContextAndCancellation(t *testing.T) {
	for _, op := range []CacheOperation{CacheRead, CacheWrite, CacheDelete} {
		t.Run(string(op), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				type key struct{}
				entered := make(chan struct{})
				block := func(ctx context.Context) error {
					if ctx.Value(key{}) != "scope" {
						t.Error("context value missing")
					}
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				}
				store := &controlledCacheStore{CacheStore: must(NewMemoryCache())}
				switch op {
				case CacheRead:
					store.get = func(ctx context.Context, _ string) (*CachedResponse, bool, error) { return nil, false, block(ctx) }
				case CacheWrite:
					store.set = func(ctx context.Context, _ string, _ *CachedResponse, _ time.Duration) error { return block(ctx) }
				case CacheDelete:
					store.del = func(ctx context.Context, _ string) error { return block(ctx) }
				}
				observed := make(chan *CacheError, 1)
				base := stubRoundTripper(func(*http.Request) (*http.Response, error) {
					return reply(200, "received", "Cache-Control", "max-age=60"), nil
				})
				cache := must(NewCacheTransport(base, store, WithOnCacheError(func(_ context.Context, e *CacheError) { observed <- e })))
				defer cache.Close()
				ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "scope"))
				defer cancel()
				method := http.MethodGet
				if op == CacheDelete {
					method = http.MethodPost
				}
				req, _ := http.NewRequestWithContext(ctx, method, itemsURL, nil)
				done := make(chan struct{})
				go func() {
					defer close(done)
					resp, err := cache.RoundTrip(req)
					if op == CacheRead {
						if !errors.Is(err, context.Canceled) {
							t.Errorf("read cancellation=%v", err)
						}
					} else if err != nil || resp == nil || resp.StatusCode != 200 {
						t.Errorf("acknowledged response lost: %v %v", resp, err)
					}
					if resp != nil {
						if strings.Contains(resp.Header.Get("Cache-Status"), "; stored") {
							t.Error("canceled persistence reported stored")
						}
						resp.Body.Close()
					}
				}()
				<-entered
				cancel()
				<-done
				e := <-observed
				if e.Operation != op || !errors.Is(e, context.Canceled) {
					t.Fatalf("event=%v", e)
				}
			})
		})
	}
}

func TestPersistentCacheStripeWaitIsCancelable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		store := &controlledCacheStore{CacheStore: must(NewMemoryCache()), get: func(context.Context, string) (*CachedResponse, bool, error) {
			close(entered)
			<-release
			return nil, false, nil
		}}
		base := stubRoundTripper(func(*http.Request) (*http.Response, error) { return reply(200, "received"), nil })
		cache := must(NewCacheTransport(base, store))
		defer cache.Close()
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, itemsURL, nil)
		first := make(chan struct{})
		go func() {
			defer close(first)
			resp, _ := cache.RoundTrip(req)
			if resp != nil {
				resp.Body.Close()
			}
		}()
		<-entered
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		second := make(chan error, 1)
		go func() { _, err := cache.RoundTrip(req.WithContext(ctx)); second <- err }()
		synctest.Wait()
		cancel()
		if err := <-second; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		<-first
	})
}

func TestPersistentCacheFailuresAreDistinctFromDelivery(t *testing.T) {
	for _, op := range []CacheOperation{CacheRead, CacheWrite, CacheDelete} {
		t.Run(string(op), func(t *testing.T) {
			cause := errors.New("private URL token body")
			store := &controlledCacheStore{CacheStore: must(NewMemoryCache())}
			var events []*CacheError
			calls := 0
			base := stubRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return reply(200, "received", "Cache-Control", "max-age=60"), nil
			})
			cache := must(NewCacheTransport(base, store, WithOnCacheError(func(_ context.Context, e *CacheError) { events = append(events, e) })))
			defer cache.Close()
			get(t, cache, itemsURL)
			switch op {
			case CacheRead:
				store.get = func(context.Context, string) (*CachedResponse, bool, error) { return nil, false, cause }
			case CacheWrite:
				store.set = func(context.Context, string, *CachedResponse, time.Duration) error { return cause }
			case CacheDelete:
				store.del = func(context.Context, string) error { return cause }
			}
			method := http.MethodGet
			if op == CacheDelete {
				method = http.MethodPost
			}
			req, _ := http.NewRequestWithContext(t.Context(), method, itemsURL, nil)
			req.Header.Set("Cache-Control", "no-cache")
			resp, err := cache.RoundTrip(req)
			if err != nil || resp.StatusCode != 200 {
				t.Fatal(resp, err)
			}
			resp.Body.Close()
			if op != CacheRead && strings.Contains(resp.Header.Get("Cache-Status"), "; stored") {
				t.Fatal("failed maintenance reported stored")
			}
			if len(events) != 1 || !errors.Is(events[0], cause) || strings.Contains(events[0].Error(), "private") {
				t.Fatalf("events=%v", events)
			}
			if op == CacheDelete {
				get(t, cache, itemsURL)
				if calls != 3 {
					t.Fatalf("failed invalidation reused stale cache; calls=%d", calls)
				}
			}
			if op == CacheRead {
				status, _, headers := get(t, cache, itemsURL, "Cache-Control", "only-if-cached")
				if status != 504 || !strings.Contains(headers.Get("Cache-Status"), "store-error") || calls != 2 {
					t.Fatalf("only-if-cached status=%d calls=%d", status, calls)
				}
			}
		})
	}
}

func TestPersistentCacheBackgroundWriteStopsOnClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan context.Context, 1)
		store := &controlledCacheStore{CacheStore: must(NewMemoryCache())}
		base := stubRoundTripper(func(*http.Request) (*http.Response, error) {
			return reply(200, "received", "Cache-Control", "max-age=0, stale-while-revalidate=60"), nil
		})
		cache := must(NewCacheTransport(base, store))
		defer cache.Close()
		get(t, cache, itemsURL)
		store.set = func(ctx context.Context, _ string, _ *CachedResponse, _ time.Duration) error {
			entered <- ctx
			<-ctx.Done()
			return ctx.Err()
		}
		ctx, cancel := context.WithCancel(t.Context())
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, itemsURL, nil)
		resp, err := cache.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		cancel()
		background := <-entered
		if background.Err() != nil {
			t.Fatal("foreground cancellation ended revalidation")
		}
		if _, ok := background.Deadline(); !ok {
			t.Fatal("background I/O unbounded")
		}
		cache.Close()
		if !errors.Is(background.Err(), context.Canceled) {
			t.Fatal(background.Err())
		}
	})
}

func TestPersistentCacheFailed304CannotReuseOldFreshness(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			store := &controlledCacheStore{CacheStore: must(NewMemoryCache())}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			calls := 0
			base := stubRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return reply(200, "body", "Cache-Control", "max-age=60", "ETag", `"old"`), nil
				}
				resp := reply(304, "", "Cache-Control", "no-cache", "ETag", `"new"`)
				if canceled {
					resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
				}
				return resp, nil
			})
			cache := must(NewCacheTransport(base, store))
			defer cache.Close()
			get(t, cache, itemsURL)
			store.set = func(context.Context, string, *CachedResponse, time.Duration) error {
				return errors.New("storage unavailable")
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, itemsURL, nil)
			req.Header.Set("Cache-Control", "no-cache")
			resp, err := cache.RoundTrip(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if !strings.Contains(resp.Header.Get("Cache-Status"), "store-error") {
				t.Fatal(resp.Header)
			}
			get(t, cache, itemsURL)
			if calls != 3 {
				t.Fatalf("reused old freshness after failed 304 persistence: calls=%d", calls)
			}
		})
	}
}

func TestMemoryCacheHonorsCanceledContext(t *testing.T) {
	cache := must(NewMemoryCache())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := cache.Get(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := cache.Set(ctx, "key", stored("body"), time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := cache.Delete(ctx, "key"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	b.cancel()
	return b.ReadCloser.Close()
}
