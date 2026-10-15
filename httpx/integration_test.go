//go:build integration

package httpx_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

type testResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// testLoggingHook implements LoggingHook for integration testing.
type testLoggingHook struct {
	mu        sync.Mutex
	requests  int
	responses int
}

func (h *testLoggingHook) LogRequest(_ *http.Request) {
	h.mu.Lock()
	h.requests++
	h.mu.Unlock()
}

func (h *testLoggingHook) LogResponse(_ *http.Request, _ *http.Response, _ time.Duration, _ error) {
	h.mu.Lock()
	h.responses++
	h.mu.Unlock()
}

func (h *testLoggingHook) count() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests, h.responses
}

// TestProductionStack exercises the full transport chain against a realistic server.
// All transports are configured using the With* options pattern.
func TestProductionStack(t *testing.T) {
	var (
		requestCount   atomic.Int32
		maxConcurrent  atomic.Int32
		curConcurrent  atomic.Int32
		mu             sync.Mutex
		requestHeaders []http.Header
		fail503        atomic.Bool
		failAll500     atomic.Bool
	)

	fail503.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Track concurrency
		cur := curConcurrent.Add(1)
		defer curConcurrent.Add(-1)
		for {
			mx := maxConcurrent.Load()
			if cur > mx {
				if maxConcurrent.CompareAndSwap(mx, cur) {
					break
				}
			} else {
				break
			}
		}

		count := requestCount.Add(1)

		mu.Lock()
		requestHeaders = append(requestHeaders, r.Header.Clone())
		mu.Unlock()

		if failAll500.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if fail503.Load() && count == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		if r.Header.Get("If-None-Match") == `"resp-etag-1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=1")
		w.Header().Set("ETag", `"resp-etag-1"`)
		w.Header().Set("Last-Modified", time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
		json.NewEncoder(w).Encode(testResponse{
			Status:  "ok",
			Message: fmt.Sprintf("response-%d", count),
		})
	}))
	defer server.Close()

	metricsHook := &httpx.CountingHook{}
	loggingHook := &testLoggingHook{}

	// Build client using ALL transports with their With* option variants.
	client := must(httpx.NewClient(
		// Base transport with connection tuning
		httpx.WithBaseTransport(
			httpx.WithMaxIdleConns(100),
			httpx.WithMaxIdleConnsPerHost(10),
			httpx.WithIdleConnTimeout(90*time.Second),
		),
		// Compression
		httpx.WithCompression(),
		// Cache with stale extensions
		httpx.WithCache(nil,
			httpx.WithStaleIfError(time.Minute),
			httpx.WithStaleWhileRevalidate(10*time.Second),
			httpx.WithCacheHook(metricsHook),
		),
		// Retry with backoff, jitter, and specific status codes
		httpx.WithRetry(
			httpx.WithMaxAttempts(3),
			httpx.WithBackoff(1*time.Millisecond, 100*time.Millisecond, 2.0),
			httpx.WithJitter(0.1),
			httpx.WithRetryOn(502, 503, 504, 429),
			httpx.WithRetryHook(metricsHook),
		),
		// Concurrency limiter with timeout
		httpx.WithLimiter(3,
			httpx.WithAcquireTimeout(5*time.Second),
		),
		// Rate limiter (high limit so test isn't throttled, but exercises the transport)
		httpx.WithRateLimit(1000, 100, httpx.WithWait()),
		// Circuit breaker with full options
		httpx.WithCircuitBreaker(
			httpx.WithFailureThreshold(5),
			httpx.WithSuccessThreshold(2),
			httpx.WithOpenTimeout(time.Hour),
		),
		// Authentication
		httpx.WithAuth(server.URL, httpx.NewBearerTokenProvider("test-token")),
		// Metrics
		httpx.WithMetrics(metricsHook),
		// Logging with an extra redacted header
		httpx.WithLogging(loggingHook,
			httpx.WithSensitiveHeaders("X-Tenant-Secret"),
		),
		// Timeout
		httpx.WithTimeout(30*time.Second),
	))

	// --- Phase 1: Auth + Compression + Codec ---
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("phase 1: request failed: %v", err)
	}

	var result testResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("phase 1: decode failed: %v", err)
	}
	resp.Body.Close()

	if result.Status != "ok" {
		t.Errorf("phase 1: expected status 'ok', got '%s'", result.Status)
	}

	mu.Lock()
	if len(requestHeaders) == 0 {
		t.Fatal("phase 1: no requests recorded")
	}
	firstAuth := requestHeaders[0].Get("Authorization")
	firstAcceptEncoding := requestHeaders[0].Get("Accept-Encoding")
	mu.Unlock()

	if firstAuth != "Bearer test-token" {
		t.Errorf("phase 1: expected 'Bearer test-token', got '%s'", firstAuth)
	}
	if firstAcceptEncoding == "" {
		t.Error("phase 1: expected Accept-Encoding header")
	}

	// --- Phase 2: Retry verification ---
	if requestCount.Load() != 2 {
		t.Errorf("phase 2: expected 2 backend requests (503 + retry), got %d", requestCount.Load())
	}
	if metricsHook.Retries.Load() != 1 {
		t.Errorf("phase 2: expected 1 retry recorded, got %d", metricsHook.Retries.Load())
	}

	// --- Phase 3: Cache hit ---
	fail503.Store(false)
	resp2, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("phase 3: request failed: %v", err)
	}
	resp2.Body.Close()

	if requestCount.Load() != 2 {
		t.Errorf("phase 3: expected 2 total backend requests (cache hit), got %d", requestCount.Load())
	}
	if !strings.HasPrefix(resp2.Header.Get("Cache-Status"), "httpx; hit") {
		t.Errorf("phase 3: expected a cache hit, Cache-Status=%q", resp2.Header.Get("Cache-Status"))
	}

	// --- Phase 4: revalidation after expiry (ETag + max-age=1) ---
	time.Sleep(1100 * time.Millisecond)

	resp3, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("phase 4: request failed: %v", err)
	}
	body3, _ := io.ReadAll(resp3.Body)
	resp3.Body.Close()

	if len(body3) == 0 {
		t.Error("phase 4: expected non-empty body after revalidation")
	}

	// --- Phase 5: Circuit breaker ---
	failAll500.Store(true)
	prevCount := requestCount.Load()

	var circuitOpened bool
	for range 20 {
		resp, err := client.Get(server.URL + "/circuit-test")
		if errors.Is(err, httpx.ErrCircuitOpen) {
			circuitOpened = true
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
	}

	if !circuitOpened {
		t.Error("phase 5: expected circuit breaker to open after failures")
	}

	circuitHits := requestCount.Load() - prevCount
	if circuitHits < 5 {
		t.Errorf("phase 5: expected at least 5 backend hits before circuit open, got %d", circuitHits)
	}

	failAll500.Store(false)

	// --- Phase 6: Concurrency limiter ---
	maxConcurrent.Store(0)
	curConcurrent.Store(0)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			resp, err := client.Get(fmt.Sprintf("%s/concurrent/%d", server.URL, n))
			if err != nil {
				return
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	if maxConcurrent.Load() > 3 {
		t.Errorf("phase 6: expected max concurrent <= 3, got %d", maxConcurrent.Load())
	}

	// --- Phase 7: Metrics + Logging ---
	if metricsHook.Requests.Load() == 0 {
		t.Error("phase 7: expected metrics hook to record requests")
	}
	if metricsHook.Responses.Load() == 0 {
		t.Error("phase 7: expected metrics hook to record responses")
	}
	if metricsHook.CacheHits.Load() == 0 {
		t.Error("phase 7: expected at least one cache hit recorded")
	}

	logReqs, logResps := loggingHook.count()
	if logReqs == 0 {
		t.Error("phase 7: expected logging hook to record requests")
	}
	if logResps == 0 {
		t.Error("phase 7: expected logging hook to record responses")
	}
}

// TestCircuitBreakerIgnoresLocalThrottling verifies that rate limiting and
// concurrency limiting errors do NOT trip the circuit breaker. This was a
// bug where ErrRateLimited/ErrLimiterTimeout counted as circuit failures.
func TestCircuitBreakerIgnoresLocalThrottling(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	// Rate limit: 1 request per second, burst of 1.
	// Circuit breaker: opens after 3 failures.
	client := must(httpx.NewClient(
		httpx.WithRateLimit(1, 1),
		httpx.WithCircuitBreaker(
			httpx.WithFailureThreshold(3),
			httpx.WithOpenTimeout(time.Hour),
		),
	))

	// First request succeeds (within burst).
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	resp.Body.Close()

	// Rapid-fire requests should get ErrRateLimited, NOT ErrCircuitOpen.
	rateLimited := 0
	circuitOpened := false
	for range 10 {
		resp, err := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
		if errors.Is(err, httpx.ErrRateLimited) {
			rateLimited++
		}
		if errors.Is(err, httpx.ErrCircuitOpen) {
			circuitOpened = true
		}
	}

	if rateLimited == 0 {
		t.Error("expected at least one ErrRateLimited")
	}
	if circuitOpened {
		t.Error("circuit breaker should NOT have opened from rate limiting")
	}
}

// TestCircuitBreakerRecovery verifies the full lifecycle:
// closed → open (on failures) → half-open (after timeout) → closed (on successes).
func TestCircuitBreakerRecovery(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := must(httpx.NewClient(
		httpx.WithCircuitBreaker(
			httpx.WithFailureThreshold(2),
			httpx.WithSuccessThreshold(2),
			httpx.WithOpenTimeout(50*time.Millisecond),
		),
	))

	// Trigger 2 failures to open the circuit.
	for range 2 {
		resp, _ := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}

	// Circuit should be open now.
	_, err := client.Get(server.URL)
	if !errors.Is(err, httpx.ErrCircuitOpen) {
		t.Fatalf("expected ErrCircuitOpen, got %v", err)
	}

	// Wait for half-open timeout.
	time.Sleep(60 * time.Millisecond)

	// Fix the server.
	fail.Store(false)

	// Half-open: 2 successes should close the circuit.
	for range 2 {
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("recovery request failed: %v", err)
		}
		resp.Body.Close()
	}

	// Circuit should be closed — normal requests work.
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("post-recovery request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after recovery, got %d", resp.StatusCode)
	}
}

// TestRetryWithCircuitBreaker verifies that retry stops immediately when
// the circuit breaker opens mid-retry, and does not keep retrying.
func TestRetryWithCircuitBreaker(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	// Circuit opens after 2 failures. Retry allows up to 5 attempts.
	// With circuit inside retry, each attempt hits the circuit.
	// After 2 attempts fail, circuit opens, and remaining retries get
	// ErrCircuitOpen which is not retryable → stops.
	client := must(httpx.NewClient(
		httpx.WithRetry(
			httpx.WithMaxAttempts(5),
			httpx.WithBackoff(1*time.Millisecond, 10*time.Millisecond, 1.0),
		),
		httpx.WithCircuitBreaker(
			httpx.WithFailureThreshold(2),
			httpx.WithOpenTimeout(time.Hour),
		),
	))

	resp, err := client.Get(server.URL)
	if resp != nil {
		resp.Body.Close()
	}

	// Should have stopped after circuit opened (2 real requests + remaining
	// retries short-circuited). The final error should be ErrCircuitOpen.
	if !errors.Is(err, httpx.ErrCircuitOpen) {
		t.Errorf("expected ErrCircuitOpen, got %v", err)
	}

	// Only 2 real requests should have reached the server.
	if requestCount.Load() != 2 {
		t.Errorf("expected 2 backend requests (circuit opened after 2), got %d", requestCount.Load())
	}
}

// TestStaleWhileRevalidate verifies that stale responses are served immediately
// while background revalidation updates the cache.
func TestStaleWhileRevalidate(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=1") // 1 second freshness
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"count":%d}`, n)
	}))
	defer server.Close()

	client := must(httpx.NewClient(
		httpx.WithCache(nil,
			httpx.WithStaleWhileRevalidate(10*time.Second),
		),
	))

	// First request populates cache.
	resp1, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	body1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()

	if requestCount.Load() != 1 {
		t.Fatalf("expected 1 backend request, got %d", requestCount.Load())
	}

	// Wait for cache to become stale.
	time.Sleep(1100 * time.Millisecond)

	// Second request: cache is stale but within stale-while-revalidate window.
	// Should serve stale immediately and trigger background revalidation.
	resp2, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("second request failed: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	// Should return the stale cached body (same as first).
	if string(body1) != string(body2) {
		t.Errorf("expected stale response %s, got %s", string(body1), string(body2))
	}

	// Wait for background revalidation to complete.
	time.Sleep(100 * time.Millisecond)

	if requestCount.Load() < 2 {
		t.Error("expected background revalidation to fire")
	}
}

