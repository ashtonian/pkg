//go:build integration

package httpx_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

// benchServer returns a test server that handles all transport needs:
// JSON body, Cache-Control, ETag with If-None-Match support.
func benchServer(b *testing.B) *httptest.Server {
	b.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"bench-etag"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("ETag", `"bench-etag"`)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}))
	b.Cleanup(server.Close)
	return server
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func doGet(b *testing.B, client *http.Client, url string) {
	b.Helper()
	resp, err := client.Get(url)
	if err != nil {
		b.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// BenchmarkTransport measures per-transport overhead against a vanilla http.Client baseline.
func BenchmarkTransport(b *testing.B) {
	b.Run("Baseline", func(b *testing.B) {
		server := benchServer(b)
		client := &http.Client{}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("Base", func(b *testing.B) {
		server := benchServer(b)
		client := &http.Client{Transport: must(httpx.NewBaseTransport())}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("Compression", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithCompression()))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("Cache_Hit", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithCache(nil)))
		resp, _ := client.Get(server.URL)
		resp.Body.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("Cache_Miss", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithCache(nil)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := range b.N {
			resp, err := client.Get(fmt.Sprintf("%s?i=%d", server.URL, i))
			if err != nil {
				b.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	})

	b.Run("Retry_NoRetry", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithRetry(httpx.WithMaxAttempts(3))))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("CircuitBreaker_Closed", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithCircuitBreaker()))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("CircuitBreaker_Open", func(b *testing.B) {
		down := roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("down") })
		transport := must(httpx.NewCircuitTransport(down, httpx.WithFailureThreshold(1)))
		trip, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
		if _, err := transport.RoundTrip(trip); err == nil {
			b.Fatal("the failing base should have tripped the circuit")
		}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			req, _ := http.NewRequest(http.MethodGet, "http://example.com", nil)
			if _, err := transport.RoundTrip(req); !errors.Is(err, httpx.ErrCircuitOpen) {
				b.Fatal("expected circuit to be open")
			}
		}
	})

	b.Run("Limiter", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithLimiter(100)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("RateLimit", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithRateLimit(1000000, 1000000)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("Auth", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithAuth(server.URL, httpx.NewBearerTokenProvider("token"))))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("Metrics", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithMetrics(&httpx.CountingHook{})))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("Logging", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(httpx.WithLogging(&httpx.StructuredLoggingHook{})))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("Hedging", func(b *testing.B) {
		server := benchServer(b)
		// Delay longer than response time — measures goroutine+channel overhead without firing a hedge.
		client := must(httpx.NewClient(httpx.WithHedging(10 * time.Second)))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})
}

// BenchmarkFullStack measures combined transport stacks.
func BenchmarkFullStack(b *testing.B) {
	b.Run("AllTransports", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(
			httpx.WithCompression(),
			httpx.WithRetry(httpx.WithMaxAttempts(3)),
			httpx.WithCircuitBreaker(),
			httpx.WithAuth(server.URL, httpx.NewBearerTokenProvider("token")),
			httpx.WithMetrics(&httpx.CountingHook{}),
			httpx.WithLogging(&httpx.StructuredLoggingHook{}),
			httpx.WithTimeout(30*time.Second),
		))
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})

	b.Run("CacheHit", func(b *testing.B) {
		server := benchServer(b)
		client := must(httpx.NewClient(
			httpx.WithCompression(),
			httpx.WithCache(nil),
			httpx.WithRetry(httpx.WithMaxAttempts(3)),
			httpx.WithCircuitBreaker(),
			httpx.WithAuth(server.URL, httpx.NewBearerTokenProvider("token")),
			httpx.WithMetrics(&httpx.CountingHook{}),
			httpx.WithTimeout(30*time.Second),
		))
		resp, _ := client.Get(server.URL)
		resp.Body.Close()
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			doGet(b, client, server.URL)
		}
	})
}

// BenchmarkInternal measures internal component performance.
func BenchmarkInternal(b *testing.B) {
	b.Run("BufferPool", func(b *testing.B) {
		pool := must(httpx.NewBufferPool(32 * 1024))
		data := bytes.Repeat([]byte("x"), 1024)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			buf := pool.Get()
			buf.Write(data)
			pool.Put(buf)
		}
	})

	b.Run("BufferPool_Parallel", func(b *testing.B) {
		pool := must(httpx.NewBufferPool(32 * 1024))
		data := bytes.Repeat([]byte("x"), 1024)
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				buf := pool.Get()
				buf.Write(data)
				pool.Put(buf)
			}
		})
	})

	b.Run("JSON_Encode", func(b *testing.B) {
		type Data struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			Email   string `json:"email"`
			Active  bool   `json:"active"`
			Created string `json:"created"`
		}
		data := Data{ID: 1, Name: "John Doe", Email: "john@example.com", Active: true, Created: "2024-01-01T00:00:00Z"}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if _, err := httpx.JSON.Encode(data); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("JSON_Decode", func(b *testing.B) {
		type Data struct {
			ID      int    `json:"id"`
			Name    string `json:"name"`
			Email   string `json:"email"`
			Active  bool   `json:"active"`
			Created string `json:"created"`
		}
		encoded := []byte(`{"id":1,"name":"John Doe","email":"john@example.com","active":true,"created":"2024-01-01T00:00:00Z"}`)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			var d Data
			if err := httpx.JSON.Decode(encoded, &d); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("MemoryCache_Get", func(b *testing.B) {
		cache := must(httpx.NewMemoryCache())
		cache.Set("key", &httpx.CachedResponse{StatusCode: 200, Body: []byte("test")}, time.Hour)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			cache.Get("key")
		}
	})

	b.Run("MemoryCache_Set", func(b *testing.B) {
		cache := must(httpx.NewMemoryCache())
		resp := &httpx.CachedResponse{StatusCode: 200, Body: []byte("test")}
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			cache.Set("key", resp, time.Hour)
		}
	})

	b.Run("MemoryCache_Parallel", func(b *testing.B) {
		cache := must(httpx.NewMemoryCache())
		resp := &httpx.CachedResponse{StatusCode: 200, Body: []byte("test")}
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				key := fmt.Sprintf("key-%d", i%100)
				if i%2 == 0 {
					cache.Set(key, resp, time.Hour)
				} else {
					cache.Get(key)
				}
				i++
			}
		})
	})
}
