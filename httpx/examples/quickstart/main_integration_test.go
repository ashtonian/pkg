//go:build integration

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ashtonian/pkg/httpx"
)

func TestRunRetriesAndDecodes(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/users/1" || r.Header.Get("Authorization") != "Bearer example-token" {
			t.Errorf("unexpected request: %s %s, auth present=%t", r.Method, r.URL.Path, r.Header.Get("Authorization") != "")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1,"name":"Ada"}`) // fixture; client observes write failures
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := run(t.Context(), srv.URL+"/", "example-token", &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "Ada (HTTP 200)\n" || attempts.Load() != 2 {
		t.Fatalf("output=%q attempts=%d", out.String(), attempts.Load())
	}
}

func TestRunRejectsFailureResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http failure", http.StatusUnauthorized, `{"error":"no access"}`, "HTTP 401"},
		{"invalid JSON", http.StatusOK, `{`, "unexpected end"},
		{"oversized body", http.StatusOK, strings.Repeat("x", (1<<20)+1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body) // fixture; client observes write failures
			}))
			defer srv.Close()
			var out bytes.Buffer
			err := run(t.Context(), srv.URL, "token", &out)
			if err == nil || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
			if tc.name == "oversized body" && !errors.Is(err, httpx.ErrResponseTooLarge) {
				t.Fatalf("error=%v, want ErrResponseTooLarge", err)
			}
			if out.Len() != 0 {
				t.Fatalf("failure printed a user: %q", out.String())
			}
		})
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := run(ctx, "https://api.example.com", "token", io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}
