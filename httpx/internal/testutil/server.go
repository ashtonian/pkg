// Package testutil provides testing utilities for the httpx package.
package testutil

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ashtonian/pkg/httpx"
)

// TestServer is a configurable test server for testing HTTP transports.
type TestServer struct {
	*httptest.Server

	mu           sync.RWMutex
	latency      time.Duration
	failRate     float64
	statusCode   int
	compression  string
	etag         string
	cacheControl string
	lastModified time.Time
	body         []byte
	contentType  string
	headers      http.Header

	requestCount atomic.Int64
	lastRequest  atomic.Pointer[http.Request]
}

// ServerOption configures a TestServer.
type ServerOption func(*TestServer)

// NewTestServer creates a new configurable test server.
func NewTestServer(opts ...ServerOption) *TestServer {
	ts := &TestServer{
		statusCode:  http.StatusOK,
		body:        []byte(`{"status":"ok"}`),
		contentType: "application/json",
		headers:     make(http.Header),
	}

	for _, opt := range opts {
		opt(ts)
	}

	ts.Server = httptest.NewServer(http.HandlerFunc(ts.handler))
	return ts
}

// NewTLSTestServer creates a new TLS test server.
func NewTLSTestServer(opts ...ServerOption) *TestServer {
	ts := &TestServer{
		statusCode:  http.StatusOK,
		body:        []byte(`{"status":"ok"}`),
		contentType: "application/json",
		headers:     make(http.Header),
	}

	for _, opt := range opts {
		opt(ts)
	}

	ts.Server = httptest.NewTLSServer(http.HandlerFunc(ts.handler))
	return ts
}

