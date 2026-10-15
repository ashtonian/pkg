//go:build unit

package httpx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestNewChain(t *testing.T) {
	chain := NewChain(
		WithCompressionT(),
		WithRetryT(WithMaxAttempts(3)),
	)

	if len(chain) != 2 {
		t.Errorf("expected 2 functions in chain, got %d", len(chain))
	}
}

func TestChain_Append(t *testing.T) {
	chain := NewChain(WithCompressionT())
	newChain := chain.Append(WithRetryT())

	if len(chain) != 1 {
		t.Error("original chain should not be modified")
	}
	if len(newChain) != 2 {
		t.Errorf("expected 2 functions in new chain, got %d", len(newChain))
	}
}

func TestChain_Prepend(t *testing.T) {
	chain := NewChain(WithRetryT())
	newChain := chain.Prepend(WithCompressionT())

	if len(chain) != 1 {
		t.Error("original chain should not be modified")
	}
	if len(newChain) != 2 {
		t.Errorf("expected 2 functions in new chain, got %d", len(newChain))
	}
}

func TestChain_Build(t *testing.T) {
	chain := NewChain(
		WithCompressionT(),
		WithRetryT(),
	)

	transport := must(chain.Build(nil))

	if transport == nil {
		t.Fatal("expected non-nil transport")
	}

	// Verify the chain is built correctly by checking types
	_, ok := transport.(*RetryTransport)
	if !ok {
		t.Error("expected outermost transport to be RetryTransport")
	}
}

func TestChain_Build_WithBase(t *testing.T) {
	base := must(NewBaseTransport(WithMaxIdleConns(50)))
	chain := NewChain(WithCompressionT())

	transport := must(chain.Build(base))

	comp, ok := transport.(*CompressionTransport)
	if !ok {
		t.Fatal("expected CompressionTransport")
	}

	httpT, ok := comp.Unwrap().(*http.Transport)
	if !ok {
		t.Fatal("expected base to be *http.Transport")
	}

	if httpT.MaxIdleConns != 50 {
		t.Errorf("expected MaxIdleConns 50, got %d", httpT.MaxIdleConns)
	}
}

func TestChain_Client(t *testing.T) {
	chain := NewChain(WithCompressionT())
	client := must(chain.Client(nil, 30*time.Second))

	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Timeout != 30*time.Second {
		t.Errorf("expected timeout 30s, got %v", client.Timeout)
	}
}

func TestChain_Integration(t *testing.T) {
	requestCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.Header.Get("Accept-Encoding") == "" {
			t.Error("expected Accept-Encoding header from compression")
		}
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Error("expected Authorization header from auth")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	chain := NewChain(
		WithCompressionT(),
		WithRetryT(WithMaxAttempts(3)),
		WithAuthT(server.URL, NewBearerTokenProvider("token")),
	)
	client := must(chain.Client(nil, 30*time.Second))

	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if requestCount != 1 {
		t.Errorf("expected 1 request, got %d", requestCount)
	}
}

func TestDefaultChain(t *testing.T) {
	chain := DefaultChain()

	if len(chain) != 3 {
		t.Errorf("expected 3 transports in default chain, got %d", len(chain))
	}

	transport := must(chain.Build(nil))
	var layers []string
	for rt := http.RoundTripper(transport); rt != nil; rt = unwrapTransport(rt) {
		layers = append(layers, fmt.Sprintf("%T", rt))
	}
	want := []string{"*httpx.RetryTransport", "*httpx.CircuitTransport", "*httpx.CompressionTransport", "*http.Transport"}
	if !slices.Equal(layers, want) {
		t.Fatalf("layers outer to inner = %v, want %v (the breaker counts every retry attempt)", layers, want)
	}
}

func TestResilientChain(t *testing.T) {
	chain := ResilientChain()

	if len(chain) < 5 {
		t.Errorf("expected at least 5 transports in resilient chain, got %d", len(chain))
	}

	transport := must(chain.Build(nil))
	if transport == nil {
		t.Error("expected non-nil transport")
	}
}

func TestAPIClientChain(t *testing.T) {
	chain := APIClientChain()

	if len(chain) != 3 {
		t.Errorf("expected 3 transports in API client chain, got %d", len(chain))
	}

	transport := must(chain.Build(nil))
	if transport == nil {
		t.Error("expected non-nil transport")
	}
}

func TestTransportFuncs(t *testing.T) {
	tests := []struct {
		name string
		fn   TransportFunc
	}{
		{"Compression", WithCompressionT()},
		{"Cache", WithCacheT(nil)},
		{"Retry", WithRetryT()},
		{"Metrics", WithMetricsT(nil)},
		{"Logging", WithLoggingT(nil)},
		{"Limiter", WithLimiterT(10)},
		{"RateLimit", WithRateLimitT(10, 5)},
		{"CircuitBreaker", WithCircuitBreakerT()},
		{"Hedging", WithHedgingT(100 * time.Millisecond)},
		{"Auth", WithAuthT("http://example.com", NewBearerTokenProvider("token"))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := must(NewBaseTransport())
			wrapped, err := tt.fn(base)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if wrapped == nil {
				t.Error("expected non-nil transport")
			}
		})
	}
}

func TestChain_EmptyChain(t *testing.T) {
	chain := NewChain()
	transport := must(chain.Build(nil))

	// Should return a base transport
	_, ok := transport.(*http.Transport)
	if !ok {
		t.Error("expected *http.Transport for empty chain")
	}
}