// TestStaleIfError verifies that stale cached responses are served when the
// upstream returns an error.
func TestStaleIfError(t *testing.T) {
	var fail atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=1") // 1 second freshness
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := must(httpx.NewClient(
		httpx.WithCache(nil,
			httpx.WithStaleIfError(time.Minute),
		),
	))

	// Populate cache.
	resp1, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	resp1.Body.Close()

	// Wait for cache to become stale.
	time.Sleep(1100 * time.Millisecond)

	// Break the server.
	fail.Store(true)

	// Should serve stale because the upstream returns 502 and we're within
	// the stale-if-error window (cache expired ~0.1s ago, stale-if-error = 1min).
	resp2, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("stale-if-error request failed: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()

	if !strings.HasPrefix(resp2.Header.Get("Cache-Status"), "httpx; hit") {
		t.Error("expected a stale cache hit for the stale-if-error response")
	}

	var result testResponse
	if err := json.Unmarshal(body2, &result); err != nil {
		t.Fatalf("decode stale body: %v", err)
	}
	if result.Status != "ok" {
		t.Errorf("expected stale body with status 'ok', got '%s'", result.Status)
	}
}

// TestChainAPIIntegration verifies that the Chain API produces a fully functional
// client with the same behavior as NewClient.
func TestChainAPIIntegration(t *testing.T) {
	var requestCount atomic.Int32
	var fail503 atomic.Bool
	fail503.Store(true)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requestCount.Add(1)
		if fail503.Load() && n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	chain := httpx.NewChain(
		httpx.WithCompressionT(),
		httpx.WithCacheT(nil),
		httpx.WithCircuitBreakerT(httpx.WithFailureThreshold(5)),
		httpx.WithRetryT(
			httpx.WithMaxAttempts(3),
			httpx.WithBackoff(1*time.Millisecond, 10*time.Millisecond, 1.0),
		),
		httpx.WithAuthT(server.URL, httpx.NewBearerTokenProvider("chain-token")),
	)
	client := must(chain.Client(nil, 30*time.Second))

	// First request: 503 → retry → 200.
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("chain request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if requestCount.Load() != 2 {
		t.Errorf("expected 2 backend requests (503 + retry), got %d", requestCount.Load())
	}

	// Second request: cache hit.
	resp2, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("cached request failed: %v", err)
	}
	resp2.Body.Close()
	if !strings.HasPrefix(resp2.Header.Get("Cache-Status"), "httpx; hit") {
		t.Error("expected a cache hit from the chain client")
	}
	if requestCount.Load() != 2 {
		t.Errorf("expected 2 total requests (cache hit), got %d", requestCount.Load())
	}
}

