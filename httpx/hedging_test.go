//go:build integration

package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHedgingTransport_NoHedge(t *testing.T) {
	requestCount := int32(0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Long delay so hedging won't trigger
	transport := must(NewHedgingTransport(http.DefaultTransport, time.Hour))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("expected 1 request (no hedging), got %d", requestCount)
	}
}

func TestHedgingTransport_HedgeOnSlow(t *testing.T) {
	requestCount := int32(0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count == 1 {
			// First request is slow
			time.Sleep(200 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewHedgingTransport(http.DefaultTransport, 50*time.Millisecond))
	client := &http.Client{Transport: transport}

	start := time.Now()
	resp, err := client.Get(server.URL)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if atomic.LoadInt32(&requestCount) != 2 {
		t.Errorf("expected 2 requests (with hedge), got %d", requestCount)
	}

	// Should complete faster than the slow request
	if elapsed > 150*time.Millisecond {
		t.Errorf("expected fast completion with hedge, got %v", elapsed)
	}
}

func TestHedgingTransport_NonGetNotHedged(t *testing.T) {
	requestCount := int32(0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewHedgingTransport(http.DefaultTransport, 10*time.Millisecond))
	client := &http.Client{Transport: transport}

	resp, err := client.Post(server.URL, "text/plain", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// POST should not be hedged by default
	if atomic.LoadInt32(&requestCount) != 1 {
		t.Errorf("expected 1 request (POST not hedged), got %d", requestCount)
	}
}

func TestHedgingTransport_WithHedgeIf(t *testing.T) {
	requestCount := int32(0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&requestCount, 1)
		if count == 1 {
			time.Sleep(100 * time.Millisecond)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Hedge POST requests
	transport := must(NewHedgingTransport(http.DefaultTransport, 10*time.Millisecond,
		WithHedgeIf(func(req *http.Request) bool {
			return req.Method == http.MethodPost
		}),
	))
	client := &http.Client{Transport: transport}

	resp, err := client.Post(server.URL, "text/plain", nil)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if atomic.LoadInt32(&requestCount) < 2 {
		t.Errorf("expected at least 2 requests (POST hedged), got %d", requestCount)
	}
}

func TestHedgingTransport_MaxHedgedRequests(t *testing.T) {
	requestCount := int32(0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewHedgingTransport(http.DefaultTransport, 10*time.Millisecond,
		WithMaxHedgedRequests(3),
	))
	client := &http.Client{Transport: transport, Timeout: time.Second}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// Should have at most 3 concurrent requests
	count := atomic.LoadInt32(&requestCount)
	if count > 3 {
		t.Errorf("expected at most 3 requests, got %d", count)
	}
}
func TestAdaptiveHedgingTransport(t *testing.T) {
	requestCount := int32(0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewAdaptiveHedgingTransport(http.DefaultTransport, 0.95))
	client := &http.Client{Transport: transport}

	// Make several requests to build up latency data
	for i := 0; i < 20; i++ {
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		resp.Body.Close()
	}

	// The adaptive transport should have adjusted delay based on observed latencies
	// We can't easily test the exact value, but it should not panic
}

func TestHedgingDelayedBodyThroughRealServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // headers first; the body follows later
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("late body"))
	}))
	defer server.Close()

	client := &http.Client{Transport: must(NewHedgingTransport(http.DefaultTransport, time.Hour))}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "late body" {
		t.Fatalf("delayed body = %q, %v", body, err)
	}
}
