//go:build unit

package httpx

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDeferredTokenAcquisition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		started, release := make(chan struct{}), make(chan struct{})
		source := must(NewTokenSource("", time.Time{}, func(ctx context.Context) (string, time.Time, error) {
			if calls.Add(1) == 1 {
				close(started)
				select {
				case <-release:
				case <-ctx.Done():
					return "", time.Time{}, ctx.Err()
				}
			}
			return "new", time.Now().Add(time.Minute), nil
		}, WithDeferredAcquisition(), WithRefreshMargin(0)))
		t.Cleanup(func() {
			if err := source.Close(); err != nil {
				t.Error(err)
			}
		})
		for range 3 {
			if token, expiry, err := source.Token(t.Context()); token != "" || !expiry.IsZero() || err != nil || calls.Load() != 0 {
				t.Fatalf("initial token=%q expiry=%v err=%v calls=%d", token, expiry, err, calls.Load())
			}
		}
		refreshed := make(chan error, 1)
		go func() { _, _, err := source.Refresh(t.Context(), ""); refreshed <- err }()
		<-started
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		waiter := make(chan error, 1)
		go func() { _, _, err := source.Token(ctx); waiter <- err }()
		synctest.Wait()
		select {
		case err := <-waiter:
			t.Fatalf("Token bypassed active refresh: %v", err)
		default:
		}
		cancel()
		if err := <-waiter; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		if err := <-refreshed; err != nil {
			t.Fatal(err)
		}
		if token, _, err := source.Refresh(t.Context(), ""); token != "new" || err != nil || calls.Load() != 1 {
			t.Fatal(token, err, calls.Load())
		}
		time.Sleep(time.Minute)
		if token, _, err := source.Token(t.Context()); token != "new" || err != nil || calls.Load() != 2 {
			t.Fatal(token, err, calls.Load())
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if _, _, err := source.Token(t.Context()); !errors.Is(err, ErrTokenSourceClosed) {
			t.Fatal(err)
		}
	})
}

func TestInitialTokenAuthBoundaries(t *testing.T) {
	for _, mode := range []string{"anonymous", "key", "query", "authority", "foreign", "initial-error", "source-error", "empty-refresh"} {
		t.Run(mode, func(t *testing.T) {
			cause := errors.New("private credential")
			var calls, initial, refreshes int
			source := must(NewTokenSource("", time.Time{}, func(context.Context) (string, time.Time, error) {
				refreshes++
				if mode == "source-error" {
					return "", time.Time{}, cause
				}
				if mode == "empty-refresh" {
					return "", time.Time{}, nil
				}
				return "new", time.Time{}, nil
			}, WithDeferredAcquisition()))
			t.Cleanup(func() {
				if err := source.Close(); err != nil {
					t.Error(err)
				}
			})
			if mode == "source-error" {
				if err := source.Close(); err != nil {
					t.Fatal(err)
				}
			}
			provider := FuncAuthProvider(func(r *http.Request) error {
				initial++
				if mode == "initial-error" {
					return cause
				}
				if mode == "query" {
					return (&QueryParamAuthProvider{Key: "key", Value: "secret"}).AddAuth(r)
				}
				r.Header.Set("X-API-Key", "secret")
				return nil
			})
			binding := TokenBinding{URL: "https://api.example", Source: source}
			if mode != "anonymous" {
				binding.InitialAuth = provider
			}
			base := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if r.Header.Get("Authorization") != "" {
						t.Error("empty bearer overwrote initial authentication")
					}
					if mode == "key" && r.Header.Get("X-API-Key") != "secret" {
						t.Error("initial key missing")
					}
					if mode == "query" && r.URL.Query().Get("key") != "secret" {
						t.Error("initial query missing")
					}
					if mode == "authority" || mode == "foreign" {
						if initial != 0 || r.Header.Get("X-API-Key") != "" {
							t.Error("foreign authority received initial authentication")
						}
					}
					return reply(http.StatusUnauthorized, "rejected"), nil
				}
				if r.Header.Get("Authorization") != "Bearer new" || r.Header.Get("X-API-Key") != "" || r.URL.Query().Has("key") {
					t.Error("bearer replay retained initial provider credentials")
				}
				return reply(http.StatusNoContent, ""), nil
			})
			transport := must(NewTokenTransport(base, binding))
			req := must(http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.example/resource", nil))
			if mode == "authority" {
				req.Host = "other.example"
			}
			if mode == "foreign" {
				req.URL.Host = "other.example"
			}
			resp, err := transport.RoundTrip(req)
			if resp != nil {
				if err := resp.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			wantCalls, wantRefresh := 2, 1
			switch mode {
			case "initial-error", "source-error":
				wantCalls, wantRefresh = 0, 0
			case "empty-refresh":
				wantCalls = 1
			case "authority", "foreign":
				wantCalls, wantRefresh = 1, 0
			}
			if calls != wantCalls || refreshes != wantRefresh {
				t.Fatalf("calls=%d refreshes=%d, want %d/%d", calls, refreshes, wantCalls, wantRefresh)
			}
			if mode == "initial-error" || mode == "source-error" || mode == "empty-refresh" {
				if !errors.Is(err, ErrAuthRefresh) || strings.Contains(err.Error(), "private credential") {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if req.Header.Get("X-API-Key") != "" || req.Header.Get("Authorization") != "" || req.URL.Query().Has("key") {
				t.Error("mutated original request")
			}
		})
	}
}
