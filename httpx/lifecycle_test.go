//go:build unit

package httpx

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// refreshOrigin stores a response that is stale on arrival, so the next GET
// is served stale while a background refresh starts; every refresh is held
// at the origin until its context ends.
func refreshOrigin(finished *atomic.Int32) *origin {
	return &origin{respond: func(n int, r *http.Request) *http.Response {
		if n == 1 {
			return reply(200, "v1", "Cache-Control", "max-age=1, stale-while-revalidate=600", "Age", "5", "ETag", `"v1"`)
		}
		<-r.Context().Done()
		finished.Add(1)
		return reply(http.StatusServiceUnavailable, "")
	}}
}

// builders construct a transport stack whose cache the build owns, over base.
var lifecycleBuilders = []struct {
	name  string
	build func(*Lifecycle, http.RoundTripper) (http.RoundTripper, error)
}{
	{"NewClient", func(l *Lifecycle, base http.RoundTripper) (http.RoundTripper, error) {
		c, err := NewClient(WithTransport(base), WithCache(nil), WithLifecycle(l))
		if err != nil {
			return nil, err
		}
		return c.Transport, nil
	}},
	{"NewStandard", func(l *Lifecycle, base http.RoundTripper) (http.RoundTripper, error) {
		c, err := NewStandard(WithTransport(base), WithCache(nil), WithLifecycle(l))
		if err != nil {
			return nil, err
		}
		return c.Transport, nil
	}},
	{"Chain.BuildWithLifecycle", func(l *Lifecycle, base http.RoundTripper) (http.RoundTripper, error) {
		return NewChain(WithRetryT(), WithCacheT(nil)).BuildWithLifecycle(l, base)
	}},
	{"Chain.ClientWithLifecycle", func(l *Lifecycle, base http.RoundTripper) (http.RoundTripper, error) {
		c, err := NewChain(WithCacheT(nil)).ClientWithLifecycle(l, base, 0)
		if err != nil {
			return nil, err
		}
		return c.Transport, nil
	}},
	{"ResilientChain", func(l *Lifecycle, base http.RoundTripper) (http.RoundTripper, error) {
		return ResilientChain().BuildWithLifecycle(l, base)
	}},
}

// Closing idle connections leaves a builder's cache refreshing; closing its
// Lifecycle cancels the refresh and returns once it has finished.
func TestLifecycleStopsBuilderCacheRefresh(t *testing.T) {
	for _, b := range lifecycleBuilders {
		t.Run(b.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var finished atomic.Int32
				var life Lifecycle
				defer life.Close()
				rt := must(b.build(&life, refreshOrigin(&finished)))
				client := &http.Client{Transport: rt}

				get(t, rt, itemsURL)
				if _, body, h := get(t, rt, itemsURL); body != "v1" || h.Get("Cache-Status") != "httpx; hit; detail=stale-while-revalidate" {
					t.Fatalf("second GET: %q, Cache-Status %q; want the stale v1 while refreshing", body, h.Get("Cache-Status"))
				}
				synctest.Wait()
				client.CloseIdleConnections()
				synctest.Wait()
				if finished.Load() != 0 {
					t.Fatal("CloseIdleConnections stopped the background refresh")
				}

				if err := life.Close(); err != nil {
					t.Fatal(err)
				}
				if finished.Load() != 1 {
					t.Fatal("Close returned before the refresh finished")
				}
				if err := life.Close(); err != nil {
					t.Fatalf("second Close: %v", err)
				}
			})
		})
	}
}

// Every concurrent Close returns only after the work has stopped.
func TestLifecycleConcurrentClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var finished atomic.Int32
		var life Lifecycle
		rt := must(NewChain(WithCacheT(nil)).BuildWithLifecycle(&life, refreshOrigin(&finished)))
		get(t, rt, itemsURL)
		get(t, rt, itemsURL)
		synctest.Wait()

		var wg sync.WaitGroup
		var early atomic.Int32
		for range 8 {
			wg.Go(func() {
				if err := life.Close(); err != nil {
					t.Error(err)
				}
				if finished.Load() != 1 {
					early.Add(1)
				}
			})
		}
		wg.Wait()
		if early.Load() != 0 {
			t.Fatalf("%d Close calls returned before the refresh finished", early.Load())
		}
	})
}

