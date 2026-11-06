//go:build unit

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestTokenConcurrent401RefreshAndReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var rejected, refreshed atomic.Int32
		allRejected := make(chan struct{})
		source := must(NewTokenSource("old", time.Time{}, func(context.Context) (string, time.Time, error) { refreshed.Add(1); return "new", time.Time{}, nil }))
		defer source.Close()
		base := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(r.Body)
			r.Body.Close()
			if err != nil || string(body) != "payload" {
				t.Errorf("body=%q err=%v", body, err)
			}
			if r.Header.Get("Authorization") == "Bearer old" {
				if rejected.Add(1) == 100 {
					close(allRejected)
				}
				<-allRejected
				return reply(401, "rejected"), nil
			}
			if r.Header.Get("Authorization") != "Bearer new" {
				t.Error("incorrect replay credential")
			}
			return reply(200, "accepted"), nil
		})
		client := must(NewClient(WithTransport(base), WithTokenAuth("https://api.example", source)))
		var wg sync.WaitGroup
		for range 100 {
			wg.Go(func() {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.example/items", strings.NewReader("payload"))
				resp, err := client.Do(req)
				if err != nil {
					t.Error(err)
					return
				}
				defer resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Errorf("status=%d", resp.StatusCode)
				}
			})
		}
		wg.Wait()
		if rejected.Load() != 100 || refreshed.Load() != 1 {
			t.Fatalf("401s=%d refreshes=%d", rejected.Load(), refreshed.Load())
		}
	})
}

func TestTokenSharedFlightCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started, release := make(chan struct{}), make(chan struct{})
		type key struct{}
		source := must(NewTokenSource("old", time.Time{}, func(ctx context.Context) (string, time.Time, error) {
			if ctx.Value(key{}) != "trace" {
				t.Error("lost caller context values")
			}
			close(started)
			select {
			case <-ctx.Done():
				return "", time.Time{}, ctx.Err()
			case <-release:
				return "new", time.Time{}, nil
			}
		}))
		defer source.Close()
		ctx, cancel := context.WithCancel(context.WithValue(t.Context(), key{}, "trace"))
		first := make(chan error, 1)
		second := make(chan error, 1)
		go func() { _, _, err := source.Refresh(ctx, "old"); first <- err }()
		<-started
		go func() {
			token, _, err := source.Refresh(t.Context(), "old")
			if token != "new" && err == nil {
				t.Error("second token")
			}
			second <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		close(release)
		if err := <-second; err != nil {
			t.Fatal(err)
		}
	})
}

func TestTokenRefreshLifecycleAndMargin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		source := must(NewTokenSource("old", time.Now().Add(100*time.Second), func(context.Context) (string, time.Time, error) {
			calls++
			return "new", time.Now().Add(time.Hour), nil
		}))
		defer source.Close()
		time.Sleep(89 * time.Second)
		if token, _, err := source.Token(t.Context()); token != "old" || err != nil {
			t.Fatal(token, err)
		}
		time.Sleep(time.Second)
		if token, _, err := source.Token(t.Context()); token != "new" || err != nil || calls != 1 {
			t.Fatal(token, err, calls)
		}
	})
	for _, closeSource := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			started, stopped := make(chan struct{}), make(chan struct{})
			source := must(NewTokenSource("", time.Time{}, func(ctx context.Context) (string, time.Time, error) {
				close(started)
				<-ctx.Done()
				close(stopped)
				return "", time.Time{}, ctx.Err()
			}))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, _, err := source.Token(ctx); done <- err }()
			<-started
			if closeSource {
				source.Close()
			} else {
				cancel()
			}
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			<-stopped
			source.Close()
		})
	}
}

func TestTokenAuthorityReplayAndErrorBoundaries(t *testing.T) {
	for _, mode := range []string{"authority", "origin", "stream", "second-401", "refresh-error"} {
		t.Run(mode, func(t *testing.T) {
			calls, refreshes := 0, 0
			cause := errors.New("private token endpoint and secret")
			source := must(NewTokenSource("old", time.Time{}, func(context.Context) (string, time.Time, error) {
				refreshes++
				if mode == "refresh-error" {
					return "", time.Time{}, cause
				}
				return "new", time.Time{}, nil
			}))
			defer source.Close()
			base := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if (mode == "authority" || mode == "origin") && r.Header.Get("Authorization") != "" {
					t.Error("credential leaked")
				}
				if r.Body != nil {
					r.Body.Close()
				}
				return reply(401, "unauthorized"), nil
			})
			transport := must(NewTokenTransport(base, TokenBinding{URL: "https://api.example", Source: source}))
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://api.example/items", strings.NewReader("body"))
			if mode == "authority" {
				req.Host = "other.example"
			}
			if mode == "origin" {
				req.URL.Host = "other.example"
			}
			if mode == "stream" {
				req.GetBody = nil
			}
			resp, err := transport.RoundTrip(req)
			if resp != nil {
				resp.Body.Close()
			}
			if mode == "refresh-error" {
				if !errors.Is(err, ErrAuthRefresh) || !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") {
					t.Fatal(err)
				}
				return
			}
			if err != nil || resp.StatusCode != 401 {
				t.Fatal(resp, err)
			}
			wantCalls, wantRefresh := 1, 0
			if mode == "second-401" {
				wantCalls, wantRefresh = 2, 1
			}
			if calls != wantCalls || refreshes != wantRefresh {
				t.Fatalf("calls=%d refreshes=%d", calls, refreshes)
			}
		})
	}
}

func TestRefreshDoesNotReturnExpiredNewerToken(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	source := must(NewTokenSource("newer", now.Add(time.Minute), func(context.Context) (string, time.Time, error) {
		calls++
		return "fresh", now.Add(time.Hour), nil
	}, WithTokenClock(func() time.Time { return now })))
	defer source.Close()
	now = now.Add(2 * time.Minute)
	token, _, err := source.Refresh(t.Context(), "older")
	if err != nil || token != "fresh" || calls != 1 {
		t.Fatal(token, calls, err)
	}
}
