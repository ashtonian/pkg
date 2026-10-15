# Credentials, logging and operations

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Origin-bound credentials

`NewAuthTransport(base, httpx.AuthBinding{URL: originURL, Provider: provider})`
binds a provider to scheme, host and port; the path is ignored. Pass more
bindings for additional origins. The client-builder equivalent is
`WithAuth(originURL, provider)`. Redirects to another
origin or from HTTPS to HTTP do not receive that provider's credentials.
An overridden HTTP `Host` must also match the binding; a gateway socket URL
does not authorize credentials for every virtual host it can route to.

Providers support bearer tokens, refreshable bearer tokens, basic auth,
API-key headers and your own `AuthProvider`. The refreshable provider exposes
`Refresh()` and `SetToken()`. `Refresh()` calls the supplied callback; the
application decides when to invoke it. Requests and 401 responses do not
trigger token refresh or reauthentication automatically.
Prefer these providers for credentials instead of a custom decorator that
re-adds secrets on every redirect.

Bindings govern credentials the provider adds. Headers the caller already
put on a request, cookies managed by `http.Client`, and a custom transport's
identity remain separate concerns. Use TLS for credentials. For untrusted
destination URLs, apply an egress/SSRF policy in the dialer/transport and
control redirects; httpx does not provide a destination allowlist.

When combining auth with caching, follow the [cache partitioning
rules](caching.md#identity-and-authority). Only `Authorization` and `Cookie`
are included automatically; API-key and other identity headers need explicit
configuration. These transports do not establish application authorization
or tenant boundaries.

## Logging and redaction

`NewLoggingTransport` hands a `LoggingHook` detached, bodiless snapshots.
The client-builder equivalent is `WithLogging`. Default
redaction covers authorization, cookies, common API-key/token headers,
credential query parameters, URL userinfo and fragments. `Referer`, `Location`
and `Content-Location` receive URL redaction too, as do request URLs,
`url.Error` URLs and HTTP(S) URLs found in error text. Request redirect objects,
response bodies and TLS state are excluded. The caller receives the original
transport error for programmatic inspection. Hooks receive a detached sanitized
error with no original cause available through `errors.Unwrap` or `errors.As`.

Extend the defaults with `WithSensitiveHeaders` and
`WithSensitiveQueryParams` for application-specific secret names. A secret
embedded in an arbitrary path, header, context value or free-form error text
cannot be inferred reliably: avoid putting credentials there. Do not treat
arbitrary application data as safe to log merely because URL credentials
are redacted.

Metrics, retry and cache hooks receive live requests. Use them to count
bounded categories; do not dump their headers, URLs or errors to logs. Use
the dedicated logging hook for redacted request/response records.

## Metrics

`NewMetricsTransport` reports round-trip events. In an explicit chain, pass
the hook to `WithRetryHook` and `WithCacheHook` to receive those layers'
events too. The client builder's `WithMetrics` wires all three automatically.
`CountingHook` tracks counts atomically; `LatencyHook` keeps
running aggregates and a bounded recent-sample window; `MultiHook` fans out.
Implement `RetryCauseHook` for `status`, `network`, `timeout` and `unknown`
retry categories. Export these to your preferred metrics system.

Latency measures the wrapped `RoundTrip` call. With the builder's outer
metrics layer, it includes retry/admission waits and any body buffering by
the cache. A plain network transport usually returns after headers arrive;
subsequent caller body reads and application decoding are outside the
measurement. `CountingHook.Errors` counts Go errors, so HTTP failure status
counts need explicit classification. Keep labels bounded; avoid raw URLs,
credential values, user IDs and arbitrary origin names.

## Troubleshooting

| Symptom | Check |
|---|---|
| `ErrCircuitOpen` | Origin state, recent failure classifications and transition callbacks |
| `ErrRateLimited` | Attempt rate including retries/hedges, burst and `WithWait` |
| `ErrLimiterTimeout` | Whether callers close response bodies; slot acquisition deadline |
| POST/PATCH did not retry | Server-supported idempotency key, safe replay policy and body availability |
| Unexpected cached value | Effective authority, all identity dimensions, `Vary`, freshness and `Cache-Status` |
| `ErrResponseTooLarge` | Decoded response size; use a deliberate larger bound or stream it |
| `ErrNoCodec` | Response content type and installed optional codec |
| Shutdown waits | Open bodies and cache background refreshes; cancel work and close the cache before idle connections |

See [errors](reference.md#errors), [auth options](reference.md#auth), and
[observability options](reference.md#metrics-and-logging).
