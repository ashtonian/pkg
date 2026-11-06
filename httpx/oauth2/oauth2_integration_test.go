//go:build integration

package oauth2

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
	xoauth2 "golang.org/x/oauth2"
)

func TestTokenEndpointAndAPIWorkflow(t *testing.T) {
	var grants atomic.Int32
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grants.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("refresh_token") != "refresh" {
			t.Error("missing refresh grant")
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"access_token":"new","refresh_token":"rotated","token_type":"Bearer","expires_in":3600}`); err != nil {
			t.Error(err)
		}
	}))
	defer issuer.Close()
	var persisted atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer new" {
			if !persisted.Load() {
				t.Error("replayed before persistence")
			}
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(401)
	}))
	defer api.Close()
	grantClient, err := httpx.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	defer grantClient.CloseIdleConnections()
	ctx := context.WithValue(t.Context(), xoauth2.HTTPClient, grantClient)
	source, err := Source(&xoauth2.Config{Endpoint: xoauth2.Endpoint{TokenURL: issuer.URL, AuthStyle: xoauth2.AuthStyleInParams}}, &xoauth2.Token{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(time.Hour)}, func(context.Context, *xoauth2.Token) error { persisted.Store(true); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	client, err := httpx.NewClient(httpx.WithTokenAuth(api.URL, source))
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 204 || grants.Load() != 1 {
		t.Fatal(resp.StatusCode, grants.Load())
	}
}
