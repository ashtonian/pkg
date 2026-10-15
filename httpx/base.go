package httpx

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"
)

// BaseOption configures a base transport.
type BaseOption func(*http.Transport)

// NewBaseTransport creates a tuned *http.Transport with production defaults.
//
// Default configuration:
//   - MaxIdleConns: 100
//   - MaxIdleConnsPerHost: 10
//   - IdleConnTimeout: 90s
//   - TLSHandshakeTimeout: 10s
//   - ResponseHeaderTimeout: 30s (no per-request bound otherwise — a server
//     that accepts the connection but never sends headers would hang the call)
//   - ForceAttemptHTTP2: true
//   - DisableCompression: true (compression is handled by CompressionTransport)
//
// Negative counts, durations or buffer sizes and a nil dialer are rejected
// with an error wrapping [ErrInvalidConfig].
func NewBaseTransport(opts ...BaseOption) (*http.Transport, error) {
	t := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true, // We handle compression ourselves
	}

	for _, opt := range opts {
		opt(t)
	}

	if err := validateBaseTransport(t); err != nil {
		return nil, err
	}
	return t, nil
}

func validateBaseTransport(t *http.Transport) error {
	var errs []error
	if t.DialContext == nil {
		errs = append(errs, invalidConfig("base transport: WithDialer requires a non-nil dialer"))
	}
	for _, c := range []struct {
		name string
		n    int
	}{
		{"WithMaxIdleConns", t.MaxIdleConns},
		{"WithMaxIdleConnsPerHost", t.MaxIdleConnsPerHost},
		{"WithMaxConnsPerHost", t.MaxConnsPerHost},
		{"WithWriteBufferSize", t.WriteBufferSize},
		{"WithReadBufferSize", t.ReadBufferSize},
	} {
		if c.n < 0 {
			errs = append(errs, invalidConfig("base transport: %s must be >= 0, got %d", c.name, c.n))
		}
	}
	for _, c := range []struct {
		name string
		d    time.Duration
	}{
		{"WithIdleConnTimeout", t.IdleConnTimeout},
		{"WithTLSHandshakeTimeout", t.TLSHandshakeTimeout},
		{"WithResponseHeaderTimeout", t.ResponseHeaderTimeout},
		{"WithExpectContinueTimeout", t.ExpectContinueTimeout},
	} {
		if c.d < 0 {
			errs = append(errs, invalidConfig("base transport: %s must be >= 0, got %s", c.name, c.d))
		}
	}
	return errors.Join(errs...)
}

// WithMaxIdleConns sets the maximum number of idle connections across all hosts.
func WithMaxIdleConns(n int) BaseOption {
	return func(t *http.Transport) {
		t.MaxIdleConns = n
	}
}

// WithMaxIdleConnsPerHost sets the maximum number of idle connections per host.
func WithMaxIdleConnsPerHost(n int) BaseOption {
	return func(t *http.Transport) {
		t.MaxIdleConnsPerHost = n
	}
}

// WithIdleConnTimeout sets how long idle connections remain open.
func WithIdleConnTimeout(d time.Duration) BaseOption {
	return func(t *http.Transport) {
		t.IdleConnTimeout = d
	}
}

// WithTLSHandshakeTimeout sets the TLS handshake timeout.
func WithTLSHandshakeTimeout(d time.Duration) BaseOption {
	return func(t *http.Transport) {
		t.TLSHandshakeTimeout = d
	}
}

// WithHTTP2 enables HTTP/2 (this is the default).
func WithHTTP2() BaseOption {
	return func(t *http.Transport) {
		t.ForceAttemptHTTP2 = true
	}
}

// WithDisableHTTP2 disables HTTP/2, forcing HTTP/1.1.
func WithDisableHTTP2() BaseOption {
	return func(t *http.Transport) {
		t.ForceAttemptHTTP2 = false
	}
}

// WithDialer sets a custom dialer for establishing connections.
func WithDialer(d *net.Dialer) BaseOption {
	return func(t *http.Transport) {
		if d == nil {
			t.DialContext = nil // rejected by NewBaseTransport
			return
		}
		t.DialContext = d.DialContext
	}
}

// WithTLSConfig sets a custom TLS configuration.
func WithTLSConfig(cfg *tls.Config) BaseOption {
	return func(t *http.Transport) {
		t.TLSClientConfig = cfg
	}
}

// WithDisableKeepAlives disables HTTP keep-alives.
// This forces a new connection for each request.
func WithDisableKeepAlives() BaseOption {
	return func(t *http.Transport) {
		t.DisableKeepAlives = true
	}
}

// WithMaxConnsPerHost limits the total number of connections per host.
// This includes dialing, active, and idle connections.
func WithMaxConnsPerHost(n int) BaseOption {
	return func(t *http.Transport) {
		t.MaxConnsPerHost = n
	}
}

// WithResponseHeaderTimeout sets the timeout for reading response headers.
func WithResponseHeaderTimeout(d time.Duration) BaseOption {
	return func(t *http.Transport) {
		t.ResponseHeaderTimeout = d
	}
}

// WithExpectContinueTimeout sets how long the transport waits for a server's
// first response after sending request headers when the request had an
// "Expect: 100-continue" header (default: 1s).
func WithExpectContinueTimeout(d time.Duration) BaseOption {
	return func(t *http.Transport) {
		t.ExpectContinueTimeout = d
	}
}

// WithWriteBufferSize sets the write buffer size for connections.
func WithWriteBufferSize(size int) BaseOption {
	return func(t *http.Transport) {
		t.WriteBufferSize = size
	}
}

// WithReadBufferSize sets the read buffer size for connections.
func WithReadBufferSize(size int) BaseOption {
	return func(t *http.Transport) {
		t.ReadBufferSize = size
	}
}
