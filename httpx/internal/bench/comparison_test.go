//go:build integration

// Package bench contains head-to-head benchmarks of httpx against popular Go HTTP libraries.
//
// This is a separate module to isolate benchmark dependencies (resty, go-retryablehttp,
// gobreaker) from the main httpx module. Users of httpx never pull in these dependencies.
//
// Run:
//
//	go test -tags=integration -bench=. -benchmem
//
// Output groups like:
//
//	BenchmarkRetry/httpx
//	BenchmarkRetry/hashicorp_retryablehttp
//	BenchmarkRetry/resty
//
// making direct comparisons easy to read.
package bench

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
	"github.com/go-resty/resty/v2"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/sony/gobreaker"
)

// ----------------------------------------------------------------------------
// Shared helpers
// ----------------------------------------------------------------------------

// okServer returns a minimal JSON server for benchmarking.
func okServer(b *testing.B) *httptest.Server {
	b.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "max-age=3600")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	b.Cleanup(s.Close)
	return s
}

// drain reads and closes a standard *http.Response body.
func drain(b *testing.B, resp *http.Response) {
	b.Helper()
	_, readErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		b.Fatal(err)
	}
}

// ----------------------------------------------------------------------------
// Retry: httpx vs hashicorp/go-retryablehttp vs go-resty/resty
//
// All configured with max 3 attempts, 100ms–1s backoff.
// Server always returns 200, so no retries fire — this measures the
// per-request overhead of having retry enabled.
// ----------------------------------------------------------------------------

func BenchmarkRetry(b *testing.B) {
	b.Run("httpx", func(b *testing.B) {
		server := okServer(b)
		client := must(httpx.NewClient(
			httpx.WithRetry(
				httpx.WithMaxAttempts(3),
				httpx.WithBackoff(100*time.Millisecond, time.Second, 2.0),
			),
		))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			drain(b, resp)
		}
	})

	b.Run("hashicorp_retryablehttp", func(b *testing.B) {
		server := okServer(b)
		client := retryablehttp.NewClient()
		client.RetryMax = 2 // Initial attempt plus two retries.
		client.RetryWaitMin = 100 * time.Millisecond
		client.RetryWaitMax = time.Second
		client.Logger = nil
		client.HTTPClient.Timeout = 30 * time.Second
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			drain(b, resp)
		}
	})

	b.Run("resty", func(b *testing.B) {
		server := okServer(b)
		client := resty.New().
			SetRetryCount(2).
			SetRetryWaitTime(100 * time.Millisecond).
			SetRetryMaxWaitTime(time.Second).
			SetTimeout(30 * time.Second)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.R().Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			_ = resp.Body()
		}
	})
}

// ----------------------------------------------------------------------------
// Circuit Breaker: httpx vs sony/gobreaker
//
// Closed: normal operation, all requests succeed.
// Open:   circuit tripped, requests rejected without network I/O.
// ----------------------------------------------------------------------------

func BenchmarkCircuitBreaker(b *testing.B) {
	b.Run("Closed", func(b *testing.B) {
		server := okServer(b)

		b.Run("httpx", func(b *testing.B) {
			client := must(httpx.NewClient(
				httpx.WithCircuitBreaker(
					httpx.WithFailureThreshold(5),
					httpx.WithSuccessThreshold(2),
					httpx.WithOpenTimeout(30*time.Second),
				),
			))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				resp, err := client.Get(server.URL)
				if err != nil {
					b.Fatal(err)
				}
				drain(b, resp)
			}
		})

		b.Run("sony_gobreaker", func(b *testing.B) {
			cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
				Name:        "bench",
				MaxRequests: 2,
				Interval:    30 * time.Second,
				Timeout:     30 * time.Second,
				ReadyToTrip: func(counts gobreaker.Counts) bool {
					return counts.ConsecutiveFailures >= 5
				},
			})
			client := &http.Client{Timeout: 30 * time.Second}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, err := cb.Execute(func() (any, error) {
					resp, err := client.Get(server.URL)
					if err != nil {
						return nil, err
					}
					drain(b, resp)
					return resp, nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	})

	b.Run("Open", func(b *testing.B) {
		b.Run("httpx", func(b *testing.B) {
			// Trip the same origin that the timed loop uses. An empty request
			// has no origin and does not trip a per-origin circuit breaker.
			base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Header: make(http.Header), Request: req}, nil
			})
			transport := must(httpx.NewCircuitTransport(base,
				httpx.WithFailureThreshold(1), httpx.WithOpenTimeout(time.Hour),
			))
			req := must(http.NewRequest(http.MethodGet, "http://benchmark.invalid", nil))
			resp, err := transport.RoundTrip(req)
			if err != nil {
				b.Fatal(err)
			}
			drain(b, resp)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				req := must(http.NewRequest(http.MethodGet, "http://benchmark.invalid", nil))
				_, err := transport.RoundTrip(req)
				if !errors.Is(err, httpx.ErrCircuitOpen) {
					b.Fatal("expected circuit to be open")
				}
			}
		})

		b.Run("sony_gobreaker", func(b *testing.B) {
			cb := gobreaker.NewCircuitBreaker(gobreaker.Settings{
				Name:        "bench",
				MaxRequests: 1,
				Interval:    time.Hour,
				Timeout:     time.Hour,
				ReadyToTrip: func(counts gobreaker.Counts) bool {
					return counts.ConsecutiveFailures >= 1
				},
			})
			if _, err := cb.Execute(func() (any, error) { return nil, io.EOF }); !errors.Is(err, io.EOF) {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				_, err := cb.Execute(func() (any, error) { return nil, nil })
				if err != gobreaker.ErrOpenState {
					b.Fatal("expected circuit to be open")
				}
			}
		})
	})
}

