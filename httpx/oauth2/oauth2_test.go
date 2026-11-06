//go:build unit

package oauth2

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
	xoauth2 "golang.org/x/oauth2"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSourceForcesRefreshAndRetainsRotatedCredential(t *testing.T) {
	calls, rotations := 0, 0
	storageErr := errors.New("private persistence error")
	client, err := httpx.NewClient(httpx.WithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return nil, err
		}
		want := "refresh-old"
		if calls == 2 {
			want = "refresh-new"
		}
		if r.Form.Get("refresh_token") != want || r.Form.Get("grant_type") != "refresh_token" {
			t.Error("refresh grant not using newest credential")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"access_token":"access-new","refresh_token":"refresh-new","token_type":"Bearer","expires_in":3600}`))}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx := context.WithValue(t.Context(), xoauth2.HTTPClient, client)
	tok := &xoauth2.Token{AccessToken: "access-old", RefreshToken: "refresh-old", Expiry: time.Now().Add(time.Hour)}
	cfg := &xoauth2.Config{ClientID: "test", Endpoint: xoauth2.Endpoint{TokenURL: "https://auth.example/token", AuthStyle: xoauth2.AuthStyleInParams}}
	source, err := Source(cfg, tok, func(_ context.Context, token *xoauth2.Token) error {
		rotations++
		if token.RefreshToken != "refresh-new" {
			t.Error("rotation snapshot")
		}
		token.RefreshToken = "mutated callback"
		if rotations == 1 {
			return storageErr
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if token, _, err := source.Token(ctx); err != nil || token != "access-old" || calls != 0 {
		t.Fatal(token, err, calls)
	}
	_, _, err = source.Refresh(ctx, "access-old")
	if !errors.Is(err, storageErr) || !errors.Is(err, httpx.ErrAuthRefresh) || strings.Contains(err.Error(), "private") {
		t.Fatal(err)
	}
	if token, _, err := source.Token(ctx); err != nil || token != "access-new" || calls != 1 || rotations != 1 {
		t.Fatal(token, err, calls, rotations)
	}
	if _, _, err := source.Refresh(ctx, "access-new"); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	if tok.AccessToken != "access-old" || tok.RefreshToken != "refresh-old" {
		t.Fatal("mutated caller token")
	}
}

type grantFunc func() (*xoauth2.Token, error)

func (f grantFunc) Token() (*xoauth2.Token, error) { return f() }

func TestReacquireRebuildsGrantWithCurrentContext(t *testing.T) {
	type key struct{}
	values := []string{}
	source, err := Reacquire(func(ctx context.Context) xoauth2.TokenSource {
		values = append(values, ctx.Value(key{}).(string))
		return grantFunc(func() (*xoauth2.Token, error) { return &xoauth2.Token{AccessToken: ctx.Value(key{}).(string)}, nil })
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	ctx := context.WithValue(t.Context(), key{}, "first")
	if token, _, err := source.Token(ctx); err != nil || token != "first" {
		t.Fatal(token, err)
	}
	ctx = context.WithValue(t.Context(), key{}, "second")
	if token, _, err := source.Refresh(ctx, "first"); err != nil || token != "second" {
		t.Fatal(token, err)
	}
	if len(values) != 2 {
		t.Fatal(values)
	}
}

func TestInvalidSourceConfiguration(t *testing.T) {
	if _, err := Source(nil, nil, nil); !errors.Is(err, httpx.ErrInvalidConfig) {
		t.Fatal(err)
	}
	if _, err := Reacquire(nil); !errors.Is(err, httpx.ErrInvalidConfig) {
		t.Fatal(err)
	}
}
