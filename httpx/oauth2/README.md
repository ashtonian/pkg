# httpx/oauth2

Adapt `golang.org/x/oauth2` grants to [httpx bearer authentication](../docs/authentication.md). `Source` refreshes bearer grants even after a 401 before expiry, retains rotated credentials and persists them before replay. `Reacquire` builds a fresh context-bound grant for client credentials and other grants without refresh tokens.

## Quick Start

The [executable example](example_integration_test.go) uses a local issuer and API: an unexpired token receives 401, the source refreshes it, and the request succeeds.

```go
//go:build integration

package oauth2_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/ashtonian/pkg/httpx"
	httpxoauth2 "github.com/ashtonian/pkg/httpx/oauth2"
	xoauth2 "golang.org/x/oauth2"
)

func ExampleSource() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			w.Header().Set("Content-Type", "application/json")
			// A write failure ends this local fixture response.
			_, _ = w.Write([]byte(`{"access_token":"new","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	grantClient, err := httpx.NewClient(httpx.WithTimeout(5 * time.Second))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer grantClient.CloseIdleConnections()
	cfg := &xoauth2.Config{Endpoint: xoauth2.Endpoint{TokenURL: server.URL + "/token", AuthStyle: xoauth2.AuthStyleInParams}}
	token := &xoauth2.Token{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}
	source, err := httpxoauth2.Source(cfg, token, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer source.Close()
	client, err := httpx.NewClient(httpx.WithTokenAuth(server.URL, source))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	ctx := context.WithValue(context.Background(), xoauth2.HTTPClient, grantClient)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := resp.Body.Close(); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(resp.StatusCode)
	// Output: 200
}
```

## Configuration

| Input | Type | Default | Description |
|---|---|---|---|
| `cfg` (`Source`) | `*oauth2.Config` | required | Issuer endpoint and client credentials; public fields are copied at construction |
| `tok` (`Source`) | `*oauth2.Token` | required | Initial bearer grant with a refresh token; the source owns a copy |
| `onRotate` (`Source`) | `func(context.Context, *oauth2.Token) error` | nil | Persist each newly acquired grant before it is exposed to waiting requests |
| `newSource` (`Reacquire`) | `func(context.Context) oauth2.TokenSource` | required | Construct a fresh grant using the current refresh context |
| `opts` | `...httpx.TokenOption` | core defaults | [Refresh margin, timeout, clock and observer](../docs/reference.md#automatic-bearer-refresh) |
| Context `oauth2.HTTPClient` | `*http.Client` | x/oauth2 default client | Supply a separate bounded `httpx` client for token endpoint calls; never one that uses this source |

The shared refresh context is always bounded by `WithRefreshTimeout`, including when x/oauth2's default client is selected. Application client settings still own TLS, network policy and transport pools. Context values from the first waiter reach the grant client and persistence callback.

## API and operations

- `Source(cfg, tok, onRotate, opts...) (*httpx.RefreshingTokenSource, error)` forces the refresh grant using the newest refresh token. Empty token types mean bearer; other explicit types are rejected.
- `Reacquire(newSource, opts...) (*httpx.RefreshingTokenSource, error)` acquires on first use and reconstructs the grant on expiry or rejection. For client credentials, use `func(ctx context.Context) oauth2.TokenSource { return cfg.TokenSource(ctx) }`; do not return a cached source that retains an earlier context.
- `Close()` cancels refresh and waits for cooperative completion. Sources are borrowed by transports; their owner closes them once.

`onRotate` receives a snapshot and runs before replay. If persistence fails, current waiters receive `ErrAuthRefresh` while the new access/refresh credentials remain in memory. Subsequent calls can use the installed access token; the application must repair persistence before restart or another process uses the old grant. Never silently restore the invalidated refresh token. Observe `httpx.WithOnRefresh` for failure counts and use bounded classifications; do not log underlying issuer/persistence errors or token objects.

Coordination is in-process. Use an application-owned distributed lock/transaction plus reload and fenced persistence when replicas share rotating credentials. The [core guide](../docs/authentication.md#multiple-processes) describes that callback pattern. The convenience `Source` owns in-memory refresh state; a distributed reload/compare-and-swap protocol belongs in a custom core refresh callback, not outside `Source` around individual requests.

## Security

Bind the source to each intended origin with `WithTokenAuth` or `TokenBinding`. URL origin and effective Host must match. Keep token endpoint TLS verification enabled, store refresh credentials securely, and pass context through persistence. Sign-in, authorization redirects, device authorization, consent and credential storage remain in x/oauth2 and the application.

## Build and test

From the module directory, test with a published parent that supplies the token API, or use the repository workspace during development:

```sh
go build ./...
go test -tags=unit -race ./...
go test -tags=integration -race ./...
```

The module requires Go 1.26 or later. Each optional module is released independently; the parent API must be published before this module's parent requirement is advanced. [Archive checks](../docs/development.md#release-archives) exercise fresh consumers without workspace replacements.

## Code map

| File | Responsibility |
|---|---|
| `oauth2.go` | `Source`, `Reacquire`, bearer validation and rotation persistence |
| `example_integration_test.go` | Complete token endpoint and 401 replay example |

## Dependencies

- `github.com/ashtonian/pkg/httpx` owns token concurrency, transport replay and context lifetime.
- `golang.org/x/oauth2` owns OAuth grants and issuer response handling; v0.37.0 requires Go 1.26.
- No service, storage backend, telemetry registry or sign-in framework is installed by this adapter.

## References

- [Core authentication](../docs/authentication.md)
- [x/oauth2 source and API](https://pkg.go.dev/golang.org/x/oauth2)
