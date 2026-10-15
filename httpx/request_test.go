//go:build integration

package httpx_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

var ctx = context.Background()

type reqTestUser struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func TestGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reqTestUser{ID: 1, Name: "Alice"})
	}))
	defer srv.Close()

	user, resp, err := httpx.Get[reqTestUser](ctx, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if user.ID != 1 || user.Name != "Alice" {
		t.Errorf("expected {1, Alice}, got %+v", user)
	}

	// Body should still be readable.
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(body) == 0 {
		t.Error("expected body to be re-readable")
	}
}

func TestPost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		var input reqTestUser
		json.NewDecoder(r.Body).Decode(&input)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reqTestUser{ID: 42, Name: input.Name})
	}))
	defer srv.Close()

	created, resp, err := httpx.Post[reqTestUser](ctx, srv.Client(), srv.URL, reqTestUser{Name: "Bob"})
	if err != nil {
		t.Fatalf("Post failed: %v", err)
	}
	resp.Body.Close()
	if created.ID != 42 || created.Name != "Bob" {
		t.Errorf("expected {42, Bob}, got %+v", created)
	}
}

func TestPut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reqTestUser{ID: 1, Name: "Updated"})
	}))
	defer srv.Close()

	user, resp, err := httpx.Put[reqTestUser](ctx, srv.Client(), srv.URL, reqTestUser{Name: "Updated"})
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	resp.Body.Close()
	if user.Name != "Updated" {
		t.Errorf("expected Updated, got %s", user.Name)
	}
}

func TestPatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reqTestUser{ID: 1, Name: "Patched"})
	}))
	defer srv.Close()

	user, resp, err := httpx.Patch[reqTestUser](ctx, srv.Client(), srv.URL, reqTestUser{Name: "Patched"})
	if err != nil {
		t.Fatalf("Patch failed: %v", err)
	}
	resp.Body.Close()
	if user.Name != "Patched" {
		t.Errorf("expected Patched, got %s", user.Name)
	}
}

func TestDelete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	resp, err := httpx.Delete(ctx, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestGet_NonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":"not found"}`)
	}))
	defer srv.Close()

	user, resp, err := httpx.Get[reqTestUser](ctx, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("expected nil error for 404, got %v", err)
	}
	if resp.StatusCode != 404 {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	// T should be zero.
	if user.ID != 0 || user.Name != "" {
		t.Errorf("expected zero value, got %+v", user)
	}
	// Body should be intact for error inspection.
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "not found") {
		t.Errorf("expected error body, got %s", string(body))
	}
}

func TestGet_NoCodec(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	_, _, err := httpx.Get[reqTestUser](ctx, srv.Client(), srv.URL)
	if !errors.Is(err, httpx.ErrNoCodec) {
		t.Errorf("expected ErrNoCodec, got %v", err)
	}
}

func TestGet_MultipleTypes(t *testing.T) {
	type Widget struct {
		Serial string `json:"serial"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user":
			json.NewEncoder(w).Encode(reqTestUser{ID: 1, Name: "Alice"})
		case "/widget":
			json.NewEncoder(w).Encode(Widget{Serial: "ABC123"})
		}
	}))
	defer srv.Close()

	user, _, err := httpx.Get[reqTestUser](ctx, srv.Client(), srv.URL+"/user")
	if err != nil {
		t.Fatalf("Get user failed: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("expected Alice, got %s", user.Name)
	}

	widget, _, err := httpx.Get[Widget](ctx, srv.Client(), srv.URL+"/widget")
	if err != nil {
		t.Fatalf("Get widget failed: %v", err)
	}
	if widget.Serial != "ABC123" {
		t.Errorf("expected ABC123, got %s", widget.Serial)
	}
}

func TestGet_WithFullStack(t *testing.T) {
	var requestCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("expected auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=3600")
		json.NewEncoder(w).Encode(reqTestUser{ID: 1, Name: "Alice"})
	}))
	defer srv.Close()

	client := must(httpx.NewClient(
		httpx.WithRetry(
			httpx.WithMaxAttempts(3),
			httpx.WithBackoff(1*time.Millisecond, 10*time.Millisecond, 1.0),
		),
		httpx.WithCache(nil),
		httpx.WithAuth(srv.URL, httpx.NewBearerTokenProvider("test-token")),
		httpx.WithCompression(),
	))

	// First request: 503 -> retry -> 200.
	user, resp, err := httpx.Get[reqTestUser](ctx, client, srv.URL)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	resp.Body.Close()
	if user.Name != "Alice" {
		t.Errorf("expected Alice, got %s", user.Name)
	}
	if requestCount.Load() != 2 {
		t.Errorf("expected 2 requests (503 + retry), got %d", requestCount.Load())
	}

	// Second request: cache hit.
	user2, resp2, err := httpx.Get[reqTestUser](ctx, client, srv.URL)
	if err != nil {
		t.Fatalf("Get cached failed: %v", err)
	}
	resp2.Body.Close()
	if user2.Name != "Alice" {
		t.Errorf("expected Alice, got %s", user2.Name)
	}
	if requestCount.Load() != 2 {
		t.Errorf("expected 2 total requests (cache hit), got %d", requestCount.Load())
	}
}

func TestDo_CustomRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Custom") != "value" {
			t.Error("expected custom header")
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reqTestUser{ID: 1, Name: "Custom"})
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("X-Custom", "value")

	user, resp, err := httpx.Do[reqTestUser](srv.Client(), req)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	resp.Body.Close()
	if user.Name != "Custom" {
		t.Errorf("expected Custom, got %s", user.Name)
	}
}

func TestPost_NilBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reqTestUser{ID: 1, Name: "NoBody"})
	}))
	defer srv.Close()

	user, resp, err := httpx.Post[reqTestUser](ctx, srv.Client(), srv.URL, nil)
	if err != nil {
		t.Fatalf("Post nil body failed: %v", err)
	}
	resp.Body.Close()
	if user.Name != "NoBody" {
		t.Errorf("expected NoBody, got %s", user.Name)
	}
}

func TestPost_ReaderBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reqTestUser{ID: 1, Name: string(body)})
	}))
	defer srv.Close()

	user, resp, err := httpx.Post[reqTestUser](ctx, srv.Client(), srv.URL, strings.NewReader("raw-data"))
	if err != nil {
		t.Fatalf("Post reader body failed: %v", err)
	}
	resp.Body.Close()
	if user.Name != "raw-data" {
		t.Errorf("expected raw-data, got %s", user.Name)
	}
}

func TestGet_SetsAcceptHeader(t *testing.T) {
	var receivedAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1}`)
	}))
	defer srv.Close()

	httpx.Get[reqTestUser](ctx, srv.Client(), srv.URL)
	if receivedAccept == "" {
		t.Error("expected Accept header to be set")
	}
}
