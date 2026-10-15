//go:build unit

package httpx

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

func TestNewBaseTransport(t *testing.T) {
	transport := must(NewBaseTransport())

	if transport == nil {
		t.Fatal("expected non-nil transport")
	}

	// Check defaults
	if transport.MaxIdleConns != 100 {
		t.Errorf("expected MaxIdleConns 100, got %d", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 10 {
		t.Errorf("expected MaxIdleConnsPerHost 10, got %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("expected IdleConnTimeout 90s, got %v", transport.IdleConnTimeout)
	}
	if transport.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("expected TLSHandshakeTimeout 10s, got %v", transport.TLSHandshakeTimeout)
	}
	if !transport.ForceAttemptHTTP2 {
		t.Error("expected ForceAttemptHTTP2 to be true")
	}
	if !transport.DisableCompression {
		t.Error("expected DisableCompression to be true")
	}
}

func TestWithMaxIdleConns(t *testing.T) {
	transport := must(NewBaseTransport(WithMaxIdleConns(200)))
	if transport.MaxIdleConns != 200 {
		t.Errorf("expected MaxIdleConns 200, got %d", transport.MaxIdleConns)
	}
}

func TestWithMaxIdleConnsPerHost(t *testing.T) {
	transport := must(NewBaseTransport(WithMaxIdleConnsPerHost(20)))
	if transport.MaxIdleConnsPerHost != 20 {
		t.Errorf("expected MaxIdleConnsPerHost 20, got %d", transport.MaxIdleConnsPerHost)
	}
}

func TestWithIdleConnTimeout(t *testing.T) {
	transport := must(NewBaseTransport(WithIdleConnTimeout(60 * time.Second)))
	if transport.IdleConnTimeout != 60*time.Second {
		t.Errorf("expected IdleConnTimeout 60s, got %v", transport.IdleConnTimeout)
	}
}

func TestWithTLSHandshakeTimeout(t *testing.T) {
	transport := must(NewBaseTransport(WithTLSHandshakeTimeout(5 * time.Second)))
	if transport.TLSHandshakeTimeout != 5*time.Second {
		t.Errorf("expected TLSHandshakeTimeout 5s, got %v", transport.TLSHandshakeTimeout)
	}
}

func TestWithHTTP2(t *testing.T) {
	transport := must(NewBaseTransport(WithHTTP2()))
	if !transport.ForceAttemptHTTP2 {
		t.Error("expected ForceAttemptHTTP2 to be true")
	}
}

func TestWithDisableHTTP2(t *testing.T) {
	transport := must(NewBaseTransport(WithDisableHTTP2()))
	if transport.ForceAttemptHTTP2 {
		t.Error("expected ForceAttemptHTTP2 to be false")
	}
}

func TestWithDialer(t *testing.T) {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
	}
	transport := must(NewBaseTransport(WithDialer(dialer)))
	if transport.DialContext == nil {
		t.Error("expected DialContext to be set")
	}
}

func TestWithTLSConfig(t *testing.T) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	transport := must(NewBaseTransport(WithTLSConfig(tlsConfig)))
	if transport.TLSClientConfig != tlsConfig {
		t.Error("expected TLSClientConfig to be set")
	}
}

func TestWithDisableKeepAlives(t *testing.T) {
	transport := must(NewBaseTransport(WithDisableKeepAlives()))
	if !transport.DisableKeepAlives {
		t.Error("expected DisableKeepAlives to be true")
	}
}

func TestWithMaxConnsPerHost(t *testing.T) {
	transport := must(NewBaseTransport(WithMaxConnsPerHost(5)))
	if transport.MaxConnsPerHost != 5 {
		t.Errorf("expected MaxConnsPerHost 5, got %d", transport.MaxConnsPerHost)
	}
}

func TestWithResponseHeaderTimeout(t *testing.T) {
	transport := must(NewBaseTransport(WithResponseHeaderTimeout(30 * time.Second)))
	if transport.ResponseHeaderTimeout != 30*time.Second {
		t.Errorf("expected ResponseHeaderTimeout 30s, got %v", transport.ResponseHeaderTimeout)
	}
}

func TestWithWriteBufferSize(t *testing.T) {
	transport := must(NewBaseTransport(WithWriteBufferSize(8192)))
	if transport.WriteBufferSize != 8192 {
		t.Errorf("expected WriteBufferSize 8192, got %d", transport.WriteBufferSize)
	}
}

func TestWithReadBufferSize(t *testing.T) {
	transport := must(NewBaseTransport(WithReadBufferSize(8192)))
	if transport.ReadBufferSize != 8192 {
		t.Errorf("expected ReadBufferSize 8192, got %d", transport.ReadBufferSize)
	}
}

func TestMultipleBaseOptions(t *testing.T) {
	transport := must(NewBaseTransport(
		WithMaxIdleConns(200),
		WithMaxIdleConnsPerHost(50),
		WithIdleConnTimeout(120*time.Second),
		WithDisableHTTP2(),
	))

	if transport.MaxIdleConns != 200 {
		t.Errorf("expected MaxIdleConns 200, got %d", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost != 50 {
		t.Errorf("expected MaxIdleConnsPerHost 50, got %d", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout != 120*time.Second {
		t.Errorf("expected IdleConnTimeout 120s, got %v", transport.IdleConnTimeout)
	}
	if transport.ForceAttemptHTTP2 {
		t.Error("expected ForceAttemptHTTP2 to be false")
	}
}
