//go:build integration

package httpx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryTransport_SuccessfulRequest(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if requestCount != 1 {
		t.Errorf("expected 1 request, got %d", requestCount)
	}
}

func TestRetryTransport_RetryOn503(t *testing.T) {
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

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(5),
		WithBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0),
	))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}
	if requestCount != 3 {
		t.Errorf("expected 3 requests, got %d", requestCount)
	}
}

func TestRetryTransport_MaxAttempts(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(3),
		WithBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0),
	))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if requestCount != 3 {
		t.Errorf("expected 3 requests, got %d", requestCount)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", resp.StatusCode)
	}
}

func TestRetryTransport_RetryAfterHeader(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(3),
		WithBackoff(1*time.Millisecond, 2*time.Second, 2.0), // max wait admits Retry-After: 1
	))
	client := &http.Client{Transport: transport}

	start := time.Now()
	resp, err := client.Get(server.URL)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if elapsed < 900*time.Millisecond {
		t.Errorf("expected to wait at least 1 second for Retry-After, elapsed: %v", elapsed)
	}
}

func TestRetryTransport_ContextCancellation(t *testing.T) {
	requestCount := int32(0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(10),
		WithBackoff(100*time.Millisecond, time.Second, 2.0),
	))
	client := &http.Client{Transport: transport}

	// Cancel (no deadline) during the first backoff sleep.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(50*time.Millisecond, cancel)

	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	_, err := client.Do(req)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context canceled error, got %v", err)
	}
}

func TestRetryTransport_RequestBody(t *testing.T) {
	var bodies []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(body))

		if len(bodies) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(5),
		WithBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0),
	))
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest(http.MethodPut, server.URL, bytes.NewReader([]byte("test body")))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if len(bodies) != 3 {
		t.Errorf("expected 3 requests, got %d", len(bodies))
	}

	for i, body := range bodies {
		if body != "test body" {
			t.Errorf("request %d: expected body 'test body', got '%s'", i, body)
		}
	}
}

func TestRetryTransport_NonIdempotentSentOnce(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(3),
		WithBackoff(time.Millisecond, 10*time.Millisecond, 2.0),
	))
	client := &http.Client{Transport: transport}

	// POST should not be retried
	resp, err := client.Post(server.URL, "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if requestCount != 1 {
		t.Errorf("expected 1 request for non-idempotent POST, got %d", requestCount)
	}

	// GET should be retried
	requestCount = 0
	resp, err = client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if requestCount != 3 {
		t.Errorf("expected 3 requests for idempotent GET, got %d", requestCount)
	}
}

func TestRetryTransport_CustomRetryIf(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount < 3 {
			w.WriteHeader(http.StatusConflict) // 409
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(5),
		WithBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0),
		WithRetryIf(func(resp *http.Response, err error) bool {
			if err != nil {
				return true
			}
			return resp.StatusCode == http.StatusConflict
		}),
	))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if requestCount != 3 {
		t.Errorf("expected 3 requests, got %d", requestCount)
	}
}

func TestRetryTransport_WithRetryOn(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if requestCount < 3 {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(5),
		WithBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0),
		WithRetryOn(http.StatusConflict),
	))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if requestCount != 3 {
		t.Errorf("expected 3 requests, got %d", requestCount)
	}
}

func TestRetryTransport_Hook(t *testing.T) {
	hook := &CountingHook{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(3),
		WithBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0),
		WithRetryHook(hook),
	))
	client := &http.Client{Transport: transport}

	resp, _ := client.Get(server.URL)
	if resp != nil {
		resp.Body.Close()
	}

	if hook.Retries.Load() != 2 {
		t.Errorf("expected 2 retries, got %d", hook.Retries.Load())
	}
}

func TestRetryTransport_NoRetryOn400(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	transport := must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(3),
	))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if requestCount != 1 {
		t.Errorf("expected 1 request (no retry on 400), got %d", requestCount)
	}
}

func TestWithJitter(t *testing.T) {
	// Just verify the option doesn't panic - jitter is now private
	transport := must(NewRetryTransport(http.DefaultTransport, WithJitter(0.5)))
	if transport == nil {
		t.Error("expected non-nil transport")
	}
}
