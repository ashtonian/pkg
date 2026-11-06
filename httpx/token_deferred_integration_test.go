//go:build integration

package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenChallengeAuthentication(t *testing.T) {
	for _, mode := range []string{"anonymous-success", "anonymous-401", "api-key-401", "eager"} {
		t.Run(mode, func(t *testing.T) {
			var requests, refreshes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := requests.Add(1)
				if mode != "eager" && n == 1 {
					if r.Header.Get("Authorization") != "" {
						t.Error("acquired token before challenge")
					}
					wantKey := ""
					if mode == "api-key-401" {
						wantKey = "initial-key"
					}
					if r.Header.Get("X-API-Key") != wantKey {
						t.Error("incorrect initial credentials")
					}
					if mode != "anonymous-success" {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				} else if r.Header.Get("Authorization") != "Bearer new" || r.Header.Get("X-API-Key") != "" {
					t.Error("incorrect bearer credentials")
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			var opts []TokenOption
			if mode != "eager" {
				opts = append(opts, WithDeferredAcquisition())
			}
			source, err := NewTokenSource("", time.Time{}, func(context.Context) (string, time.Time, error) {
				refreshes.Add(1)
				wantRequests := int32(1)
				if mode == "eager" {
					wantRequests = 0
				}
				if requests.Load() != wantRequests {
					t.Error("callback ran at the wrong stage")
				}
				return "new", time.Time{}, nil
			}, opts...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := source.Close(); err != nil {
					t.Error(err)
				}
			})
			binding := TokenBinding{URL: server.URL, Source: source}
			if mode == "api-key-401" {
				binding.InitialAuth = NewAPIKeyProvider("initial-key")
			}
			base, err := NewBaseTransport()
			if err != nil {
				t.Fatal(err)
			}
			defer base.CloseIdleConnections()
			transport, err := NewTokenTransport(base, binding)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
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
			wantRequests, wantRefreshes := int32(2), int32(1)
			switch mode {
			case "anonymous-success":
				wantRequests, wantRefreshes = 1, 0
			case "eager":
				wantRequests = 1
			}
			if resp.StatusCode != http.StatusNoContent || requests.Load() != wantRequests || refreshes.Load() != wantRefreshes {
				t.Fatalf("status=%d requests=%d refreshes=%d", resp.StatusCode, requests.Load(), refreshes.Load())
			}
		})
	}
}
