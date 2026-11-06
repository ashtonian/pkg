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
