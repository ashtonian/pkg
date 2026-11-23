package httpx

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// HedgingTransport implements request hedging for tail latency reduction.
type HedgingTransport struct {
	base    http.RoundTripper
	delay   time.Duration
	max     int
	hedgeIf func(*http.Request) bool
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
		base:  base,
		delay: delay,
		max:   2,
	}

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
	if !t.eligible(req) {
		return t.base.RoundTrip(req)
	}
	return t.hedge(req, t.delay)
}

// eligible reports whether req is hedged: the predicate selects it and its
// body, if any, can be replayed. A hedge runs beside the first attempt, so
// it needs its own copy of the body before the first has finished sending
// it; a body without GetBody is sent once, unhedged.
func (t *HedgingTransport) eligible(req *http.Request) bool {
	if !t.hedgeIf(req) {
		return false
	}
	return req.Body == nil || req.Body == http.NoBody || req.GetBody != nil
}

// hedge sends an eligible req, launching a further attempt each delay
// while one is pending, up to the cap.
func (t *HedgingTransport) hedge(req *http.Request, delay time.Duration) (*http.Response, error) {
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
	timer := time.NewTimer(delay)
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
				timer.Reset(delay)
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
	window *latencyWindow
}

// The adaptive delay starts at adaptiveInitialDelay and, once
// adaptiveMinSamples latencies are recorded, is the configured percentile
// of the last adaptiveWindow, recomputed every adaptiveRecomputeEvery
// samples.
const (
	adaptiveInitialDelay   = 100 * time.Millisecond
	adaptiveWindow         = 1000
	adaptiveMinSamples     = 10
	adaptiveRecomputeEvery = 64
)

// NewAdaptiveHedgingTransport creates a hedging transport that adapts delay based on latency.
//
// percentile selects the observed latency used as the hedge delay and must be
// within (0, 1]. The latencies are those of the requests the transport
// hedges, from the first attempt until a response arrives; requests it does
// not hedge, and calls that end in an error or cancellation, are not
// counted.
func NewAdaptiveHedgingTransport(base http.RoundTripper, percentile float64, opts ...HedgingOption) (*AdaptiveHedgingTransport, error) {
	if !(percentile > 0 && percentile <= 1) {
		return nil, invalidConfig("adaptive hedging: percentile must be within (0, 1], got %v", percentile)
	}
	inner, err := NewHedgingTransport(base, adaptiveInitialDelay, opts...)
	if err != nil {
		return nil, err
	}
	return &AdaptiveHedgingTransport{
		HedgingTransport: inner,
		window:           newLatencyWindow(percentile, adaptiveWindow, adaptiveInitialDelay),
	}, nil
}

// RoundTrip implements http.RoundTripper with adaptive delay.
func (t *AdaptiveHedgingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The delay controls only eligible requests, so only they train it.
	if !t.eligible(req) {
		return t.base.RoundTrip(req)
	}
	start := time.Now()
	resp, err := t.hedge(req, t.window.delay())
	if err == nil {
		t.window.record(time.Since(start))
	}
	return resp, err
}

// latencyWindow holds the last len(samples) latencies and publishes their
// percentile, so a request reads its delay with one atomic load and
// recording never waits on it. A recording claims its slot with an atomic
// counter. Every adaptiveRecomputeEvery samples (every sample until then)
// the recording that reaches the boundary computes the percentile into an
// owned scratch buffer; one already computing makes another skip, and the
// next boundary includes what it skipped.
type latencyWindow struct {
	percentile float64
	samples    []atomic.Int64 // nanoseconds; negative until first written
	recorded   atomic.Uint64
	published  atomic.Int64 // nanoseconds

	mu      sync.Mutex // held while computing; guards scratch
	scratch []time.Duration
}

func newLatencyWindow(percentile float64, size int, initial time.Duration) *latencyWindow {
	w := &latencyWindow{
		percentile: percentile,
		samples:    make([]atomic.Int64, size),
		scratch:    make([]time.Duration, 0, size),
	}
	for i := range w.samples {
		w.samples[i].Store(-1)
	}
	w.published.Store(int64(initial))
	return w
}

func (w *latencyWindow) delay() time.Duration {
	return time.Duration(w.published.Load())
}

func (w *latencyWindow) record(d time.Duration) {
	n := w.recorded.Add(1)
	w.samples[(n-1)%uint64(len(w.samples))].Store(int64(d))
	if n < adaptiveMinSamples || (n > adaptiveRecomputeEvery && n%adaptiveRecomputeEvery != 0) {
		return
	}
	if !w.mu.TryLock() {
		return
	}
	defer w.mu.Unlock()
	w.scratch = w.scratch[:0]
	for i := range w.samples {
		// A slot claimed but not yet written still reads negative or its
		// previous value; either is at most one in-flight sample per
		// concurrent recording.
		if v := w.samples[i].Load(); v >= 0 {
			w.scratch = append(w.scratch, time.Duration(v))
		}
	}
	if len(w.scratch) < adaptiveMinSamples {
		return
	}
	slices.Sort(w.scratch)
	w.published.Store(int64(w.scratch[min(int(float64(len(w.scratch))*w.percentile), len(w.scratch)-1)]))
}

// CloseIdleConnections forwards to the wrapped transport.
func (t *HedgingTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *HedgingTransport) Unwrap() http.RoundTripper { return t.base }
