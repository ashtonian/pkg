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

// Over a real connection, a primary that does not answer is hedged, the
// hedge's response wins, and the primary is cancelled. Timing is the unit
// tests'.
func TestHedgingOverTheNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			<-r.Context().Done() // the primary answers only by being cancelled
			return
		}
		_, _ = w.Write([]byte("hedge"))
	}))
	defer server.Close()

	client := &http.Client{Transport: must(NewHedgingTransport(http.DefaultTransport, time.Millisecond))}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "hedge" {
		t.Fatalf("body = %q, %v; want the hedge's", body, err)
	}
	if n := requests.Load(); n != 2 {
		t.Errorf("server saw %d requests, want the primary and one hedge", n)
	}
}

// A winner whose body arrives after its headers stays readable once
// RoundTrip has returned.
func TestHedgingDelayedBodyThroughRealServer(t *testing.T) {
	headersRead := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush() // headers first; the body follows once the client has them
		<-headersRead
		_, _ = w.Write([]byte("late body"))
	}))
	defer server.Close()

	client := &http.Client{Transport: must(NewHedgingTransport(http.DefaultTransport, time.Hour))}
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	close(headersRead)
	body, err := io.ReadAll(resp.Body)
	if err != nil || string(body) != "late body" {
		t.Fatalf("delayed body = %q, %v", body, err)
	}
}
