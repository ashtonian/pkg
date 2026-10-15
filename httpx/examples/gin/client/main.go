// Package main demonstrates using httpx to consume a REST API with:
//   - Multiple resource types (User, Device) decoded with Get[T]
//   - Compression (gzip), HTTP caching with conditional revalidation
//   - Auth (bearer token), retry, circuit breaker, rate limiting
//   - Error response handling on 4xx/5xx
//   - 204 No Content from DELETE
//   - Metrics and logging hooks
//
// Usage:
//
//	# Terminal 1: Start the server
//	go run ./server
//
//	# Terminal 2: Run the client
//	go run ./client
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

const demoToken = "local-httpx-demo"

// ---------------------------------------------------------------------------
// Domain types — must match the server
// ---------------------------------------------------------------------------

type User struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Device struct {
	ID       int       `json:"id"`
	Serial   string    `json:"serial"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"last_seen"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// Hooks
// ---------------------------------------------------------------------------

type metricsHook struct {
	requests  atomic.Int64
	cacheHits atomic.Int64
	cacheMiss atomic.Int64
	retries   atomic.Int64
	totalTime atomic.Int64
}

func (h *metricsHook) OnRequest(*http.Request) { h.requests.Add(1) }
func (h *metricsHook) OnResponse(_ *http.Request, _ *http.Response, d time.Duration, _ error) {
	h.totalTime.Add(d.Milliseconds())
}
func (h *metricsHook) OnRetry(req *http.Request, attempt int, err error) {
	h.retries.Add(1)
	slog.InfoContext(req.Context(), "retry", "attempt", attempt, "method", req.Method, "path", req.URL.Path, "error", err)
}
func (h *metricsHook) OnCacheHit(req *http.Request, fresh bool) {
	h.cacheHits.Add(1)
	tag := "fresh"
	if !fresh {
		tag = "stale"
	}
	slog.InfoContext(req.Context(), "cache hit", "freshness", tag, "path", req.URL.Path)
}
func (h *metricsHook) OnCacheMiss(req *http.Request) {
	h.cacheMiss.Add(1)
	slog.InfoContext(req.Context(), "cache miss", "path", req.URL.Path)
}
func (h *metricsHook) Summary() {
	fmt.Println("\n=== Metrics Summary ===")
	fmt.Printf("  Requests:     %d\n", h.requests.Load())
	fmt.Printf("  Cache Hits:   %d\n", h.cacheHits.Load())
	fmt.Printf("  Cache Misses: %d\n", h.cacheMiss.Load())
	fmt.Printf("  Retries:      %d\n", h.retries.Load())
	if n := h.requests.Load(); n > 0 {
		fmt.Printf("  Avg Latency:  %dms\n", h.totalTime.Load()/n)
	}
}

type loggingHook struct{}

func (*loggingHook) LogRequest(req *http.Request) {
	slog.InfoContext(req.Context(), "request", "method", req.Method, "path", req.URL.Path)
}
func (*loggingHook) LogResponse(req *http.Request, resp *http.Response, d time.Duration, err error) {
	if err != nil {
		slog.WarnContext(req.Context(), "request error", "path", req.URL.Path, "duration", d, "error", err)
		return
	}
	slog.InfoContext(req.Context(), "response", "status", resp.StatusCode, "path", req.URL.Path, "duration", d)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8080", "example server URL")
	deadline := flag.Duration("deadline", 2*time.Minute, "deadline for the entire demonstration")
	flag.Parse()
	if *deadline <= 0 {
		slog.Error("deadline must be positive")
		os.Exit(1)
	}
	token := os.Getenv("HTTPX_EXAMPLE_TOKEN")
	if token == "" {
		token = demoToken
	}
	ctx, cancel := context.WithTimeout(context.Background(), *deadline)
	defer cancel()
	if err := run(ctx, strings.TrimRight(*baseURL, "/"), token); err != nil {
		slog.ErrorContext(ctx, "example failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, baseURL, token string) error {
	metrics := &metricsHook{}

	// Compose the features exercised by this example. Choose limits for your API.
	// All transports compose at the HTTP level; the typed helpers (Get[T],
	// Post[T], etc.) handle encoding/decoding on top.
	client, err := httpx.NewClient(
		httpx.WithCompression(),
		httpx.WithCache(nil,
			httpx.WithStaleIfError(5*time.Minute),
			httpx.WithStaleWhileRevalidate(30*time.Second),
			httpx.WithCacheHook(metrics),
		),
		httpx.WithRetry(
			httpx.WithMaxAttempts(3),
			httpx.WithBackoff(50*time.Millisecond, 2*time.Second, 2.0),
			httpx.WithJitter(0.1),
			httpx.WithRetryOn(429, 502, 503, 504),
		),
		httpx.WithLimiter(10),
		httpx.WithRateLimit(100, 200),
		httpx.WithCircuitBreaker(
			httpx.WithFailureThreshold(5),
			httpx.WithSuccessThreshold(2),
			httpx.WithOpenTimeout(10*time.Second),
		),
		httpx.WithAuth(baseURL, httpx.NewBearerTokenProvider(token)),
		httpx.WithMetrics(metrics),
		httpx.WithLogging(&loggingHook{}),
		httpx.WithTimeout(30*time.Second),
	)
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}

	defer client.CloseIdleConnections()

	fmt.Println("=== httpx Example Client ===")
	fmt.Println("Make sure the server is running: go run ./server")
	fmt.Println()

	// 1. Multiple types + caching + compression
	fmt.Println("--- 1. Multiple Resource Types (User & Device) ---")
	if err := testMultipleTypes(ctx, client, baseURL); err != nil {
		return err
	}

	// 2. HTTP cache: fresh hits and conditional revalidation
	fmt.Println("\n--- 2. HTTP Cache ---")
	if err := testCache(ctx, client, baseURL); err != nil {
		return err
	}

	// 3. CRUD with typed helpers + 204 No Content
	fmt.Println("\n--- 3. CRUD Operations ---")
	if err := testCRUD(ctx, client, baseURL); err != nil {
		return err
	}

	// 4. Error response handling (4xx)
	fmt.Println("\n--- 4. Error Response Handling ---")
	if err := testErrorHandling(ctx, client, baseURL); err != nil {
		return err
	}

	// 5. Retry with typed helpers
	fmt.Println("\n--- 5. Retry (Flaky Endpoint) ---")
	if err := testRetry(ctx, client, baseURL); err != nil {
		return err
	}

	// 6. Concurrent requests through the limiter
	fmt.Println("\n--- 6. Concurrent Requests ---")
	if err := testConcurrency(ctx, client, baseURL); err != nil {
		return err
	}

	metrics.Summary()
	return nil
}

// checkResponse closes every returned body, including responses returned with a
// decoding error. HTTP status is separate from err: a non-2xx response has nil
// err in the typed helpers and must be checked before using the decoded value.
func checkResponse(resp *http.Response, requestErr error, wantStatus int) (err error) {
	if resp == nil {
		if requestErr != nil {
			return requestErr
		}
		return errors.New("missing HTTP response")
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if requestErr != nil {
		return requestErr
	}
	if resp.StatusCode != wantStatus {
		// Bound an unexpected error body as well as successful typed responses.
		body, readErr := httpx.ReadAtMost(resp.Body, 16<<10)
		return errors.Join(fmt.Errorf("unexpected HTTP %d (want %d): %s", resp.StatusCode, wantStatus, body), readErr)
	}
	return nil
}

// testMultipleTypes shows two representations through one client. The server
// sends gzip and the compression transport decodes it before the typed helper.
func testMultipleTypes(ctx context.Context, client *http.Client, baseURL string) error {
	users, resp, err := httpx.Get[[]User](ctx, client, baseURL+"/api/users")
	if err := checkResponse(resp, err, http.StatusOK); err != nil {
		return fmt.Errorf("list users: %w", err)
	}
	fmt.Printf("  Users:   %d users (status %d)\n", len(users), resp.StatusCode)

	devices, resp, err := httpx.Get[[]Device](ctx, client, baseURL+"/api/devices")
	if err := checkResponse(resp, err, http.StatusOK); err != nil {
		return fmt.Errorf("list devices: %w", err)
	}
	fmt.Printf("  Devices: %d devices (status %d)\n", len(devices), resp.StatusCode)

	device, resp, err := httpx.Get[Device](ctx, client, baseURL+"/api/devices/1")
	if err := checkResponse(resp, err, http.StatusOK); err != nil {
		return fmt.Errorf("get device: %w", err)
	}
	fmt.Printf("  Device 1: serial=%s name=%q status=%s\n", device.Serial, device.Name, device.Status)
	return nil
}

// testCache exercises a fresh cache hit and forces conditional revalidation.
// no-cache means revalidate before using a stored response, not disable storage.
func testCache(ctx context.Context, client *http.Client, baseURL string) error {
	user, resp, err := httpx.Get[User](ctx, client, baseURL+"/api/users/1")
	if err := checkResponse(resp, err, http.StatusOK); err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	fmt.Printf("  First:  %d, Name=%s\n", resp.StatusCode, user.Name)

	user2, resp, err := httpx.Get[User](ctx, client, baseURL+"/api/users/1")
	if err := checkResponse(resp, err, http.StatusOK); err != nil {
		return fmt.Errorf("get cached user: %w", err)
	}
	if !strings.Contains(resp.Header.Get("Cache-Status"), "; hit") {
		return errors.New("repeat request did not hit the cache")
	}
	fmt.Printf("  Second: %d, Name=%s, Cache-Status=%s\n", resp.StatusCode, user2.Name, resp.Header.Get("Cache-Status"))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/users/1", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cache-Control", "no-cache")
	user3, resp, err := httpx.Do[User](client, req)
	if err := checkResponse(resp, err, http.StatusOK); err != nil {
		return fmt.Errorf("revalidate user: %w", err)
	}
	if !strings.Contains(resp.Header.Get("Cache-Status"), "fwd-status=304") {
		return errors.New("conditional request did not revalidate")
	}
	fmt.Printf("  Revalidated: %d, Name=%s, Cache-Status=%s\n", resp.StatusCode, user3.Name, resp.Header.Get("Cache-Status"))
	return nil
}

// testCRUD checks each status before using the decoded value. A failed POST is
// not replayed automatically: retries of writes need server-side deduplication.
func testCRUD(ctx context.Context, client *http.Client, baseURL string) error {
	type Input struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	created, resp, err := httpx.Post[User](ctx, client, baseURL+"/api/users", Input{Name: "Dave", Email: "dave@example.com"})
	if err := checkResponse(resp, err, http.StatusCreated); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	fmt.Printf("  Created: id=%d name=%s (status %d)\n", created.ID, created.Name, resp.StatusCode)

	updated, resp, err := httpx.Put[User](ctx, client, fmt.Sprintf("%s/api/users/%d", baseURL, created.ID), Input{Name: "Dave Updated", Email: "dave.updated@example.com"})
	if err := checkResponse(resp, err, http.StatusOK); err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	fmt.Printf("  Updated: id=%d name=%s (status %d)\n", updated.ID, updated.Name, resp.StatusCode)

	// Delete leaves the response raw; 204 has no body to decode.
	resp, err = httpx.Delete(ctx, client, fmt.Sprintf("%s/api/users/%d", baseURL, created.ID))
	if err := checkResponse(resp, err, http.StatusNoContent); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	fmt.Printf("  Deleted: status %d (204 = success, no body)\n", resp.StatusCode)
	return nil
}

func readAPIError(resp *http.Response, requestErr error, wantStatus int) (apiErr APIError, err error) {
	if resp == nil || requestErr != nil || resp.StatusCode != wantStatus {
		return apiErr, checkResponse(resp, requestErr, wantStatus)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	body, err := httpx.ReadAtMost(resp.Body, 16<<10)
	if err != nil {
		return apiErr, err
	}
	if err := json.Unmarshal(body, &apiErr); err != nil {
		return apiErr, err
	}
	return apiErr, nil
}

// Non-2xx returns zero T, the response with its body intact, and nil error.
func testErrorHandling(ctx context.Context, client *http.Client, baseURL string) error {
	user, resp, err := httpx.Get[User](ctx, client, baseURL+"/api/users/9999")
	apiErr, err := readAPIError(resp, err, http.StatusNotFound)
	if err != nil {
		return fmt.Errorf("read missing-user response: %w", err)
	}
	if apiErr.Code != "NOT_FOUND" || user.ID != 0 {
		return fmt.Errorf("unexpected missing-user response: %+v", apiErr)
	}
	fmt.Printf("  Status: %d, User zero? %v\n", resp.StatusCode, user.ID == 0)
	fmt.Printf("  Error: code=%s message=%q\n", apiErr.Code, apiErr.Message)

	noAuthClient, err := httpx.NewClient(httpx.WithTimeout(5 * time.Second))
	if err != nil {
		return err
	}
	defer noAuthClient.CloseIdleConnections()
	_, resp, err = httpx.Get[User](ctx, noAuthClient, baseURL+"/api/users/1")
	apiErr, err = readAPIError(resp, err, http.StatusUnauthorized)
	if err != nil {
		return fmt.Errorf("read no-auth response: %w", err)
	}
	if apiErr.Code != "UNAUTHORIZED" {
		return fmt.Errorf("unexpected no-auth response: %+v", apiErr)
	}
	fmt.Printf("  No auth: status %d\n", resp.StatusCode)
	return nil
}

// The server fails every third GET; retries recover without replaying a write.
func testRetry(ctx context.Context, client *http.Client, baseURL string) error {
	type FlakyResp struct {
		Attempt int `json:"attempt"`
	}
	for i := 1; i <= 4; i++ {
		result, resp, err := httpx.Get[FlakyResp](ctx, client, baseURL+"/api/flaky")
		if err := checkResponse(resp, err, http.StatusOK); err != nil {
			return fmt.Errorf("flaky request %d: %w", i, err)
		}
		fmt.Printf("  Request %d: status=%d attempt=%d\n", i, resp.StatusCode, result.Attempt)
	}
	return nil
}

func testConcurrency(ctx context.Context, client *http.Client, baseURL string) error {
	var wg sync.WaitGroup
	start := time.Now()
	results := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			var resp *http.Response
			var err error
			if i%2 == 0 {
				_, resp, err = httpx.Get[User](ctx, client, fmt.Sprintf("%s/api/users/%d", baseURL, (i%3)+1))
			} else {
				_, resp, err = httpx.Get[Device](ctx, client, fmt.Sprintf("%s/api/devices/%d", baseURL, (i%3)+1))
			}
			results <- checkResponse(resp, err, http.StatusOK)
		})
	}
	wg.Wait()
	close(results)
	var requestErrors []error
	for err := range results {
		if err != nil {
			requestErrors = append(requestErrors, err)
		}
	}
	if err := errors.Join(requestErrors...); err != nil {
		return fmt.Errorf("concurrent requests: %w", err)
	}
	fmt.Printf("  20 concurrent requests completed in %s\n", time.Since(start))
	return nil
}