// ----------------------------------------------------------------------------
// Full Client: httpx (NewClient + Chain) vs resty
//
// Each library configured with retry + timeout. httpx also enables
// compression, codecs, circuit breaker, and metrics — demonstrating that
// the cost of the configured stacks under this workload.
// ----------------------------------------------------------------------------

func BenchmarkFullClient(b *testing.B) {
	server := okServer(b)

	b.Run("httpx", func(b *testing.B) {
		client := must(httpx.NewClient(
			httpx.WithCompression(),
			httpx.WithRetry(
				httpx.WithMaxAttempts(3),
				httpx.WithBackoff(100*time.Millisecond, time.Second, 2.0),
			),
			httpx.WithCircuitBreaker(httpx.WithFailureThreshold(5)),
			httpx.WithMetrics(nil),
			httpx.WithTimeout(30*time.Second),
		))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			drain(b, resp)
		}
	})

	b.Run("httpx_Chain", func(b *testing.B) {
		chain := httpx.NewChain(
			httpx.WithCompressionT(),
			httpx.WithCircuitBreakerT(httpx.WithFailureThreshold(5)),
			httpx.WithRetryT(
				httpx.WithMaxAttempts(3),
				httpx.WithBackoff(100*time.Millisecond, time.Second, 2.0),
			),
			httpx.WithMetricsT(nil),
		)
		client := must(chain.Client(nil, 30*time.Second))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			drain(b, resp)
		}
	})

	b.Run("resty", func(b *testing.B) {
		client := resty.New().
			SetRetryCount(2).
			SetRetryWaitTime(100 * time.Millisecond).
			SetRetryMaxWaitTime(time.Second).
			SetTimeout(30 * time.Second)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.R().Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			_ = resp.Body()
		}
	})
}

// ----------------------------------------------------------------------------
// Cache: httpx (with RFC 9111 cache) vs resty (no built-in cache)
//
// httpx serves repeated requests from an in-memory cache, avoiding network I/O.
// resty has no HTTP caching, so every request hits the server.
// This compares avoiding a network round trip with making one; the paths do different work.
// ----------------------------------------------------------------------------

func BenchmarkCache(b *testing.B) {
	server := okServer(b)

	b.Run("httpx_Hit", func(b *testing.B) {
		client := must(httpx.NewClient(httpx.WithCache(nil)))
		resp, err := client.Get(server.URL)
		if err != nil {
			b.Fatal(err)
		}
		drain(b, resp)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			drain(b, resp)
		}
	})

	b.Run("resty_NoCache", func(b *testing.B) {
		client := resty.New().SetTimeout(30 * time.Second)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.R().Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			_ = resp.Body()
		}
	})
}

// ----------------------------------------------------------------------------
// Allocation: measures memory allocations for common httpx operations.
// ----------------------------------------------------------------------------

func BenchmarkAllocation(b *testing.B) {
	b.Run("BaseRequest", func(b *testing.B) {
		server := okServer(b)
		client := must(httpx.NewClient())
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			drain(b, resp)
		}
	})

	b.Run("CachedRequest", func(b *testing.B) {
		server := okServer(b)
		client := must(httpx.NewClient(httpx.WithCache(nil)))
		resp, err := client.Get(server.URL)
		if err != nil {
			b.Fatal(err)
		}
		drain(b, resp)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			resp, err := client.Get(server.URL)
			if err != nil {
				b.Fatal(err)
			}
			drain(b, resp)
		}
	})

	b.Run("ChainBuild", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			chain := httpx.NewChain(
				httpx.WithCompressionT(),
				httpx.WithCircuitBreakerT(),
				httpx.WithRetryT(httpx.WithMaxAttempts(3)),
			)
			_ = must(chain.Build(nil))
		}
	})
}
