package httpx

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// HedgingTransport implements request hedging for tail latency reduction.
type HedgingTransport struct {
	base    http.RoundTripper
	delay   atomic.Int64 // nanoseconds; use getDelay/setDelay
	max     int
	hedgeIf func(*http.Request) bool
}

func (t *HedgingTransport) getDelay() time.Duration {
	return time.Duration(t.delay.Load())
}

func (t *HedgingTransport) setDelay(d time.Duration) {
	t.delay.Store(int64(d))
}

// HedgingOption configures a HedgingTransport.
type HedgingOption func(*HedgingTransport)

// NewHedgingTransport creates a new hedging transport.
//
// delay is the time to wait before sending a hedge request.
// By default, up to 2 concurrent requests are allowed.
func NewHedgingTransport(base http.RoundTripper, delay time.Duration, opts ...HedgingOption) (*HedgingTransport, error) {
	if base == nil {
		return nil, invalidConfig("hedging: base transport is nil")
	}
	if delay <= 0 {
		return nil, invalidConfig("hedging: delay must be > 0, got %s", delay)
	}

	t := &HedgingTransport{
		base: base,
		max:  2,
	}
	t.setDelay(delay)

	// Default: only hedge GET requests
	t.hedgeIf = func(req *http.Request) bool {
		return req.Method == http.MethodGet
	}

	for _, opt := range opts {
		opt(t)
	}
	if err := t.validate(); err != nil {
		return nil, err
	}

	return t, nil
}

func (t *HedgingTransport) validate() error {
	var errs []error
	if t.max < 2 {
		errs = append(errs, invalidConfig("hedging: WithMaxHedgedRequests must be >= 2, got %d", t.max))
	}
	if t.hedgeIf == nil {
		errs = append(errs, invalidConfig("hedging: WithHedgeIf requires a non-nil function"))
	}
	return errors.Join(errs...)
}

// WithMaxHedgedRequests sets the maximum number of concurrent requests.
func WithMaxHedgedRequests(n int) HedgingOption {
	return func(t *HedgingTransport) {
		t.max = n
	}
}

// WithHedgeIf sets a function to determine whether to hedge a request.
func WithHedgeIf(fn func(*http.Request) bool) HedgingOption {
	return func(t *HedgingTransport) {
		t.hedgeIf = fn
	}
}

// RoundTrip implements http.RoundTripper.
//
// Each attempt runs under its own cancellable context. The first attempt to
// return a response wins: every other attempt is cancelled and its response,
// if one still arrives, is drained and closed. The winner's context stays
// alive until the caller finishes with its body, so a delayed or streaming
// body stays readable.
func (t *HedgingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.hedgeIf(req) {
		return t.base.RoundTrip(req)
	}

	if req.Body != nil && req.Body != http.NoBody && req.GetBody == nil {
		// A hedge runs beside the first attempt, so it needs its own copy of
		// the body before the first has finished sending it; a body without
		// GetBody is sent once, unhedged.
		return t.base.RoundTrip(req)
	}
	first := req.Clone(req.Context())
	body := newReplayBody(first, 0)

	type result struct {
		attempt int
		resp    *http.Response
		err     error
	}
	results := make(chan result, t.max) // one slot per attempt: senders never block
	var cancels []context.CancelFunc    // indexed by attempt; only this goroutine touches it
	launch := func(attemptReq *http.Request) {
		ctx, cancel := context.WithCancel(req.Context())
		attempt := len(cancels)
		cancels = append(cancels, cancel)
		go func() {
			resp, err := t.base.RoundTrip(attemptReq.WithContext(ctx))
			results <- result{attempt: attempt, resp: resp, err: err}
		}()
	}
	// abandon cancels every attempt except keep and cleans up the responses
	// still in flight, so no loser's body or context outlives the call.
	abandon := func(keep, pending int) {
		for i, cancel := range cancels {
			if i != keep {
				cancel()
			}
		}
		go func() {
			for range pending {
				if r := <-results; r.resp != nil {
					drainAndClose(r.resp)
				}
			}
		}()
	}

	launch(first)
	pending := 1
	timer := time.NewTimer(t.getDelay())
	defer timer.Stop()

	for {
		select {
		case r := <-results:
			pending--
			if r.err == nil {
				abandon(r.attempt, pending)
				return releaseWhenBodyDone(r.resp, cancels[r.attempt]), nil
			}
			cancels[r.attempt]()
			if pending == 0 {
				return nil, r.err
			}

		case <-timer.C:
			if len(cancels) >= t.max {
				continue
			}
			next, err := body.next(req)
			if err != nil {
				// No further hedge can be built; keep waiting on those in flight.
				continue
			}
			launch(next)
			pending++
			if len(cancels) < t.max {
				timer.Reset(t.getDelay())
			}

		case <-req.Context().Done():
			abandon(-1, pending)
			return nil, req.Context().Err()
		}
	}
}

// AdaptiveHedgingTransport adjusts hedge delay based on observed latencies.
type AdaptiveHedgingTransport struct {
	*HedgingTransport
	mu         sync.RWMutex
	latencies  []time.Duration
	percentile float64
	maxSamples int
}

// NewAdaptiveHedgingTransport creates a hedging transport that adapts delay based on latency.
//
// percentile selects the observed latency used as the hedge delay and must be
// within (0, 1].
func NewAdaptiveHedgingTransport(base http.RoundTripper, percentile float64, opts ...HedgingOption) (*AdaptiveHedgingTransport, error) {
	if !(percentile > 0 && percentile <= 1) {
		return nil, invalidConfig("adaptive hedging: percentile must be within (0, 1], got %v", percentile)
	}
	inner, err := NewHedgingTransport(base, 100*time.Millisecond, opts...)
	if err != nil {
		return nil, err
	}
	return &AdaptiveHedgingTransport{
		HedgingTransport: inner,
		percentile:       percentile,
		maxSamples:       1000,
		latencies:        make([]time.Duration, 0, 1000),
	}, nil
}

// RoundTrip implements http.RoundTripper with adaptive delay.
func (t *AdaptiveHedgingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Update delay atomically based on observed latencies.
	t.HedgingTransport.setDelay(t.calculateDelay())

	start := time.Now()
	resp, err := t.HedgingTransport.RoundTrip(req)
	t.recordLatency(time.Since(start))

	return resp, err
}

func (t *AdaptiveHedgingTransport) recordLatency(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.latencies) >= t.maxSamples {
		// Remove oldest sample
		t.latencies = t.latencies[1:]
	}
	t.latencies = append(t.latencies, d)
}

func (t *AdaptiveHedgingTransport) calculateDelay() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.latencies) < 10 {
		return t.HedgingTransport.getDelay()
	}

	// Sort latencies to find percentile - O(n log n)
	sorted := make([]time.Duration, len(t.latencies))
	copy(sorted, t.latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	idx := int(float64(len(sorted)) * t.percentile)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}

	return sorted[idx]
}

// CloseIdleConnections forwards to the wrapped transport.
func (t *HedgingTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *HedgingTransport) Unwrap() http.RoundTripper { return t.base }
