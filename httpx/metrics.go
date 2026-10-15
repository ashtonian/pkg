package httpx

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// MetricsTransport reports each request and its response or error to a
// MetricsHook.
type MetricsTransport struct {
	base http.RoundTripper
	hook MetricsHook
}

// NewMetricsTransport creates a new metrics transport.
func NewMetricsTransport(base http.RoundTripper, hook MetricsHook) (*MetricsTransport, error) {
	if base == nil {
		return nil, invalidConfig("metrics: base transport is nil")
	}
	return &MetricsTransport{
		base: base,
		hook: metricsHookOrNoop(hook),
	}, nil
}

// RoundTrip implements http.RoundTripper.
func (t *MetricsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.hook.OnRequest(req)

	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	duration := time.Since(start)

	t.hook.OnResponse(req, resp, duration, err)

	return resp, err
}

// CountingHook is a simple MetricsHook that counts events.
// All fields are safe for concurrent use.
type CountingHook struct {
	Requests    atomic.Int64
	Responses   atomic.Int64
	Errors      atomic.Int64
	Retries     atomic.Int64
	CacheHits   atomic.Int64
	CacheMisses atomic.Int64
}

func (h *CountingHook) OnRequest(*http.Request) {
	h.Requests.Add(1)
}

func (h *CountingHook) OnResponse(_ *http.Request, _ *http.Response, _ time.Duration, err error) {
	h.Responses.Add(1)
	if err != nil {
		h.Errors.Add(1)
	}
}

func (h *CountingHook) OnRetry(_ *http.Request, _ int, _ error) {
	h.Retries.Add(1)
}

func (h *CountingHook) OnCacheHit(_ *http.Request, _ bool) {
	h.CacheHits.Add(1)
}

func (h *CountingHook) OnCacheMiss(_ *http.Request) {
	h.CacheMisses.Add(1)
}

// DefaultLatencyWindow is how many recent latencies a LatencyHook keeps.
const DefaultLatencyWindow = 1024

// LatencyHook records request latencies: running aggregates (count, sum,
// min, max) over every response since creation or Reset, and the most
// recent window of samples. Memory is bounded by the window. All methods
// are safe for concurrent use.
type LatencyHook struct {
	mu       sync.Mutex
	window   []time.Duration // ring buffer, len == cap once full
	next     int             // next write position in window
	count    int64
	sum      time.Duration
	min, max time.Duration
}

// NewLatencyHook creates a LatencyHook keeping the last window samples.
func NewLatencyHook(window int) (*LatencyHook, error) {
	if window < 1 {
		return nil, invalidConfig("latency hook: window must be >= 1, got %d", window)
	}
	return &LatencyHook{window: make([]time.Duration, 0, window)}, nil
}

func (h *LatencyHook) OnRequest(*http.Request) {}

func (h *LatencyHook) OnResponse(_ *http.Request, _ *http.Response, duration time.Duration, _ error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 || duration < h.min {
		h.min = duration
	}
	if duration > h.max {
		h.max = duration
	}
	h.count++
	h.sum += duration
	if len(h.window) < cap(h.window) {
		h.window = append(h.window, duration)
		return
	}
	h.window[h.next] = duration
	h.next = (h.next + 1) % len(h.window)
}

func (h *LatencyHook) OnRetry(*http.Request, int, error) {}
func (h *LatencyHook) OnCacheHit(*http.Request, bool)    {}
func (h *LatencyHook) OnCacheMiss(*http.Request)         {}

// Count returns how many latencies were recorded.
func (h *LatencyHook) Count() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

// Sum returns the total of all recorded latencies.
func (h *LatencyHook) Sum() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sum
}

// Avg returns the mean of all recorded latencies.
func (h *LatencyHook) Avg() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count == 0 {
		return 0
	}
	return h.sum / time.Duration(h.count)
}

// Min returns the smallest recorded latency.
func (h *LatencyHook) Min() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.min
}

// Max returns the largest recorded latency.
func (h *LatencyHook) Max() time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.max
}

// Recent returns the samples in the window, oldest first.
func (h *LatencyHook) Recent() []time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]time.Duration, 0, len(h.window))
	out = append(out, h.window[h.next:]...)
	return append(out, h.window[:h.next]...)
}

// Reset clears the aggregates and the window.
func (h *LatencyHook) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.window = h.window[:0]
	h.next, h.count, h.sum, h.min, h.max = 0, 0, 0, 0, 0
}

// MultiHook fans every event out to several MetricsHooks, including
// OnRetryCause for those that implement RetryCauseHook.
type MultiHook struct {
	hooks []MetricsHook
}

// NewMultiHook combines hooks; nil hooks are skipped.
func NewMultiHook(hooks ...MetricsHook) *MultiHook {
	m := &MultiHook{}
	for _, h := range hooks {
		if h != nil {
			m.hooks = append(m.hooks, h)
		}
	}
	return m
}

func (h *MultiHook) OnRequest(req *http.Request) {
	for _, hook := range h.hooks {
		hook.OnRequest(req)
	}
}

func (h *MultiHook) OnResponse(req *http.Request, resp *http.Response, duration time.Duration, err error) {
	for _, hook := range h.hooks {
		hook.OnResponse(req, resp, duration, err)
	}
}

func (h *MultiHook) OnRetry(req *http.Request, attempt int, err error) {
	for _, hook := range h.hooks {
		hook.OnRetry(req, attempt, err)
	}
}

// OnRetryCause forwards to the hooks that implement RetryCauseHook.
func (h *MultiHook) OnRetryCause(req *http.Request, attempt int, cause RetryCause, err error) {
	for _, hook := range h.hooks {
		if c, ok := hook.(RetryCauseHook); ok {
			c.OnRetryCause(req, attempt, cause, err)
		}
	}
}

func (h *MultiHook) OnCacheHit(req *http.Request, fresh bool) {
	for _, hook := range h.hooks {
		hook.OnCacheHit(req, fresh)
	}
}

func (h *MultiHook) OnCacheMiss(req *http.Request) {
	for _, hook := range h.hooks {
		hook.OnCacheMiss(req)
	}
}

var _ RetryCauseHook = (*MultiHook)(nil)

// CloseIdleConnections forwards to the wrapped transport.
func (t *MetricsTransport) CloseIdleConnections() { closeIdleConnections(t.base) }

// Unwrap returns the wrapped transport.
func (t *MetricsTransport) Unwrap() http.RoundTripper { return t.base }