// A Lifecycle with no background work closes promptly, and never closes a
// caller-supplied base's connections; CloseIdleConnections still reaches it.
func TestLifecycleLeavesSuppliedBase(t *testing.T) {
	for _, withCache := range []bool{false, true} {
		for _, b := range []struct {
			name  string
			build func(*Lifecycle, http.RoundTripper) (http.RoundTripper, error)
		}{
			{"NewClient", func(l *Lifecycle, base http.RoundTripper) (http.RoundTripper, error) {
				opts := []Option{WithTransport(base), WithLifecycle(l)}
				if withCache {
					opts = append(opts, WithCache(nil))
				}
				c, err := NewClient(opts...)
				if err != nil {
					return nil, err
				}
				return c.Transport, nil
			}},
			{"Chain", func(l *Lifecycle, base http.RoundTripper) (http.RoundTripper, error) {
				chain := NewChain(WithCompressionT())
				if withCache {
					chain = chain.Append(WithCacheT(nil))
				}
				return chain.BuildWithLifecycle(l, base)
			}},
		} {
			name := b.name
			if withCache {
				name += "/cache"
			}
			t.Run(name, func(t *testing.T) {
				base := &idleCounter{RoundTripper: &origin{respond: func(int, *http.Request) *http.Response {
					return reply(200, "ok", "Cache-Control", "max-age=60")
				}}}
				var life Lifecycle
				rt := must(b.build(&life, base))
				get(t, rt, itemsURL)
				if err := life.Close(); err != nil {
					t.Fatal(err)
				}
				if base.closes.Load() != 0 {
					t.Fatal("Close closed the idle connections of a supplied base")
				}
				(&http.Client{Transport: rt}).CloseIdleConnections()
				if base.closes.Load() != 1 {
					t.Fatalf("CloseIdleConnections reached the base %d times, want 1", base.closes.Load())
				}
			})
		}
	}
}

// closingStore is a caller-owned store that records Close.
type closingStore struct {
	*MemoryCache
	closed atomic.Bool
}

func (s *closingStore) Close() error { s.closed.Store(true); return nil }