// TestHedgingIntegration verifies that hedging fires a speculative request when
// the primary is slow, and returns whichever response arrives first.
func TestHedgingIntegration(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requestCount.Add(1)
		// First request is slow; second (hedge) is fast.
		if n == 1 {
			time.Sleep(200 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "response-%d", n)
	}))
	defer server.Close()

	client := must(httpx.NewClient(
		httpx.WithHedging(20 * time.Millisecond), // hedge after 20ms
	))

	start := time.Now()
	resp, err := client.Get(server.URL)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("hedged request failed: %v", err)
	}
	resp.Body.Close()

	// Both requests should have been sent.
	if requestCount.Load() < 2 {
		t.Errorf("expected at least 2 requests (primary + hedge), got %d", requestCount.Load())
	}

	// Response should have arrived much faster than 200ms (the slow primary).
	if elapsed > 150*time.Millisecond {
		t.Errorf("hedging should have returned faster than slow primary, took %v", elapsed)
	}
}

// TestRetryWithRetryAfter verifies that the retry transport honors the
// Retry-After header from the server.
func TestRetryWithRetryAfter(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1") // 1 second
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	client := must(httpx.NewClient(
		httpx.WithRetry(
			httpx.WithMaxAttempts(3),
			httpx.WithBackoff(1*time.Millisecond, 2*time.Second, 1.0), // max wait admits Retry-After: 1
		),
	))

	start := time.Now()
	resp, err := client.Get(server.URL)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after retry, got %d", resp.StatusCode)
	}
	if requestCount.Load() != 2 {
		t.Errorf("expected 2 requests (429 + retry), got %d", requestCount.Load())
	}
	// Should have waited at least ~1 second for Retry-After.
	if elapsed < 900*time.Millisecond {
		t.Errorf("expected retry to honor Retry-After: 1, but elapsed only %v", elapsed)
	}
}

