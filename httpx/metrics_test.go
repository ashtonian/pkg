//go:build unit

package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMetricsTransport(t *testing.T) {
	hook := &CountingHook{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := must(NewMetricsTransport(http.DefaultTransport, hook))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if hook.Requests.Load() != 1 {
		t.Errorf("expected 1 request, got %d", hook.Requests.Load())
	}
	if hook.Responses.Load() != 1 {
		t.Errorf("expected 1 response, got %d", hook.Responses.Load())
	}
	if hook.Errors.Load() != 0 {
		t.Errorf("expected 0 errors, got %d", hook.Errors.Load())
	}
}

func TestMetricsTransport_Error(t *testing.T) {
	hook := &CountingHook{}

	transport := must(NewMetricsTransport(http.DefaultTransport, hook))
	client := &http.Client{Transport: transport, Timeout: 100 * time.Millisecond}

	_, err := client.Get("http://localhost:1")
	if err == nil {
		t.Error("expected error")
	}

	if hook.Requests.Load() != 1 {
		t.Errorf("expected 1 request, got %d", hook.Requests.Load())
	}
	if hook.Responses.Load() != 1 {
		t.Errorf("expected 1 response (with error), got %d", hook.Responses.Load())
	}
	if hook.Errors.Load() != 1 {
		t.Errorf("expected 1 error, got %d", hook.Errors.Load())
	}
}

func TestMetricsTransport_NilHook(t *testing.T) {
	transport := must(NewMetricsTransport(http.DefaultTransport, nil))
	if transport.hook == nil {
		t.Error("expected default hook")
	}
}
func TestCountingHook(t *testing.T) {
	hook := &CountingHook{}

	req, _ := http.NewRequest("GET", "http://example.com", nil)

	hook.OnRequest(req)
	hook.OnRequest(req)
	if hook.Requests.Load() != 2 {
		t.Errorf("expected 2 requests, got %d", hook.Requests.Load())
	}

	hook.OnResponse(req, &http.Response{StatusCode: 200}, time.Second, nil)
	if hook.Responses.Load() != 1 || hook.Errors.Load() != 0 {
		t.Errorf("expected 1 response, 0 errors")
	}

	hook.OnResponse(req, nil, time.Second, http.ErrHandlerTimeout)
	if hook.Responses.Load() != 2 || hook.Errors.Load() != 1 {
		t.Errorf("expected 2 responses, 1 error")
	}

	hook.OnRetry(req, 1, nil)
	hook.OnRetry(req, 2, nil)
	if hook.Retries.Load() != 2 {
		t.Errorf("expected 2 retries, got %d", hook.Retries.Load())
	}

	hook.OnCacheHit(req, true)
	if hook.CacheHits.Load() != 1 {
		t.Errorf("expected 1 cache hit, got %d", hook.CacheHits.Load())
	}

	hook.OnCacheMiss(req)
	if hook.CacheMisses.Load() != 1 {
		t.Errorf("expected 1 cache miss, got %d", hook.CacheMisses.Load())
	}
}

func TestLatencyHookEmpty(t *testing.T) {
	hook := must(NewLatencyHook(DefaultLatencyWindow))
	if hook.Count() != 0 || hook.Sum() != 0 || hook.Avg() != 0 || hook.Min() != 0 || hook.Max() != 0 || len(hook.Recent()) != 0 {
		t.Error("an empty hook reports zero everywhere")
	}
}

func TestMultiHook(t *testing.T) {
	hook1 := &CountingHook{}
	hook2 := &CountingHook{}

	multi := NewMultiHook(hook1, hook2)

	req, _ := http.NewRequest("GET", "http://example.com", nil)

	multi.OnRequest(req)
	if hook1.Requests.Load() != 1 || hook2.Requests.Load() != 1 {
		t.Error("expected both hooks to receive request")
	}

	multi.OnResponse(req, nil, time.Second, nil)
	if hook1.Responses.Load() != 1 || hook2.Responses.Load() != 1 {
		t.Error("expected both hooks to receive response")
	}

	multi.OnRetry(req, 1, nil)
	if hook1.Retries.Load() != 1 || hook2.Retries.Load() != 1 {
		t.Error("expected both hooks to receive retry")
	}

	multi.OnCacheHit(req, true)
	if hook1.CacheHits.Load() != 1 || hook2.CacheHits.Load() != 1 {
		t.Error("expected both hooks to receive cache hit")
	}

	multi.OnCacheMiss(req)
	if hook1.CacheMisses.Load() != 1 || hook2.CacheMisses.Load() != 1 {
		t.Error("expected both hooks to receive cache miss")
	}
}
