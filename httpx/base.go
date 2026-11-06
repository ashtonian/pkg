package httpx

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"
)

// BaseOption configures a base transport.
type BaseOption func(*baseConfig)

type baseConfig struct {
	*http.Transport
	dialer    *net.Dialer
	egress    *EgressPolicy
	egressSet bool
	configErr error
}

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
	cfg := defaultBaseConfig()
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.dialer == nil {
		return nil, invalidConfig("base transport: WithDialer requires a non-nil dialer")
	}
	if cfg.egressSet && cfg.egress == nil {
		return nil, invalidConfig("base transport: WithEgressPolicy requires a non-nil policy")
	}
	if cfg.configErr != nil {
		return nil, cfg.configErr
	}
	if cfg.egress != nil && (cfg.DialContext != nil || cfg.DialTLSContext != nil || cfg.DialTLS != nil) {
		return nil, invalidConfig("base transport: egress policy requires WithDialer, not custom transport dial functions")
	}
	dialer := *cfg.dialer
	if cfg.egress != nil {
		cfg.egress.guard(&dialer)
		cfg.Proxy = nil
	}
	if cfg.DialContext == nil {
		cfg.DialContext = dialer.DialContext
	}
	if err := validateBaseTransport(cfg.Transport); err != nil {
		return nil, err
	}
	return cfg.Transport, nil
}

func defaultBaseConfig() *baseConfig {
	return &baseConfig{dialer: &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}, Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true, // We handle compression ourselves
	}}
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
	return func(t *baseConfig) {
		t.MaxIdleConns = n
	}
}

// WithMaxIdleConnsPerHost sets the maximum number of idle connections per host.
func WithMaxIdleConnsPerHost(n int) BaseOption {
	return func(t *baseConfig) {
		t.MaxIdleConnsPerHost = n
	}
}

// WithIdleConnTimeout sets how long idle connections remain open.
func WithIdleConnTimeout(d time.Duration) BaseOption {
	return func(t *baseConfig) {
		t.IdleConnTimeout = d
	}
}

// WithTLSHandshakeTimeout sets the TLS handshake timeout.
func WithTLSHandshakeTimeout(d time.Duration) BaseOption {
	return func(t *baseConfig) {
		t.TLSHandshakeTimeout = d
	}
}

// WithHTTP2 enables HTTP/2 (this is the default).
func WithHTTP2() BaseOption {
	return func(t *baseConfig) {
		t.ForceAttemptHTTP2 = true
	}
}

// WithDisableHTTP2 disables HTTP/2, forcing HTTP/1.1.
func WithDisableHTTP2() BaseOption {
	return func(t *baseConfig) {
		t.ForceAttemptHTTP2 = false
	}
}

// WithDialer sets a custom dialer for establishing connections.
func WithDialer(d *net.Dialer) BaseOption {
	return func(t *baseConfig) {
		t.dialer = d
		t.DialContext = nil
	}
}

// WithTLSConfig sets a custom TLS configuration.
func WithTLSConfig(cfg *tls.Config) BaseOption {
	return func(t *baseConfig) {
		t.TLSClientConfig = cfg
	}
}

// WithDisableKeepAlives disables HTTP keep-alives.
// This forces a new connection for each request.
func WithDisableKeepAlives() BaseOption {
	return func(t *baseConfig) {
		t.DisableKeepAlives = true
	}
}

// WithMaxConnsPerHost limits the total number of connections per host.
// This includes dialing, active, and idle connections.
func WithMaxConnsPerHost(n int) BaseOption {
	return func(t *baseConfig) {
		t.MaxConnsPerHost = n
	}
}

// WithResponseHeaderTimeout sets the timeout for reading response headers.
func WithResponseHeaderTimeout(d time.Duration) BaseOption {
	return func(t *baseConfig) {
		t.ResponseHeaderTimeout = d
	}
}

// WithExpectContinueTimeout sets how long the transport waits for a server's
// first response after sending request headers when the request had an
// "Expect: 100-continue" header (default: 1s).
func WithExpectContinueTimeout(d time.Duration) BaseOption {
	return func(t *baseConfig) {
		t.ExpectContinueTimeout = d
	}
}

// WithWriteBufferSize sets the write buffer size for connections.
func WithWriteBufferSize(size int) BaseOption {
	return func(t *baseConfig) {
		t.WriteBufferSize = size
	}
}

// WithReadBufferSize sets the read buffer size for connections.
func WithReadBufferSize(size int) BaseOption {
	return func(t *baseConfig) {
		t.ReadBufferSize = size
	}
}

// WithTransportSettings configures standard transport fields not covered by a
// named option. fn runs during construction. WithEgressPolicy disables proxies
// and rejects custom DialContext/DialTLS/DialTLSContext functions; use WithDialer
// to compose a resolver or control hook with policy enforcement.
func WithTransportSettings(fn func(*http.Transport)) BaseOption {
	return func(cfg *baseConfig) {
		if fn == nil {
			cfg.configErr = invalidConfig("base transport: settings callback is nil")
			return
		}
		fn(cfg.Transport)
	}
}