// TestConcurrentCacheReads verifies thread-safety of cache under concurrent access.
func TestConcurrentCacheReads(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := must(httpx.NewClient(
		httpx.WithCache(nil),
	))

	// Warm cache.
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("warmup failed: %v", err)
	}
	resp.Body.Close()

	initial := requestCount.Load()

	// Fire concurrent requests — all should hit cache.
	var wg sync.WaitGroup
	errCh := make(chan error, 50)
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := client.Get(server.URL)
			if err != nil {
				errCh <- err
				return
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if len(body) == 0 {
				errCh <- fmt.Errorf("empty body from cache")
			}
		}()
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent cache read error: %v", err)
	}

	// No additional backend requests should have been made.
	if requestCount.Load() != initial {
		t.Errorf("expected %d backend requests (all cache hits), got %d", initial, requestCount.Load())
	}
}

// TestCacheWithRetry verifies that failed responses (which trigger retries)
// are not cached, and the successful response after retry IS cached.
func TestCacheWithRetry(t *testing.T) {
	var requestCount atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	client := must(httpx.NewClient(
		httpx.WithCache(nil),
		httpx.WithRetry(
			httpx.WithMaxAttempts(3),
			httpx.WithBackoff(1*time.Millisecond, 10*time.Millisecond, 1.0),
		),
	))

	// First request: 503 → retry → 200 (cached).
	resp1, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("first request failed: %v", err)
	}
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("expected 200 after retry, got %d", resp1.StatusCode)
	}

	// Second request: should hit cache (no additional backend request).
	resp2, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("cached request failed: %v", err)
	}
	resp2.Body.Close()

	if !strings.HasPrefix(resp2.Header.Get("Cache-Status"), "httpx; hit") {
		t.Error("expected cache hit after retry success")
	}
	if requestCount.Load() != 2 {
		t.Errorf("expected 2 backend requests total (503 + success + cache hit), got %d", requestCount.Load())
	}
}

// TestLimiterWithTimeout verifies that the concurrency limiter returns
// ErrLimiterTimeout when all slots are occupied and the timeout expires.
func TestLimiterWithTimeout(t *testing.T) {
	started := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		time.Sleep(200 * time.Millisecond) // hold the slot
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := must(httpx.NewClient(
		httpx.WithLimiter(1,
			httpx.WithAcquireTimeout(20*time.Millisecond),
		),
	))

	// First request: occupies the only slot.
	go func() {
		resp, _ := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	// Wait for first request to start.
	<-started

	// Second request: should timeout waiting for a slot.
	_, err := client.Get(server.URL)
	if !errors.Is(err, httpx.ErrLimiterTimeout) {
		t.Errorf("expected ErrLimiterTimeout, got %v", err)
	}
}
