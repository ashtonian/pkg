package httpx

import (
	"fmt"
	"net/http"
	"time"
)

// TransportFunc wraps a transport, returning an error when its configuration
// is invalid. This is the building block for the Chain API.
type TransportFunc func(http.RoundTripper) (http.RoundTripper, error)

// Chain provides a fluent API for composing transports.
// It's an alternative to the decorator pattern that makes
// the transport stack more explicit and readable.
//
// Example:
//
//	transport, err := httpx.NewChain(
//	    httpx.WithCompressionT(),
//	    httpx.WithRetryT(httpx.WithMaxAttempts(3)),
//	    httpx.WithCircuitBreakerT(),
//	).Build(nil)
type Chain []TransportFunc

// NewChain creates a new transport chain.
// Transports are applied in order: first added is innermost (closest to base).
// Order is semantics, not style: a layer sees what the layers outside it
// send and counts what the layers inside it return. A circuit breaker inside
// retry counts every attempt, outside it only logical outcomes; a limiter or
// rate limit inside retry and hedging budgets every network attempt, outside
// them one logical call; a cache outside the breaker and the budgets serves
// hits without them, inside them not at all while the breaker is open; and a
// cache outside the auth layer would partition before the credential is set.
// NewClient's order (compression, circuit breaker, limiter, rate limit, retry,
// hedging, cache, auth, metrics, logging) is the one to start from.
func NewChain(funcs ...TransportFunc) Chain {
	return Chain(funcs)
}

// Append adds transport functions to the chain.
// Returns a new chain without modifying the original.
func (c Chain) Append(funcs ...TransportFunc) Chain {
	newChain := make(Chain, len(c)+len(funcs))
	copy(newChain, c)
	copy(newChain[len(c):], funcs)
	return newChain
}

// Prepend adds transport functions to the beginning of the chain.
// Returns a new chain without modifying the original.
func (c Chain) Prepend(funcs ...TransportFunc) Chain {
	newChain := make(Chain, len(c)+len(funcs))
	copy(newChain, funcs)
	copy(newChain[len(funcs):], c)
	return newChain
}

// Build applies all transport functions to the base transport.
// If base is nil, a default [NewBaseTransport] is used.
func (c Chain) Build(base http.RoundTripper) (http.RoundTripper, error) {
	if base == nil {
		b, err := NewBaseTransport()
		if err != nil {
			return nil, err
		}
		base = b
	}

	// Apply in order: first is innermost
	for i, fn := range c {
		if fn == nil {
			return nil, invalidConfig("chain: layer %d is nil", i)
		}
		next, err := fn(base)
		if err != nil {
			return nil, fmt.Errorf("httpx: chain layer %d: %w", i, err)
		}
		base = next
	}

	return base, nil
}

// Client creates an http.Client with the transport chain.
// If base is nil, a default [NewBaseTransport] is used. A negative timeout is
// rejected; zero disables the client timeout.
func (c Chain) Client(base http.RoundTripper, timeout time.Duration) (*http.Client, error) {
	if timeout < 0 {
		return nil, invalidConfig("chain: client timeout must be >= 0, got %s", timeout)
	}
	transport, err := c.Build(base)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}, nil
}

// Transport function builders for use with Chain.

// WithCompressionT returns a TransportFunc that adds compression.
func WithCompressionT(opts ...CompressionOption) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewCompressionTransport(base, opts...))
	}
}

// WithCacheT returns a TransportFunc that adds caching.
func WithCacheT(store CacheStore, opts ...CacheOption) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewCacheTransport(base, store, opts...))
	}
}

// WithRetryT returns a TransportFunc that adds retry with backoff.
func WithRetryT(opts ...RetryOption) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewRetryTransport(base, opts...))
	}
}

// WithMetricsT returns a TransportFunc that adds metrics.
func WithMetricsT(hook MetricsHook) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewMetricsTransport(base, hook))
	}
}

// WithLoggingT returns a TransportFunc that adds logging.
func WithLoggingT(hook LoggingHook, opts ...LoggingOption) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewLoggingTransport(base, hook, opts...))
	}
}

// WithLimiterT returns a TransportFunc that adds concurrency limiting.
func WithLimiterT(maxConcurrent int, opts ...LimiterOption) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewLimiterTransport(base, maxConcurrent, opts...))
	}
}

// WithRateLimitT returns a TransportFunc that adds rate limiting.
func WithRateLimitT(rps float64, burst int, opts ...RateLimitOption) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewRateLimitTransport(base, rps, burst, opts...))
	}
}

// WithCircuitBreakerT returns a TransportFunc that adds circuit breaker.
func WithCircuitBreakerT(opts ...CircuitOption) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewCircuitTransport(base, opts...))
	}
}

// WithHedgingT returns a TransportFunc that adds request hedging.
func WithHedgingT(delay time.Duration, opts ...HedgingOption) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewHedgingTransport(base, delay, opts...))
	}
}

// WithAuthT returns a TransportFunc that binds provider to the origin of
// originURL (see WithAuth).
func WithAuthT(originURL string, provider AuthProvider) TransportFunc {
	return func(base http.RoundTripper) (http.RoundTripper, error) {
		return asRoundTripper(NewAuthTransport(base, AuthBinding{URL: originURL, Provider: provider}))
	}
}

// Preset chains for common use cases.

// DefaultChain returns compression, a circuit breaker and retry, in the
// NewClient order: the breaker is inside retry, so it counts every attempt.
func DefaultChain() Chain {
	return NewChain(
		WithCompressionT(),
		WithCircuitBreakerT(),
		WithRetryT(WithMaxAttempts(3)),
	)
}

// ResilientChain returns a chain optimized for reliability, in NewClient's
// order: every attempt passes the breaker and holds a limiter slot, and the
// cache answers — stale if need be — even while the breaker is open.
func ResilientChain() Chain {
	return NewChain(
		WithCompressionT(),
		WithCircuitBreakerT(
			WithFailureThreshold(3),
			WithOpenTimeout(time.Minute),
		),
		WithLimiterT(50),
		WithRetryT(
			WithMaxAttempts(5),
			WithBackoff(100*time.Millisecond, 30*time.Second, 2.0),
		),
		WithCacheT(nil, WithStaleIfError(10*time.Minute)),
	)
}

// APIClientChain returns a chain for REST API clients: retry inside the cache,
// so a hit is never retried and a miss is.
func APIClientChain() Chain {
	return NewChain(
		WithCompressionT(),
		WithRetryT(WithMaxAttempts(3)),
		WithCacheT(nil),
	)
}
