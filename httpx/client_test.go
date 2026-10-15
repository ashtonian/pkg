//go:build integration

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	client := must(NewClient())

	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Transport == nil {
		t.Fatal("expected non-nil transport")
	}
}

func TestNewClient_WithBaseTransport(t *testing.T) {
	client := must(NewClient(
		WithBaseTransport(
			WithMaxIdleConns(50),
			WithDisableHTTP2(),
		),
	))

	// Get base transport from stack
	stack := TransportStack(client)
	if len(stack) == 0 {
		t.Fatal("expected transport stack")
	}

	base, ok := stack[len(stack)-1].(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport at bottom of stack")
	}

	if base.MaxIdleConns != 50 {
		t.Errorf("expected MaxIdleConns 50, got %d", base.MaxIdleConns)
	}
}

func TestNewClient_WithCompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") == "" {
			t.Error("expected Accept-Encoding header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := must(NewClient(WithCompression()))

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
}

func TestNewClient_WithRetry(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := must(NewClient(
		WithRetry(
			WithMaxAttempts(5),
			WithBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0),
		),
	))

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if requestCount != 3 {
		t.Errorf("expected 3 requests, got %d", requestCount)
	}
}

func TestNewClient_WithCache(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Cache-Control", "max-age=3600")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := must(NewClient(WithCache(nil)))

	// First request
	resp1, _ := client.Get(server.URL)
	resp1.Body.Close()

	// Second request - should hit cache
	resp2, _ := client.Get(server.URL)
	resp2.Body.Close()

	if requestCount != 1 {
		t.Errorf("expected 1 request (cache hit), got %d", requestCount)
	}
}

func TestNewClient_WithMetrics(t *testing.T) {
	hook := &CountingHook{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := must(NewClient(WithMetrics(hook)))

	resp, _ := client.Get(server.URL)
	resp.Body.Close()

	if hook.Requests.Load() != 1 {
		t.Errorf("expected 1 request tracked, got %d", hook.Requests.Load())
	}
}

func TestNewClient_WithTimeout(t *testing.T) {
	client := must(NewClient(WithTimeout(5 * time.Second)))

	if client.Timeout != 5*time.Second {
		t.Errorf("expected timeout 5s, got %v", client.Timeout)
	}
}

func TestNewClient_WithAuth(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := must(NewClient(WithAuth(server.URL, NewBearerTokenProvider("token"))))

	resp, _ := client.Get(server.URL)
	resp.Body.Close()

	if receivedHeader != "Bearer token" {
		t.Errorf("expected 'Bearer token', got '%s'", receivedHeader)
	}
}

func TestNewClient_WithLimiter(t *testing.T) {
	client := must(NewClient(WithLimiter(10)))

	stack := TransportStack(client)
	found := false
	for _, t := range stack {
		if _, ok := t.(*LimiterTransport); ok {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected LimiterTransport in stack")
	}
}

func TestNewClient_WithRateLimit(t *testing.T) {
	client := must(NewClient(WithRateLimit(10, 5)))

	stack := TransportStack(client)
	found := false
	for _, t := range stack {
		if _, ok := t.(*RateLimitTransport); ok {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected RateLimitTransport in stack")
	}
}

func TestNewClient_WithCircuitBreaker(t *testing.T) {
	client := must(NewClient(WithCircuitBreaker()))

	stack := TransportStack(client)
	found := false
	for _, t := range stack {
		if _, ok := t.(*CircuitTransport); ok {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected CircuitTransport in stack")
	}
}

func TestNewClient_WithHedging(t *testing.T) {
	client := must(NewClient(WithHedging(100 * time.Millisecond)))

	stack := TransportStack(client)
	found := false
	for _, t := range stack {
		if _, ok := t.(*HedgingTransport); ok {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected HedgingTransport in stack")
	}
}

func TestNewClient_FullStack(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		// Check headers
		if r.Header.Get("Accept-Encoding") == "" {
			t.Error("expected Accept-Encoding header")
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("expected Authorization header")
		}

		w.Header().Set("Cache-Control", "max-age=3600")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	hook := &CountingHook{}

	client := must(NewClient(
		WithBaseTransport(WithMaxIdleConnsPerHost(5)),
		WithCompression(),
		WithCache(nil),
		WithRetry(WithMaxAttempts(3)),
		WithLimiter(10),
		WithCircuitBreaker(),
		WithAuth(server.URL, NewBearerTokenProvider("token")),
		WithMetrics(hook),
		WithTimeout(30*time.Second),
	))

	// First request
	resp1, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp1.Body.Close()

	// Second request - should hit cache
	resp2, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp2.Body.Close()

	if requestCount != 1 {
		t.Errorf("expected 1 request (cache hit), got %d", requestCount)
	}
}

func TestTransportStack(t *testing.T) {
	client := must(NewClient(
		WithCompression(),
		WithRetry(),
		WithMetrics(&CountingHook{}),
	))

	stack := TransportStack(client)

	if len(stack) < 3 {
		t.Errorf("expected at least 3 transports in stack, got %d", len(stack))
	}
}

func TestWithTransport(t *testing.T) {
	custom := &http.Transport{MaxIdleConns: 999}
	client := must(NewClient(WithTransport(custom)))

	if client.Transport != custom {
		t.Error("expected custom transport to be used")
	}
}

func TestWithLogging(t *testing.T) {
	hook := &capturingLogHook{}
	client := must(NewClient(WithLogging(hook)))

	stack := TransportStack(client)
	found := false
	for _, t := range stack {
		if _, ok := t.(*LoggingTransport); ok {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected LoggingTransport in stack")
	}
}
