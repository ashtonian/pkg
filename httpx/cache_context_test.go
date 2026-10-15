//go:build unit

package httpx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

// A cache hit or stale fallback must not turn a caller's cancelled operation
// into success. Exercise http.Client as a consumer, including error wrapping.
func TestCachePreservesCallerContextErrors(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		for _, duringRevalidation := range []bool{false, true} {
			t.Run(fmt.Sprintf("deadline=%t/revalidating=%t", deadline, duringRevalidation), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var cancel context.CancelFunc
					calls := 0
					base := stubRoundTripper(func(req *http.Request) (*http.Response, error) {
						calls++
						if calls == 1 {
							cc := "max-age=60"
							if duringRevalidation {
								cc = "max-age=0, stale-if-error=60"
							}
							return reply(200, "cached", "Cache-Control", cc, "ETag", `"v1"`), nil
						}
						if deadline {
							<-req.Context().Done()
						} else {
							cancel()
						}
						return nil, fmt.Errorf("upstream: %w", req.Context().Err())
					})
					cache := must(NewCacheTransport(base, nil))
					defer cache.Close()
					get(t, cache, itemsURL)

					var ctx context.Context
					want := context.Canceled
					if deadline {
						ctx, cancel = context.WithTimeout(context.Background(), time.Second)
						want = context.DeadlineExceeded
					} else {
						ctx, cancel = context.WithCancel(context.Background())
					}
					defer cancel()
					if !duringRevalidation {
						if deadline {
							<-ctx.Done()
						} else {
							cancel()
						}
					}
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, itemsURL, nil)
					if err != nil {
						t.Fatal(err)
					}
					client := &http.Client{Transport: cache}
					resp, err := client.Do(req)
					if resp != nil {
						resp.Body.Close()
					}
					if !errors.Is(err, want) || resp != nil {
						t.Fatalf("response=%v error=%v; want no response and %v", resp, err, want)
					}
					wantCalls := 1
					if duringRevalidation {
						wantCalls = 2
					}
					if calls != wantCalls {
						t.Fatalf("origin calls=%d; want %d", calls, wantCalls)
					}
				})
			})
		}
	}
}

func TestCacheStaleFallbackForLocalRefusal(t *testing.T) {
	for _, refusal := range []error{ErrCircuitOpen, ErrRateLimited, ErrLimiterTimeout} {
		t.Run(refusal.Error(), func(t *testing.T) {
			calls := 0
			base := stubRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return reply(200, "cached", "Cache-Control", "max-age=0, stale-if-error=60", "ETag", `"v1"`), nil
				}
				return nil, refusal
			})
			cache := must(NewCacheTransport(base, nil))
			defer cache.Close()
			get(t, cache, itemsURL)
			status, body, headers := get(t, cache, itemsURL)
			if status != 200 || body != "cached" || headers.Get("Cache-Status") != "httpx; hit; detail=stale-if-error" {
				t.Fatalf("fallback: status=%d body=%q headers=%v", status, body, headers)
			}
		})
	}
}
