//go:build unit

package httpx

import (
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// flakyThenCacheable answers 503 to its first request and a cacheable 200
// afterwards.
func flakyThenCacheable() http.RoundTripper {
	var n atomic.Int32
	return stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		h := http.Header{"Date": {time.Now().UTC().Format(http.TimeFormat)}}
		if n.Add(1) == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: h, Body: http.NoBody, Request: r}, nil
		}
		h.Set("Cache-Control", "max-age=60")
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})
}

func getTwice(t *testing.T, c *http.Client) {
	t.Helper()
	for range 2 {
		resp, err := c.Get("https://api.example.com/items")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func TestClientMetricsHookSeesRetriesAndCacheEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		hook := &CountingHook{}
		c := must(NewClient(
			WithTransport(flakyThenCacheable()),
			WithCache(nil),
			WithRetry(WithMaxAttempts(2), WithBackoff(time.Millisecond, time.Millisecond, 1)),
			WithMetrics(hook),
		))
		getTwice(t, c)
		// The cache wraps retry: the first call misses once and is retried
		// below the cache; the second is a hit.
		if hook.Retries.Load() != 1 || hook.CacheMisses.Load() != 1 || hook.CacheHits.Load() != 1 {
			t.Fatalf("retries=%d misses=%d hits=%d, want 1/1/1",
				hook.Retries.Load(), hook.CacheMisses.Load(), hook.CacheHits.Load())
		}
		if hook.Requests.Load() != 2 || hook.Responses.Load() != 2 {
			t.Fatalf("requests=%d responses=%d, want one per call", hook.Requests.Load(), hook.Responses.Load())
		}
	})
}

func TestExplicitLayerHooksOverrideTheClientHook(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		clientHook, retryHook, cacheHook := &CountingHook{}, &CountingHook{}, &CountingHook{}
		c := must(NewClient(
			WithTransport(flakyThenCacheable()),
			WithCache(nil, WithCacheHook(cacheHook)),
			WithRetry(WithMaxAttempts(2), WithBackoff(time.Millisecond, time.Millisecond, 1), WithRetryHook(retryHook)),
			WithMetrics(clientHook),
		))
		getTwice(t, c)
		if clientHook.Retries.Load() != 0 || clientHook.CacheHits.Load() != 0 || clientHook.CacheMisses.Load() != 0 {
			t.Fatal("an explicit layer hook replaces the client hook for that layer")
		}
		if retryHook.Retries.Load() != 1 || cacheHook.CacheHits.Load() != 1 {
			t.Fatalf("layer hooks: retries=%d hits=%d", retryHook.Retries.Load(), cacheHook.CacheHits.Load())
		}
	})
}

// causeHook records retry causes.
type causeHook struct {
	CountingHook
	mu     sync.Mutex
	causes []RetryCause
}

func (h *causeHook) OnRetryCause(_ *http.Request, _ int, cause RetryCause, _ error) {
	h.mu.Lock()
	h.causes = append(h.causes, cause)
	h.mu.Unlock()
}

func TestMultiHookForwardsRetryCauses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		causes, plain := &causeHook{}, &CountingHook{}
		c := must(NewClient(
			WithTransport(flakyThenCacheable()),
			WithRetry(WithMaxAttempts(2), WithBackoff(time.Millisecond, time.Millisecond, 1)),
			WithMetrics(NewMultiHook(causes, nil, plain)),
		))
		getTwice(t, c)
		causes.mu.Lock()
		defer causes.mu.Unlock()
		if !slices.Equal(causes.causes, []RetryCause{RetryCauseStatus}) {
			t.Fatalf("causes = %v, want [status]", causes.causes)
		}
		if causes.Retries.Load() != 1 || plain.Retries.Load() != 1 {
			t.Fatal("every hook still receives OnRetry")
		}
	})
}

func TestLatencyHookKeepsABoundedWindow(t *testing.T) {
	h := must(NewLatencyHook(2))
	for _, ms := range []int{300, 100, 200} {
		h.OnResponse(nil, nil, time.Duration(ms)*time.Millisecond, nil)
	}
	if got := h.Recent(); !slices.Equal(got, []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}) {
		t.Fatalf("Recent = %v, want the last two, oldest first", got)
	}
	if h.Count() != 3 || h.Sum() != 600*time.Millisecond || h.Avg() != 200*time.Millisecond {
		t.Fatalf("aggregates cover every sample: count=%d sum=%s avg=%s", h.Count(), h.Sum(), h.Avg())
	}
	if h.Min() != 100*time.Millisecond || h.Max() != 300*time.Millisecond {
		t.Fatalf("min=%s max=%s", h.Min(), h.Max())
	}
	h.Reset()
	if h.Count() != 0 || len(h.Recent()) != 0 || h.Max() != 0 {
		t.Fatal("Reset clears aggregates and window")
	}
}