func (s *TestServer) handler(w http.ResponseWriter, r *http.Request) {
	s.requestCount.Add(1)
	s.lastRequest.Store(r)

	s.mu.RLock()
	latency := s.latency
	failRate := s.failRate
	statusCode := s.statusCode
	compression := s.compression
	etag := s.etag
	cacheControl := s.cacheControl
	lastModified := s.lastModified
	body := s.body
	contentType := s.contentType
	headers := s.headers.Clone()
	s.mu.RUnlock()

	// Simulate latency
	if latency > 0 {
		time.Sleep(latency)
	}

	// Simulate failures
	if failRate > 0 && rand.Float64() < failRate {
		http.Error(w, "simulated failure", http.StatusServiceUnavailable)
		return
	}

	// Check for conditional requests
	if etag != "" {
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
	}

	if !lastModified.IsZero() {
		ifModSince := r.Header.Get("If-Modified-Since")
		if ifModSince != "" {
			if t, err := http.ParseTime(ifModSince); err == nil {
				if !lastModified.After(t) {
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}
		}
		w.Header().Set("Last-Modified", lastModified.UTC().Format(http.TimeFormat))
	}

	// Set cache control
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}

	// Set custom headers
	for k, v := range headers {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}

	// Set content type
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	// Handle compression
	responseBody := body
	if compression != "" && strings.Contains(r.Header.Get("Accept-Encoding"), compression) {
		var buf bytes.Buffer
		var err error

		switch compression {
		case "gzip":
			gw := gzip.NewWriter(&buf)
			_, err = gw.Write(body)
			if err == nil {
				err = gw.Close()
			}
		case "deflate":
			fw, _ := flate.NewWriter(&buf, flate.DefaultCompression)
			_, err = fw.Write(body)
			if err == nil {
				err = fw.Close()
			}
		case "br":
			// Use the registry to get brotli compressor if available
			if compressor, ok := httpx.GetCompressor(httpx.EncodingBrotli); ok {
				compressed, compErr := compressor.Compress(body, &buf)
				if compErr == nil {
					responseBody = compressed
					w.Header().Set("Content-Encoding", compression)
					err = nil
				} else {
					err = compErr
				}
			} else {
				// Brotli not registered, skip compression
				err = nil
			}
		}

		if err == nil && compression != "br" {
			responseBody = buf.Bytes()
			w.Header().Set("Content-Encoding", compression)
		}
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
	w.WriteHeader(statusCode)
	w.Write(responseBody)
}

// WithLatency sets the response latency.
func WithLatency(d time.Duration) ServerOption {
	return func(s *TestServer) {
		s.latency = d
	}
}

// WithFailRate sets the failure rate (0.0-1.0).
func WithFailRate(rate float64) ServerOption {
	return func(s *TestServer) {
		s.failRate = rate
	}
}

// WithStatusCode sets the response status code.
func WithStatusCode(code int) ServerOption {
	return func(s *TestServer) {
		s.statusCode = code
	}
}

// WithCompression sets the compression encoding to use.
func WithCompression(encoding string) ServerOption {
	return func(s *TestServer) {
		s.compression = encoding
	}
}

// WithETag sets the ETag header value.
func WithETag(etag string) ServerOption {
	return func(s *TestServer) {
		s.etag = etag
	}
}

// WithCacheControl sets the Cache-Control header.
func WithCacheControl(value string) ServerOption {
	return func(s *TestServer) {
		s.cacheControl = value
	}
}

// WithLastModified sets the Last-Modified header.
func WithLastModified(t time.Time) ServerOption {
	return func(s *TestServer) {
		s.lastModified = t
	}
}

// WithBody sets the response body.
func WithBody(body []byte) ServerOption {
	return func(s *TestServer) {
		s.body = body
	}
}

// WithJSONBody sets the response body as JSON.
func WithJSONBody(v any) ServerOption {
	return func(s *TestServer) {
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		s.body = data
		s.contentType = "application/json"
	}
}

// WithContentType sets the Content-Type header.
func WithContentType(contentType string) ServerOption {
	return func(s *TestServer) {
		s.contentType = contentType
	}
}

// WithHeader adds a custom header.
func WithHeader(key, value string) ServerOption {
	return func(s *TestServer) {
		s.headers.Add(key, value)
	}
}

// SetLatency updates the latency at runtime.
func (s *TestServer) SetLatency(d time.Duration) *TestServer {
	s.mu.Lock()
	s.latency = d
	s.mu.Unlock()
	return s
}

// SetFailRate updates the fail rate at runtime.
func (s *TestServer) SetFailRate(rate float64) *TestServer {
	s.mu.Lock()
	s.failRate = rate
	s.mu.Unlock()
	return s
}

// SetStatusCode updates the status code at runtime.
func (s *TestServer) SetStatusCode(code int) *TestServer {
	s.mu.Lock()
	s.statusCode = code
	s.mu.Unlock()
	return s
}

// SetBody updates the response body at runtime.
func (s *TestServer) SetBody(body []byte) *TestServer {
	s.mu.Lock()
	s.body = body
	s.mu.Unlock()
	return s
}

// SetETag updates the ETag at runtime.
func (s *TestServer) SetETag(etag string) *TestServer {
	s.mu.Lock()
	s.etag = etag
	s.mu.Unlock()
	return s
}

// SetCacheControl updates the Cache-Control header at runtime.
func (s *TestServer) SetCacheControl(value string) *TestServer {
	s.mu.Lock()
	s.cacheControl = value
	s.mu.Unlock()
	return s
}

// RequestCount returns the number of requests received.
func (s *TestServer) RequestCount() int64 {
	return s.requestCount.Load()
}

// LastRequest returns the last request received.
func (s *TestServer) LastRequest() *http.Request {
	return s.lastRequest.Load()
}

// ResetCount resets the request counter.
func (s *TestServer) ResetCount() {
	s.requestCount.Store(0)
}

// MockTransport is a mock http.RoundTripper for testing.
type MockTransport struct {
	mu        sync.Mutex
	responses []*http.Response
	errors    []error
	index     int
	requests  []*http.Request
}

// NewMockTransport creates a new mock transport.
func NewMockTransport() *MockTransport {
	return &MockTransport{}
}

// AddResponse adds a response to be returned.
func (m *MockTransport) AddResponse(resp *http.Response) *MockTransport {
	m.mu.Lock()
	m.responses = append(m.responses, resp)
	m.errors = append(m.errors, nil)
	m.mu.Unlock()
	return m
}

// AddError adds an error to be returned.
func (m *MockTransport) AddError(err error) *MockTransport {
	m.mu.Lock()
	m.responses = append(m.responses, nil)
	m.errors = append(m.errors, err)
	m.mu.Unlock()
	return m
}

// RoundTrip implements http.RoundTripper.
func (m *MockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.requests = append(m.requests, req)

	if m.index >= len(m.responses) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	}

	resp := m.responses[m.index]
	err := m.errors[m.index]
	m.index++

	return resp, err
}

// Requests returns all requests made through this transport.
func (m *MockTransport) Requests() []*http.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.requests
}

// Reset resets the mock transport state.
func (m *MockTransport) Reset() {
	m.mu.Lock()
	m.index = 0
	m.requests = nil
	m.mu.Unlock()
}

// NewResponse creates a new http.Response for testing.
func NewResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

// NewJSONResponse creates a new JSON http.Response for testing.
func NewJSONResponse(statusCode int, v any) *http.Response {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	resp := &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(bytes.NewReader(data)),
		Header:     make(http.Header),
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp
}
