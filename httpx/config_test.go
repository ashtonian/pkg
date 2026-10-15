//go:build unit

package httpx

import (
	"errors"
	"math"
	"net/http"
	"testing"
	"time"
)

func TestConstructorsRejectInvalidConfig(t *testing.T) {
	base := http.DefaultTransport
	tests := []struct {
		name  string
		build func() error
	}{
		{"client negative timeout", func() error { _, err := NewClient(WithTimeout(-time.Second)); return err }},
		{"client nil transport", func() error { _, err := NewClient(WithTransport(nil)); return err }},
		{"standard invalid override", func() error { _, err := NewStandard(WithRetry(WithMaxAttempts(0))); return err }},

		{"base nil dialer", func() error { _, err := NewBaseTransport(WithDialer(nil)); return err }},
		{"base negative idle conns", func() error { _, err := NewBaseTransport(WithMaxIdleConns(-1)); return err }},
		{"base negative header timeout", func() error {
			_, err := NewBaseTransport(WithResponseHeaderTimeout(-time.Second))
			return err
		}},

		{"retry nil base", func() error { _, err := NewRetryTransport(nil); return err }},
		{"retry zero attempts", func() error { _, err := NewRetryTransport(base, WithMaxAttempts(0)); return err }},
		{"retry zero initial wait", func() error {
			_, err := NewRetryTransport(base, WithBackoff(0, time.Second, 2))
			return err
		}},
		{"retry max below initial", func() error {
			_, err := NewRetryTransport(base, WithBackoff(time.Second, time.Millisecond, 2))
			return err
		}},
		{"retry multiplier below one", func() error {
			_, err := NewRetryTransport(base, WithBackoff(time.Millisecond, time.Second, 0.5))
			return err
		}},
		{"retry NaN multiplier", func() error {
			_, err := NewRetryTransport(base, WithBackoff(time.Millisecond, time.Second, math.NaN()))
			return err
		}},
		{"retry jitter above one", func() error { _, err := NewRetryTransport(base, WithJitter(2)); return err }},
		{"retry nil condition", func() error { _, err := NewRetryTransport(base, WithRetryIf(nil)); return err }},
		{"retry non-status code", func() error { _, err := NewRetryTransport(base, WithRetryOn(42)); return err }},

		{"hedging nil base", func() error { _, err := NewHedgingTransport(nil, time.Millisecond); return err }},
		{"hedging zero delay", func() error { _, err := NewHedgingTransport(base, 0); return err }},
		{"hedging single request", func() error {
			_, err := NewHedgingTransport(base, time.Millisecond, WithMaxHedgedRequests(1))
			return err
		}},
		{"adaptive hedging negative percentile", func() error {
			_, err := NewAdaptiveHedgingTransport(base, -0.1)
			return err
		}},
		{"adaptive hedging percentile above one", func() error {
			_, err := NewAdaptiveHedgingTransport(base, 1.5)
			return err
		}},

		{"limiter nil base", func() error { _, err := NewLimiterTransport(nil, 1); return err }},
		{"limiter zero capacity", func() error { _, err := NewLimiterTransport(base, 0); return err }},
		{"limiter negative timeout", func() error {
			_, err := NewLimiterTransport(base, 1, WithAcquireTimeout(-time.Second))
			return err
		}},

		{"rate limit nil base", func() error { _, err := NewRateLimitTransport(nil, 10, 1); return err }},
		{"rate limit zero rps", func() error { _, err := NewRateLimitTransport(base, 0, 1); return err }},
		{"rate limit NaN rps", func() error { _, err := NewRateLimitTransport(base, math.NaN(), 1); return err }},
		{"rate limit zero burst", func() error { _, err := NewRateLimitTransport(base, 10, 0); return err }},
		{"rate limit zero buckets", func() error { _, err := NewRateLimitTransport(base, 10, 1, WithMaxBuckets(0)); return err }},

		{"circuit nil base", func() error { _, err := NewCircuitTransport(nil); return err }},
		{"circuit zero failure threshold", func() error {
			_, err := NewCircuitTransport(base, WithFailureThreshold(0))
			return err
		}},
		{"circuit zero success threshold", func() error {
			_, err := NewCircuitTransport(base, WithSuccessThreshold(0))
			return err
		}},
		{"circuit zero open timeout", func() error { _, err := NewCircuitTransport(base, WithOpenTimeout(0)); return err }},
		{"circuit nil classifier", func() error { _, err := NewCircuitTransport(base, WithIsFailure(nil)); return err }},
		{"circuit zero breakers", func() error { _, err := NewCircuitTransport(base, WithMaxBreakers(0)); return err }},

		{"compression nil base", func() error { _, err := NewCompressionTransport(nil); return err }},
		{"compression gzip level too high", func() error {
			_, err := NewCompressionTransport(base, WithGzipLevel(10))
			return err
		}},
		{"compression deflate level too low", func() error {
			_, err := NewCompressionTransport(base, WithDeflateLevel(-3))
			return err
		}},
		{"compression unregistered encoding", func() error {
			_, err := NewCompressionTransport(base, WithEncodings("not-an-encoding"))
			return err
		}},
		{"compression nil compressor", func() error {
			_, err := NewCompressionTransport(base, WithCompressor("x-custom", nil))
			return err
		}},
		{"compression nil pool", func() error {
			_, err := NewCompressionTransport(base, WithCompressionPool(nil))
			return err
		}},

		{"cache nil base", func() error { _, err := NewCacheTransport(nil, nil); return err }},
		{"cache negative stale-if-error", func() error {
			_, err := NewCacheTransport(base, nil, WithStaleIfError(-time.Second))
			return err
		}},
		{"cache zero body limit", func() error {
			_, err := NewCacheTransport(base, nil, WithMaxCacheBodyBytes(0))
			return err
		}},
		{"cache zero retention", func() error { _, err := NewCacheTransport(base, nil, WithMaxRetention(0)); return err }},
		{"cache zero revalidation concurrency", func() error {
			_, err := NewCacheTransport(base, nil, WithRevalidation(time.Second, 0))
			return err
		}},
		{"cache empty identity header", func() error {
			_, err := NewCacheTransport(base, nil, WithCacheIdentityHeaders(""))
			return err
		}},
		{"memory cache zero entries", func() error { _, err := NewMemoryCache(WithMaxEntries(0)); return err }},
		{"memory cache zero bytes", func() error { _, err := NewMemoryCache(WithMaxBytes(0)); return err }},

		{"auth nil base", func() error {
			_, err := NewAuthTransport(nil, AuthBinding{URL: "https://h", Provider: NewBearerTokenProvider("t")})
			return err
		}},
		{"auth no bindings", func() error { _, err := NewAuthTransport(base); return err }},
		{"auth nil provider", func() error { _, err := NewAuthTransport(base, AuthBinding{URL: "https://h"}); return err }},
		{"auth relative origin", func() error {
			_, err := NewAuthTransport(base, AuthBinding{URL: "/api", Provider: NewBearerTokenProvider("t")})
			return err
		}},
		{"auth non-http origin", func() error {
			_, err := NewAuthTransport(base, AuthBinding{URL: "ftp://h", Provider: NewBearerTokenProvider("t")})
			return err
		}},
		{"auth origin with userinfo", func() error {
			_, err := NewAuthTransport(base, AuthBinding{URL: "https://u:p@h", Provider: NewBearerTokenProvider("t")})
			return err
		}},
		{"auth duplicate origin", func() error {
			_, err := NewAuthTransport(base,
				AuthBinding{URL: "https://h/a", Provider: NewBearerTokenProvider("a")},
				AuthBinding{URL: "https://H:443/b", Provider: NewBearerTokenProvider("b")})
			return err
		}},
		{"client auth bad origin", func() error {
			_, err := NewClient(WithAuth("not a url", NewBearerTokenProvider("t")))
			return err
		}},
		{"metrics nil base", func() error { _, err := NewMetricsTransport(nil, nil); return err }},
		{"logging nil base", func() error { _, err := NewLoggingTransport(nil, nil); return err }},

		{"chain nil layer", func() error { _, err := NewChain(nil).Build(base); return err }},
		{"chain invalid layer", func() error { _, err := NewChain(WithRetryT(WithMaxAttempts(0))).Build(base); return err }},
		{"chain negative client timeout", func() error { _, err := NewChain().Client(base, -time.Second); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.build()
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("expected ErrInvalidConfig, got %v", err)
			}
		})
	}
}

func TestNewClientNamesInvalidLayer(t *testing.T) {
	_, err := NewClient(WithRetry(WithMaxAttempts(0)))
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got != "httpx: retry: httpx: invalid configuration: retry: WithMaxAttempts must be >= 1, got 0" {
		t.Errorf("unexpected error text: %q", got)
	}
}

func TestDefaultsValidate(t *testing.T) {
	if _, err := NewStandard(); err != nil {
		t.Fatalf("NewStandard defaults: %v", err)
	}
	for name, c := range map[string]Chain{
		"default":   DefaultChain(),
		"resilient": ResilientChain(),
		"api":       APIClientChain(),
	} {
		if _, err := c.Build(nil); err != nil {
			t.Errorf("%s chain: %v", name, err)
		}
	}
}
