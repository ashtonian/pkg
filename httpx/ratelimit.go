package httpx

import (
	"errors"
	"fmt"
	"net/http"

	"golang.org/x/time/rate"
)

// RateLimitTransport throttles requests with one token bucket per origin
// (scheme, host and port): each origin may send rps requests per second with
// bursts up to burst. At most DefaultMaxOrigins buckets are kept
// (WithMaxBuckets); the least recently used origin is dropped beyond that and
// starts with a full bucket on its next request. A request over the limit
// fails with ErrRateLimited, or waits for a token under WithWait.
type RateLimitTransport struct {
	base       http.RoundTripper
	rps        rate.Limit
	burst      int
	wait       bool
	maxBuckets int

	buckets *originStates[*rate.Limiter]
}

// RateLimitOption configures a RateLimitTransport.
type RateLimitOption func(*RateLimitTransport)

// NewRateLimitTransport creates a per-origin rate limiting transport.
func NewRateLimitTransport(base http.RoundTripper, rps float64, burst int, opts ...RateLimitOption) (*RateLimitTransport, error) {
	if base == nil {
		return nil, invalidConfig("rate limit: base transport is nil")
	}
	t := &RateLimitTransport{
		base:       base,
		rps:        rate.Limit(rps),
		burst:      burst,
		maxBuckets: DefaultMaxOrigins,
	}
	for _, opt := range opts {
		opt(t)
	}
	var errs []error
	if !(rps > 0) {
		errs = append(errs, invalidConfig("rate limit: requests per second must be > 0, got %v", rps))
	}
	if burst < 1 {
		errs = append(errs, invalidConfig("rate limit: burst must be >= 1, got %d", burst))
	}
	if t.maxBuckets < 1 {
		errs = append(errs, invalidConfig("rate limit: WithMaxBuckets must be >= 1, got %d", t.maxBuckets))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	t.buckets = newOriginStates(t.maxBuckets, func(Origin) *rate.Limiter {
		return rate.NewLimiter(t.rps, t.burst)
	}, nil)
	return t, nil
}

// WithWait makes a request over the limit wait for a token instead of
// failing. The wait honours the request context; a wait that would outlast
// the context's deadline fails with ErrRateLimited at once.
func WithWait() RateLimitOption {
	return func(t *RateLimitTransport) { t.wait = true }
}

// WithMaxBuckets caps how many origins keep a token bucket.
func WithMaxBuckets(n int) RateLimitOption {
	return func(t *RateLimitTransport) { t.maxBuckets = n }
}

// RoundTrip implements http.RoundTripper.
func (t *RateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin := OriginOf(req.URL)
	bucket := t.buckets.get(origin)
	if !t.wait {
		if !bucket.Allow() {
			closeRequestBody(req)
			return nil, fmt.Errorf("%w: %s", ErrRateLimited, origin)
		}
		return t.base.RoundTrip(req)
	}
	if err := bucket.Wait(req.Context()); err != nil {
		closeRequestBody(req)
		if ctxErr := req.Context().Err(); ctxErr != nil {
			return nil, fmt.Errorf("httpx: rate limit wait for %s: %w", origin, ctxErr)
		}
		return nil, fmt.Errorf("%w: %s: %v", ErrRateLimited, origin, err)
	}
	return t.base.RoundTrip(req)
}

// CloseIdleConnections forwards to the wrapped transport.
func (t *RateLimitTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *RateLimitTransport) Unwrap() http.RoundTripper { return t.base }
