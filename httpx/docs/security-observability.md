# Credentials, logging and operations

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md)

## Origin-bound credentials

`WithAuth(originURL, provider)` binds a provider to scheme, host and port;
the path is ignored. Repeat it to bind more origins. Redirects to another
origin or from HTTPS to HTTP do not receive that provider's credentials.
An overridden HTTP `Host` must also match the binding; a gateway socket URL
does not authorize credentials for every virtual host it can route to.

Providers support bearer tokens, refreshable bearer tokens, basic auth,
API-key headers and your own `AuthProvider`. The refreshable provider exposes
`SetToken`; the application still owns obtaining and refreshing the token.
Prefer these providers for credentials instead of a custom decorator that
re-adds secrets on every redirect.

Bindings govern credentials the provider adds. Headers the caller already
put on a request, cookies managed by `http.Client`, and a custom transport's
identity remain separate concerns. Use TLS for credentials. For untrusted
destination URLs, apply an egress/SSRF policy in the dialer/transport and
control redirects; httpx does not provide a destination allowlist.

## Safe operational logging

`WithLogging` hands a `LoggingHook` detached, bodiless snapshots. Default
redaction covers authorization, cookies, common API-key/token headers,
credential query parameters, URL userinfo and fragments. URL-valued headers
and URLs embedded in error text are sanitized too. Request redirect objects,
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

`WithMetrics` forwards request/response events plus retries and cache
hits/misses. `CountingHook` tracks counts atomically; `LatencyHook` keeps
running aggregates and a bounded recent-sample window; `MultiHook` fans out.
Implement `RetryCauseHook` for `status`, `network`, `timeout` and `unknown`
retry categories. Export these to your preferred metrics system.

Round-trip latency ends when headers arrive, not when the body is fully
consumed. Streaming duration and application decode time require their own
measurements. `CountingHook.Errors` counts Go errors, so HTTP failure status
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
