# Outbound destination policy

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md#base-transport)

Enable `WithEgressPolicy` when a client accepts untrusted destination URLs.
It refuses internal addresses immediately before socket connection, after DNS
resolution, including connections initiated by redirects. It is opt-in; a
standard base transport retains ordinary Go networking behavior.

```go
policy := httpx.NewEgressPolicy()
base, err := httpx.NewBaseTransport(httpx.WithEgressPolicy(policy))
if err != nil {
    return err
}
client := &http.Client{Transport: base, Timeout: 10 * time.Second}
defer client.CloseIdleConnections()
```

`NewEgressPolicy(allow ...netip.Prefix)` creates an immutable policy. It rejects
invalid, unspecified, loopback, private, link-local, multicast, CGNAT
(`100.64.0.0/10`), `0.0.0.0/8`, limited broadcast and IPv6 site-local addresses.
IPv4-mapped IPv6 is checked as IPv4. Explicit trusted prefixes may allow those
addresses; treat the prefixes as operator configuration, never request input.
Invalid prefixes grant no access.

`Permits(netip.Addr)` checks an address. `CheckURL(string)` validates an absolute
HTTP(S) URL without userinfo and checks literal IPs; it does not resolve a
hostname. Use it for early input feedback while retaining the guarded transport
for DNS and redirects. Error text contains no URL path, userinfo or query.

## Dialers and proxies

`WithDialer` preserves a custom `net.Resolver`, dial timeout, local address and
control hook. The constructor copies the dialer and installs policy checks in
`ControlContext`; every candidate address is checked immediately before connect.
An answer set containing refused and allowed addresses can still connect to an
allowed address. No separate pre-resolution result can become stale before dial.
Existing `ControlContext` or `Control` runs only after the policy permits the IP.

The policy is applied after all base options, so option order cannot remove it.
`WithTransportSettings` exposes other standard transport fields, but guarded
bases reject custom `DialContext`, `DialTLSContext` and `DialTLS` functions:
those alternate paths would bypass the check. Configure dial behavior through
`WithDialer`. Do not mutate a constructed transport's dial/proxy fields.

All proxies are disabled with this policy, including environment proxies.
Checking a proxy socket does not verify the proxy's destination. A custom proxy
or tunnel needs its own destination enforcement. A `NewClient` configuration
combining `WithTransport` and a base egress policy returns `ErrInvalidConfig`;
construct the guarded base explicitly and inject that transport instead.

## Failures and limits

Match `ErrDestinationRefused` with `errors.Is`. Retry and circuit transports
exclude it before custom classifiers: refusal is local policy, not origin
health. A cache may still serve a permitted stored response without connecting.
Observe the request's error or logging layer; this policy emits no global logs.

This is an address policy, not a hostname/port allowlist, application authorization
or protection from a public gateway that itself accesses internal resources.
Keep TLS verification, credential origin binding and the application's redirect
policy. Trusted CIDRs deliberately widen network access. Reuse one policy across
clients with the same trust boundary and build separate clients for different
network permissions.
