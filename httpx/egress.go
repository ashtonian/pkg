package httpx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"syscall"
)

// ErrDestinationRefused identifies a local egress-policy refusal. It is never
// retried or counted as a circuit failure, even with custom classifiers.
var ErrDestinationRefused = errors.New("httpx: destination refused by egress policy")

// EgressPolicy rejects internal destinations except explicitly trusted
// networks. Construct it once and share it; policies are immutable.
type EgressPolicy struct {
	allow []netip.Prefix
}

// NewEgressPolicy trusts allow in addition to public addresses. Trust entries
// belong to operator configuration, never to untrusted request input. Invalid
// prefixes grant nothing. IPv4-mapped IPv6 prefixes are normalized to IPv4.
func NewEgressPolicy(allow ...netip.Prefix) *EgressPolicy {
	p := &EgressPolicy{allow: slices.Clone(allow)}
	for i, prefix := range p.allow {
		if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
			p.allow[i] = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96).Masked()
		}
	}
	return p
}

// Permits rejects invalid, unspecified, loopback, private, link-local,
// multicast, CGNAT, 0.0.0.0/8, limited broadcast and IPv6 site-local addresses,
// unless a trusted prefix includes the address. IPv4-mapped IPv6 is unwrapped.
func (p *EgressPolicy) Permits(addr netip.Addr) bool {
	if p == nil || !addr.IsValid() {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, prefix := range p.allow {
		if prefix.Contains(addr) {
			return true
		}
	}
	return addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("0.0.0.0/8").Contains(addr) &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(addr) &&
		!netip.MustParsePrefix("fec0::/10").Contains(addr)
}

// CheckURL validates an absolute HTTP(S) URL and refuses blocked literal IPs.
// Hostnames are resolved and checked at dial time by WithEgressPolicy.
// Errors contain no URL path, credentials or query.
func (p *EgressPolicy) CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || p == nil {
		return fmt.Errorf("%w: invalid HTTP destination", ErrDestinationRefused)
	}
	addr, parseErr := netip.ParseAddr(u.Hostname())
	if parseErr == nil && !p.Permits(addr) {
		return fmt.Errorf("%w: %s", ErrDestinationRefused, addr)
	}
	if parseErr != nil && strings.HasPrefix(u.Host, "[") {
		return fmt.Errorf("%w: invalid IP literal", ErrDestinationRefused)
	}
	return nil
}

// WithEgressPolicy checks resolved addresses immediately before connecting and
// disables proxies. WithDialer retains its resolver, limits and control hook;
// option order cannot replace the policy. The returned transport must not have
// its dial or proxy fields changed after construction.
func WithEgressPolicy(p *EgressPolicy) BaseOption {
	return func(cfg *baseConfig) {
		cfg.egress, cfg.egressSet = p, true
	}
}

func (p *EgressPolicy) guard(d *net.Dialer) {
	control, controlContext := d.Control, d.ControlContext
	d.Control = nil
	d.ControlContext = func(ctx context.Context, network, address string, conn syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		addr, parseErr := netip.ParseAddr(host)
		if err != nil || parseErr != nil || !p.Permits(addr) {
			return fmt.Errorf("%w: %s", ErrDestinationRefused, address)
		}
		if controlContext != nil {
			return controlContext(ctx, network, address, conn)
		}
		if control != nil {
			return control(network, address, conn)
		}
		return nil
	}
}
