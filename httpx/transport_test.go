//go:build unit

package httpx

import (
	"net/http"
	"testing"
)

func TestMetricsHookOrNoop(t *testing.T) {
	// Passing nil should return a usable noop hook internally
	hook := metricsHookOrNoop(nil)

	// Should not panic
	hook.OnRequest(nil)
	hook.OnResponse(nil, nil, 0, nil)
	hook.OnRetry(nil, 0, nil)
	hook.OnCacheHit(nil, true)
	hook.OnCacheMiss(nil)
}

func TestLoggingHookOrNoop(t *testing.T) {
	// Passing nil should return a usable noop hook internally
	hook := loggingHookOrNoop(nil)

	// Should not panic
	hook.LogRequest(nil)
	hook.LogResponse(nil, nil, 0, nil)
}

func TestCloneRequest(t *testing.T) {
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	req.Header.Set("X-Custom", "value")

	clone := cloneRequest(req)

	if clone.Method != req.Method {
		t.Errorf("expected method %s, got %s", req.Method, clone.Method)
	}
	if clone.URL.String() != req.URL.String() {
		t.Errorf("expected URL %s, got %s", req.URL.String(), clone.URL.String())
	}
	if clone.Header.Get("X-Custom") != "value" {
		t.Errorf("expected header X-Custom=value")
	}

	// Modifying clone should not affect original
	clone.Header.Set("X-Custom", "modified")
	if req.Header.Get("X-Custom") != "value" {
		t.Error("modifying clone affected original request")
	}
}
