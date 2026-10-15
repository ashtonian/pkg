//go:build integration

package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLimiterTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewLimiterTransport(http.DefaultTransport, 2))
	client := &http.Client{Transport: transport}

	if transport.Capacity() != 2 {
		t.Errorf("expected capacity 2, got %d", transport.Capacity())
	}

	// Should complete successfully
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
}

func TestLimiterTransport_Concurrency(t *testing.T) {
	var maxConcurrent int32
	var currentConcurrent int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt32(&currentConcurrent, 1)
		for {
			max := atomic.LoadInt32(&maxConcurrent)
			if current > max {
				if atomic.CompareAndSwapInt32(&maxConcurrent, max, current) {
					break
				}
			} else {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&currentConcurrent, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewLimiterTransport(http.DefaultTransport, 3))
	client := &http.Client{Transport: transport}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := client.Get(server.URL)
			if resp != nil {
				resp.Body.Close()
			}
		}()
	}

	wg.Wait()

	if maxConcurrent > 3 {
		t.Errorf("expected max concurrent <= 3, got %d", maxConcurrent)
	}
}

func TestLimiterTransport_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewLimiterTransport(http.DefaultTransport, 1,
		WithAcquireTimeout(50*time.Millisecond),
	))
	client := &http.Client{Transport: transport}

	// Start a slow request
	go func() {
		resp, _ := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	time.Sleep(10 * time.Millisecond)

	// This should timeout waiting for a slot
	_, err := client.Get(server.URL)
	if !errors.Is(err, ErrLimiterTimeout) {
		t.Errorf("expected ErrLimiterTimeout, got %v", err)
	}
}

func TestLimiterTransport_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewLimiterTransport(http.DefaultTransport, 1))
	client := &http.Client{Transport: transport}

	// Start a slow request
	go func() {
		resp, _ := client.Get(server.URL)
		if resp != nil {
			resp.Body.Close()
		}
	}()

	time.Sleep(10 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	_, err := client.Do(req)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}

func TestLimiterTransport_OnBlocked(t *testing.T) {
	var blockedCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewLimiterTransport(http.DefaultTransport, 1,
		WithOnBlocked(func() {
			atomic.AddInt32(&blockedCount, 1)
		}),
	))
	client := &http.Client{Transport: transport}

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, _ := client.Get(server.URL)
			if resp != nil {
				resp.Body.Close()
			}
		}()
	}

	wg.Wait()

	// At least 2 requests should have been blocked (first one gets slot, others wait)
	if atomic.LoadInt32(&blockedCount) < 2 {
		t.Errorf("expected at least 2 blocked callbacks, got %d", blockedCount)
	}
}

func TestLimiterTransport_Available(t *testing.T) {
	transport := must(NewLimiterTransport(http.DefaultTransport, 5))

	if transport.Available() != 5 {
		t.Errorf("expected 5 available, got %d", transport.Available())
	}
	if transport.InFlight() != 0 {
		t.Errorf("expected 0 in-flight, got %d", transport.InFlight())
	}
}
