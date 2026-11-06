//go:build unit

package httpx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"testing"
)

func TestEgressAddressClasses(t *testing.T) {
	p := NewEgressPolicy()
	for _, raw := range []string{"0.0.0.0", "0.10.20.30", "127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "100.127.255.255", "224.0.0.1", "255.255.255.255", "::", "::1", "fc00::1", "fe80::1%eth0", "fec0::1", "ff02::1"} {
		addr := netip.MustParseAddr(raw)
		if p.Permits(addr) {
			t.Errorf("permitted %s", addr)
		}
		if addr.Is4() && p.Permits(netip.AddrFrom16(addr.As16())) {
			t.Errorf("permitted mapped %s", addr)
		}
	}
	for _, raw := range []string{"8.8.8.8", "100.128.0.1", "172.32.0.1", "2001:4860:4860::8888"} {
		if !p.Permits(netip.MustParseAddr(raw)) {
			t.Errorf("refused public %s", raw)
		}
	}
	trusted := NewEgressPolicy(netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("::ffff:192.168.1.0/120"))
	if !trusted.Permits(netip.MustParseAddr("::ffff:10.1.2.3")) || !trusted.Permits(netip.MustParseAddr("192.168.1.1")) || trusted.Permits(netip.MustParseAddr("192.168.2.1")) {
		t.Fatal("trusted prefix normalization")
	}
	if p.Permits(netip.Addr{}) {
		t.Fatal("invalid address allowed")
	}
	for _, raw := range []string{"https://127.0.0.1/private?secret=token", "https://[::ffff:10.0.0.1]", "file:///private", "https://[invalid]", "https://user:secret@example.test"} {
		if !errors.Is(p.CheckURL(raw), ErrDestinationRefused) {
			t.Errorf("CheckURL accepted %q", raw)
		}
	}
	if err := p.CheckURL("https://public.example/items"); err != nil {
		t.Fatal(err)
	}
}

func TestEgressPolicyComposesWithDialer(t *testing.T) {
	for _, policyFirst := range []bool{false, true} {
		calls := 0
		controlled := errors.New("allowed address reached caller control")
		d := &net.Dialer{ControlContext: func(context.Context, string, string, syscall.RawConn) error { calls++; return controlled }}
		opts := []BaseOption{WithDialer(d), WithEgressPolicy(NewEgressPolicy())}
		if policyFirst {
			opts[0], opts[1] = opts[1], opts[0]
		}
		base, err := NewBaseTransport(opts...)
		if err != nil {
			t.Fatal(err)
		}
		if base.Proxy != nil {
			t.Fatal("proxy enabled")
		}
		_, err = base.DialContext(t.Context(), "tcp", "127.0.0.1:1")
		if !errors.Is(err, ErrDestinationRefused) || calls != 0 {
			t.Fatalf("refusal=%v calls=%d", err, calls)
		}
		_, err = base.DialContext(t.Context(), "tcp", "8.8.8.8:443")
		if !errors.Is(err, controlled) || calls != 1 {
			t.Fatalf("caller control lost: %v calls=%d", err, calls)
		}
		if d.ControlContext == nil {
			t.Fatal("mutated caller dialer")
		}
	}
	if _, err := NewBaseTransport(WithEgressPolicy(nil)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, err := NewClient(WithTransport(http.DefaultTransport), WithBaseTransport(WithEgressPolicy(NewEgressPolicy()))); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("ignored egress: %v", err)
	}
}

func TestEgressRefusalNeverRetriesOrTripsCircuit(t *testing.T) {
	calls := 0
	base := stubRoundTripper(func(*http.Request) (*http.Response, error) { calls++; return nil, ErrDestinationRefused })
	breaker := must(NewCircuitTransport(base, WithFailureThreshold(1), WithIsFailure(func(*http.Response, error) bool { return true })))
	retry := must(NewRetryTransport(breaker, WithRetryIf(func(*http.Response, error) bool { return true })))
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.example/items", nil)
	_, err := retry.RoundTrip(req)
	if !errors.Is(err, ErrDestinationRefused) || calls != 1 || breaker.State(OriginOf(req.URL)) != StateClosed {
		t.Fatalf("error=%v calls=%d state=%v", err, calls, breaker.State(OriginOf(req.URL)))
	}
}

func TestEgressRejectsAlternateDialPaths(t *testing.T) {
	for _, set := range []func(*http.Transport){
		func(tr *http.Transport) {
			tr.DialContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
		},
		func(tr *http.Transport) {
			tr.DialTLSContext = func(context.Context, string, string) (net.Conn, error) { return nil, nil }
		},
		func(tr *http.Transport) {
			//lint:ignore SA1019 Verify that the legacy TLS dial hook cannot bypass egress policy.
			tr.DialTLS = func(string, string) (net.Conn, error) { return nil, nil }
		},
	} {
		if _, err := NewBaseTransport(WithTransportSettings(set), WithEgressPolicy(NewEgressPolicy())); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("custom dial bypass accepted: %v", err)
		}
	}
	called := false
	base, err := NewBaseTransport(WithTransportSettings(func(tr *http.Transport) {
		tr.DialContext = func(context.Context, string, string) (net.Conn, error) { called = true; return nil, context.Canceled }
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = base.DialContext(t.Context(), "tcp", "example.test:80")
	if !called || !errors.Is(err, context.Canceled) {
		t.Fatal(called, err)
	}
}