// A Lifecycle closes only the caches its builds constructed: never a
// caller's cache, store or token source, even where Unwrap reaches it.
func TestLifecycleLeavesBorrowedResources(t *testing.T) {
	ok := &origin{respond: func(int, *http.Request) *http.Response {
		return reply(200, "ok", "Cache-Control", "max-age=60")
	}}
	open := func(c *CacheTransport) bool { return c.closeCtx.Err() == nil }

	t.Run("caller cache beneath a client", func(t *testing.T) {
		shared := must(NewCacheTransport(ok, nil))
		defer shared.Close()
		var life Lifecycle
		client := must(NewClient(WithTransport(shared), WithCache(nil), WithLifecycle(&life)))
		owned := client.Transport.(*CacheTransport)
		must(0, life.Close())
		if !open(shared) {
			t.Error("closed the caller's cache it reaches through Unwrap")
		}
		if open(owned) {
			t.Error("left the client's own cache running")
		}
	})

	t.Run("caller cache in a custom layer", func(t *testing.T) {
		shared := must(NewCacheTransport(ok, nil))
		defer shared.Close()
		var own *CacheTransport
		var life Lifecycle
		must(NewChain(
			func(http.RoundTripper) (http.RoundTripper, error) { return shared, nil },
			func(rt http.RoundTripper) (http.RoundTripper, error) {
				c, err := NewCacheTransport(rt, nil)
				own = c
				return c, err
			},
		).BuildWithLifecycle(&life, ok))
		defer own.Close()
		must(0, life.Close())
		if !open(shared) || !open(own) {
			t.Errorf("closed caches a custom layer supplied: shared open=%v, constructed open=%v", open(shared), open(own))
		}
	})

	t.Run("cache of an earlier build as base", func(t *testing.T) {
		earlier := must(NewChain(WithCacheT(nil)).Build(ok)).(*CacheTransport)
		defer earlier.Close()
		detached := must(WithCacheT(nil)(ok)).(*CacheTransport) // a layer function called outside any build
		defer detached.Close()
		for _, base := range []*CacheTransport{earlier, detached} {
			var life Lifecycle
			passThrough := func(rt http.RoundTripper) (http.RoundTripper, error) { return rt, nil }
			must(NewChain(passThrough, WithCacheT(nil)).BuildWithLifecycle(&life, base))
			must(0, life.Close())
			if !open(base) {
				t.Error("closed a cache it was given as the base")
			}
		}
	})

	t.Run("store and token source", func(t *testing.T) {
		store := &closingStore{MemoryCache: must(NewMemoryCache())}
		source := must(NewTokenSource("token", time.Time{}, func(context.Context) (string, time.Time, error) {
			return "token", time.Time{}, nil
		}))
		defer source.Close()
		var life Lifecycle
		client := must(NewClient(
			WithTransport(ok),
			WithTokenAuth("https://api.example.com", source),
			WithCache(store),
			WithLifecycle(&life),
		))
		get(t, client.Transport, itemsURL)
		must(0, life.Close())
		if store.closed.Load() {
			t.Error("closed the caller's store")
		}
		if _, _, err := source.Token(context.Background()); err != nil {
			t.Errorf("token source after Close: %v", err)
		}
	})
}

// A nil or closed Lifecycle fails the build, and a failed build closes the
// cache it had already constructed.
func TestLifecycleRefusesBuilds(t *testing.T) {
	ok := &origin{respond: func(int, *http.Request) *http.Response { return reply(200, "ok") }}
	var closed Lifecycle
	must(0, closed.Close())

	for _, tt := range []struct {
		name  string
		build func() error
	}{
		{"NewClient nil", func() error { _, err := NewClient(WithLifecycle(nil)); return err }},
		{"NewClient closed", func() error { _, err := NewClient(WithCache(nil), WithLifecycle(&closed)); return err }},
		{"NewStandard closed", func() error { _, err := NewStandard(WithLifecycle(&closed)); return err }},
		{"BuildWithLifecycle nil", func() error { _, err := NewChain().BuildWithLifecycle(nil, ok); return err }},
		{"BuildWithLifecycle closed", func() error { _, err := NewChain(WithCacheT(nil)).BuildWithLifecycle(&closed, ok); return err }},
		{"ClientWithLifecycle nil", func() error { _, err := NewChain().ClientWithLifecycle(nil, ok, 0); return err }},
		{"ClientWithLifecycle closed", func() error { _, err := NewChain().ClientWithLifecycle(&closed, ok, 0); return err }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.build(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("error %v, want ErrInvalidConfig", err)
			}
		})
	}

	for _, tt := range []struct {
		name  string
		life  *Lifecycle
		after TransportFunc
	}{
		{"closed lifecycle", &closed, nil},
		{"invalid later layer", new(Lifecycle), WithRetryT(WithMaxAttempts(0))},
	} {
		t.Run("failed build closes its cache/"+tt.name, func(t *testing.T) {
			var built *CacheTransport
			chain := NewChain(WithCacheT(nil), func(rt http.RoundTripper) (http.RoundTripper, error) {
				built = rt.(*CacheTransport)
				return rt, nil
			})
			if tt.after != nil {
				chain = chain.Append(tt.after)
			}
			if _, err := chain.BuildWithLifecycle(tt.life, ok); err == nil {
				t.Fatal("build succeeded")
			}
			if built.closeCtx.Err() == nil {
				t.Error("the failed build left its cache open")
			}
		})
	}
}
