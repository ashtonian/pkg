# Automatic bearer authentication

[Quick start](../README.md) · [Guides](README.md) · [Options and API](reference.md#automatic-bearer-refresh)

`NewTokenSource` supplies a bearer token and serializes refresh callbacks.
`NewTokenTransport` binds sources to origins and replays an explicitly rejected
request once. `WithTokenAuth` and `WithTokenAuthT` add the same transport through
the client and chain builders. Static/basic/API-key providers and the manually
invoked `BearerTokenProvider.Refresh` remain independent APIs.

## Construct and own a source

```go
source, err := httpx.NewTokenSource(initialToken, expiresAt,
    func(ctx context.Context) (string, time.Time, error) {
        return credentials.Refresh(ctx)
    },
    httpx.WithRefreshMargin(30*time.Second),
    httpx.WithRefreshTimeout(10*time.Second),
)
if err != nil {
    return err
}
defer source.Close()
client, err := httpx.NewClient(httpx.WithTokenAuth(originURL, source))
if err != nil {
    return err
}
defer client.CloseIdleConnections()
```

Here `credentials.Refresh` is application-owned acquisition/persistence and
returns the access token, expiry and error. It must propagate `ctx` into I/O.
Use a separate bounded `httpx` client for its HTTP calls; a grant client that
uses the same source would recurse. There is no mandatory storage format,
identity service or OAuth dependency in the core module.

An empty initial token acquires on first use. Zero expiry means unknown and
refreshes only on rejection. Otherwise `Token` refreshes before expiry by
30 seconds, capped at 10% of remaining lifetime when the token is installed.
Configure zero margin to refresh at expiry. A rejected token is refreshed even
when its expiry is still in the future. If another call already installed a
newer usable token, `Refresh(ctx, stale)` returns it without another callback.

Share a source across clients that represent the same credential. The owner
stops callers, closes each source once, and then closes client/cache resources.
Transports borrow sources; `CloseIdleConnections` does not close them.
`Close` cancels a running refresh and waits for its cooperative callback.
Calls after close return `ErrTokenSourceClosed`.

## Concurrent refresh and cancellation

One callback runs at a time. Concurrent expiry checks and 401 responses join
that work. Each caller waits with its own context, so a canceled waiter returns
without blocking on the refresh callback or canceling other live waiters.
Shared work retains the first waiter's context values, has its own configured
timeout, and is canceled when every waiter leaves or the source closes. A later
caller waits for an abandoned callback to finish before starting another.
The callback and `WithOnRefresh` observer must not call back into their source.

Callback failures wrap `ErrAuthRefresh` and preserve causes for `errors.Is` /
`errors.As`; their text omits callback messages that might contain credentials.
`WithOnRefresh` observes one result per callback before waiters resume. Keep it
quick and use bounded error classifications. Do not log unwrapped causes
without redaction.

A callback may return a nonempty new token with an error when rotation succeeded
but persistence failed. The source retains that token while returning the error
to current waiters. Subsequent calls can use the installed token. Persistence
recovery remains application-owned; do not fall back to an invalidated old
refresh credential.

## Origin, replay and cache boundaries

A binding requires both URL origin and effective `Request.Host` to match its
scheme, host and port. A foreign redirect, HTTPS downgrade or authority override
does not receive source credentials and cannot trigger its refresh. The transport
clones headers; it does not modify the caller's credential header map. Existing
caller-supplied headers remain the caller's responsibility.

Only a 401 triggers reactive refresh. A request with no body or `http.NoBody`
is replayable; any other body needs `GetBody`. `http.NewRequest` installs it for
byte buffers/readers and string readers; `EncodeRequest` installs it too.
The auth layer never buffers an unknown stream. A non-replayable request returns
its 401 body without refreshing. Before a replay, the discarded 401 body is
closed without draining, then a fresh body is obtained. A second 401 is returned
unchanged; there is no authentication loop.

An explicit 401 permits one authentication replay for any method, including
POST/PATCH without an idempotency key. This is distinct from retrying an
ambiguous network failure; use it only with APIs whose 401 rejects the operation.
Retry and hedging retain their own replay contracts.

Place token auth outside cache in a custom chain so the current bearer token
partitions the cache before lookup. A replay after rotation gets the new token's
partition. `NewClient` chooses that order. Background cache revalidation runs
inside token auth with the credential on the cached request; a background 401
does not recursively refresh the source. Subsequent foreground calls obtain the
current token before lookup. Configure all other [identity dimensions](caching.md#identity-and-authority).

## OAuth2 grants

The optional [httpx/oauth2 module](../oauth2/README.md) adapts
`golang.org/x/oauth2` to these sources. `Source` forces a refresh grant after
401 even before expiry, retains rotated refresh credentials and invokes the
application's persistence callback before replay. `Reacquire` creates a fresh
context-bound grant for client credentials and other grants without refresh
tokens. Both return the core source and use its timeout, cancellation and
shutdown contract. Sign-in and credential storage stay application-owned.

## Multiple processes

Source coordination is in-process. When an issuer invalidates refresh tokens
on use, independent processes must not rotate the same credential concurrently.
Inside the refresh callback, acquire the application's context-aware distributed
lock or row lock, reload the latest persisted credential, refresh it if required,
and atomically persist its successor before releasing ownership. A lease needs
fencing and bounded renewal; a process mutex alone does not protect another
replica. Keep the callback's deadline through lock admission and issuer I/O.
