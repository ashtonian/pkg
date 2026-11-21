//go:build integration

package httpx

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

// Over a real connection, a retried PUT sends its body again on every
// attempt and returns the eventual success. Timing is the unit tests'.
func TestRetryOverTheNetworkReplaysTheBody(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		n := len(bodies)
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := &http.Client{Transport: must(NewRetryTransport(http.DefaultTransport,
		WithMaxAttempts(5), WithBackoff(time.Millisecond, 10*time.Millisecond, 2)))}
	req, _ := http.NewRequest(http.MethodPut, server.URL, bytes.NewReader([]byte("test body")))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"test body", "test body", "test body"}; !slices.Equal(bodies, want) {
		t.Errorf("server received %q, want %q", bodies, want)
	}
}
