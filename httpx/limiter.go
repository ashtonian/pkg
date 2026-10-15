package httpx

import (
	"context"
	"net/http"
	"time"
)

// LimiterTransport limits concurrent in-flight requests. A request holds its
// slot from before it is sent until its response body is read to the end or
// closed; callers must close response bodies to free their slot.
type LimiterTransport struct {
	base      http.RoundTripper
	sem       chan struct{}
	timeout   time.Duration
	onBlocked func()
}

// LimiterOption configures a LimiterTransport.
type LimiterOption func(*LimiterTransport)

// NewLimiterTransport creates a new concurrency limiter transport.
func NewLimiterTransport(base http.RoundTripper, maxConcurrent int, opts ...LimiterOption) (*LimiterTransport, error) {
	if base == nil {
		return nil, invalidConfig("limiter: base transport is nil")
	}
	if maxConcurrent < 1 {
		return nil, invalidConfig("limiter: max concurrent requests must be >= 1, got %d", maxConcurrent)
	}

	t := &LimiterTransport{
		base: base,
		sem:  make(chan struct{}, maxConcurrent),
	}

	for _, opt := range opts {
		opt(t)
	}
	if t.timeout < 0 {
		return nil, invalidConfig("limiter: WithAcquireTimeout must be >= 0, got %s", t.timeout)
	}

	return t, nil
}

// WithAcquireTimeout sets the timeout for acquiring a slot.
// Default is 0 (wait until a slot is available or the request context ends).
func WithAcquireTimeout(d time.Duration) LimiterOption {
	return func(t *LimiterTransport) {
		t.timeout = d
	}
}

// WithOnBlocked sets a callback for when a request blocks waiting for a slot.
func WithOnBlocked(fn func()) LimiterOption {
	return func(t *LimiterTransport) {
		t.onBlocked = fn
	}
}

// RoundTrip implements http.RoundTripper.
func (t *LimiterTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Try to acquire a slot
	if err := t.acquire(req.Context()); err != nil {
		closeRequestBody(req)
		return nil, err
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.release()
		return nil, err
	}
	// The slot is held until the caller is done with the body, so the cap
	// bounds concurrent downloads, not just requests awaiting headers.
	return releaseWhenBodyDone(resp, t.release), nil
}

func (t *LimiterTransport) acquire(ctx context.Context) error {
	// Fast path: try to acquire immediately
	select {
	case t.sem <- struct{}{}:
		return nil
	default:
		// Need to wait
	}

	// Notify that we're blocked
	if t.onBlocked != nil {
		t.onBlocked()
	}

	// Wait for a slot with optional timeout
	if t.timeout > 0 {
		timer := time.NewTimer(t.timeout)
		defer timer.Stop()

		select {
		case t.sem <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return ErrLimiterTimeout
		}
	}

	// No timeout - wait indefinitely
	select {
	case t.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *LimiterTransport) release() {
	<-t.sem
}

// Available returns the number of available slots.
func (t *LimiterTransport) Available() int {
	return cap(t.sem) - len(t.sem)
}

// InFlight returns the number of in-flight requests.
func (t *LimiterTransport) InFlight() int {
	return len(t.sem)
}

// Capacity returns the maximum number of concurrent requests.
func (t *LimiterTransport) Capacity() int {
	return cap(t.sem)
}

// CloseIdleConnections forwards to the wrapped transport.
func (t *LimiterTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *LimiterTransport) Unwrap() http.RoundTripper { return t.base }
