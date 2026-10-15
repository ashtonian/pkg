//go:build integration

package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerTokenProvider(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := NewBearerTokenProvider("my-token")
	transport := must(NewAuthTransport(http.DefaultTransport, AuthBinding{URL: server.URL, Provider: provider}))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if receivedHeader != "Bearer my-token" {
		t.Errorf("expected 'Bearer my-token', got '%s'", receivedHeader)
	}
}

func TestBearerTokenProvider_SetToken(t *testing.T) {
	provider := NewBearerTokenProvider("initial")
	provider.SetToken("updated")

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	provider.AddAuth(req)

	if req.Header.Get("Authorization") != "Bearer updated" {
		t.Errorf("expected 'Bearer updated', got '%s'", req.Header.Get("Authorization"))
	}
}

func TestBearerTokenProvider_Refresh(t *testing.T) {
	callCount := 0
	provider := NewRefreshableBearerTokenProvider("initial", func() (string, error) {
		callCount++
		return "refreshed", nil
	})

	err := provider.Refresh()
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}

	req, _ := http.NewRequest("GET", "http://example.com", nil)
	provider.AddAuth(req)

	if req.Header.Get("Authorization") != "Bearer refreshed" {
		t.Errorf("expected 'Bearer refreshed', got '%s'", req.Header.Get("Authorization"))
	}
	if callCount != 1 {
		t.Errorf("expected 1 refresh call, got %d", callCount)
	}
}

func TestBearerTokenProvider_RefreshError(t *testing.T) {
	provider := NewRefreshableBearerTokenProvider("initial", func() (string, error) {
		return "", errors.New("refresh error")
	})

	err := provider.Refresh()
	if err == nil {
		t.Error("expected error from refresh")
	}

	// Token should be unchanged
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	provider.AddAuth(req)

	if req.Header.Get("Authorization") != "Bearer initial" {
		t.Errorf("expected unchanged token after error")
	}
}

func TestBearerTokenProvider_NoRefreshFunc(t *testing.T) {
	provider := NewBearerTokenProvider("token")
	err := provider.Refresh()
	if err != nil {
		t.Error("expected no error when no refresh func")
	}
}

func TestBasicAuthProvider(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := NewBasicAuthProvider("user", "pass")
	transport := must(NewAuthTransport(http.DefaultTransport, AuthBinding{URL: server.URL, Provider: provider}))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	// "user:pass" base64 encoded is "dXNlcjpwYXNz"
	expected := "Basic dXNlcjpwYXNz"
	if receivedHeader != expected {
		t.Errorf("expected '%s', got '%s'", expected, receivedHeader)
	}
}

func TestAPIKeyProvider(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := NewAPIKeyProvider("my-api-key")
	transport := must(NewAuthTransport(http.DefaultTransport, AuthBinding{URL: server.URL, Provider: provider}))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if receivedHeader != "my-api-key" {
		t.Errorf("expected 'my-api-key', got '%s'", receivedHeader)
	}
}

func TestAPIKeyProviderWithHeader(t *testing.T) {
	var receivedHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("X-Custom-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := NewAPIKeyProviderWithHeader("my-key", "X-Custom-Key")
	transport := must(NewAuthTransport(http.DefaultTransport, AuthBinding{URL: server.URL, Provider: provider}))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if receivedHeader != "my-key" {
		t.Errorf("expected 'my-key', got '%s'", receivedHeader)
	}
}

func TestQueryParamAuthProvider(t *testing.T) {
	var receivedQuery string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedQuery = r.URL.Query().Get("api_key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := NewQueryParamAuthProvider("api_key", "secret123")
	transport := must(NewAuthTransport(http.DefaultTransport, AuthBinding{URL: server.URL, Provider: provider}))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if receivedQuery != "secret123" {
		t.Errorf("expected 'secret123', got '%s'", receivedQuery)
	}
}

func TestChainAuthProvider(t *testing.T) {
	var authHeader, apiKeyHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		apiKeyHeader = r.Header.Get("X-API-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := NewChainAuthProvider(
		NewBearerTokenProvider("token"),
		NewAPIKeyProvider("api-key"),
	)
	transport := must(NewAuthTransport(http.DefaultTransport, AuthBinding{URL: server.URL, Provider: provider}))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if authHeader != "Bearer token" {
		t.Errorf("expected 'Bearer token', got '%s'", authHeader)
	}
	if apiKeyHeader != "api-key" {
		t.Errorf("expected 'api-key', got '%s'", apiKeyHeader)
	}
}

func TestFuncAuthProvider(t *testing.T) {
	var customHeader string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		customHeader = r.Header.Get("X-Custom-Auth")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider := FuncAuthProvider(func(req *http.Request) error {
		req.Header.Set("X-Custom-Auth", "custom-value")
		return nil
	})
	transport := must(NewAuthTransport(http.DefaultTransport, AuthBinding{URL: server.URL, Provider: provider}))
	client := &http.Client{Transport: transport}

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()

	if customHeader != "custom-value" {
		t.Errorf("expected 'custom-value', got '%s'", customHeader)
	}
}

func TestFuncAuthProvider_Error(t *testing.T) {
	provider := FuncAuthProvider(func(req *http.Request) error {
		return errors.New("auth error")
	})
	transport := must(NewAuthTransport(http.DefaultTransport, AuthBinding{URL: "http://example.com", Provider: provider}))
	client := &http.Client{Transport: transport}

	_, err := client.Get("http://example.com")
	if err == nil {
		t.Error("expected error from auth provider")
	}
}
