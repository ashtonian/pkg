//go:build unit

package httpx

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Every network attempt spends the client's budgets: one rate token admits
// one attempt, not a retry series, and a concurrency cap of one holds a
// hedge back.
func TestClientBudgetsCountNetworkAttempts(t *testing.T) {
	t.Run("rate limit and retry", func(t *testing.T) {
		up := &scriptedUpstream{statuses: []int{503, 503, 200}}
		c := must(NewClient(WithTransport(up), WithRateLimit(0.001, 1),
			WithRetry(WithMaxAttempts(3), WithBackoff(time.Nanosecond, time.Nanosecond, 1))))
		resp, err := c.Get("https://example.test")
		if err == nil {
			resp.Body.Close()
		}
		if !errors.Is(err, ErrRateLimited) || up.attempts() != 1 {
			t.Fatalf("attempts=%d err=%v: a retry must spend a token or fail for want of one", up.attempts(), err)
		}
	})
	t.Run("limiter and hedging", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var active, peak atomic.Int32
			up := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); old < n && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				select {
				case <-time.After(time.Second):
					return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, nil
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			})
			c := must(NewClient(WithTransport(up), WithLimiter(1), WithHedging(time.Millisecond)))
			resp, err := c.Get("https://example.test")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			synctest.Wait()
			if peak.Load() > 1 {
				t.Fatalf("WithLimiter(1) let %d attempts run at once", peak.Load())
			}
		})
	})
}

// The cache answers from outside the breaker: an open breaker does not stop a
// fresh hit.
func TestClientCacheServesWhileTheBreakerIsOpen(t *testing.T) {
	var calls atomic.Int32
	up := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			h := http.Header{"Date": {time.Now().UTC().Format(http.TimeFormat)}, "Cache-Control": {"max-age=60"}}
			return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("cached")), Request: r}, nil
		}
		return &http.Response{StatusCode: 503, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})
	c := must(NewClient(WithTransport(up), WithCache(nil), WithCircuitBreaker(WithFailureThreshold(1))))
	read := func(url string) (int, string) {
		resp, err := c.Get(url)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	read("https://example.test/a")     // stored
	read("https://example.test/other") // 503 opens the breaker
	if status, _ := read("https://example.test/other"); status == 503 {
		t.Fatal("setup: the breaker should be open")
	}
	if status, body := read("https://example.test/a"); status != 200 || body != "cached" {
		t.Fatalf("a fresh hit behind an open breaker = (%d, %q)", status, body)
	}
}
