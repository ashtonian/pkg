//go:build integration

package httpx

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// credentialRecorder is an origin server that records the credential-bearing
// parts of every request it receives.
type credentialRecorder struct {
	srv *httptest.Server

	mu   sync.Mutex
	seen []seenRequest
}

type seenRequest struct {
	path          string
	authorization string
	apiKey        string
	queryToken    string
}

func newCredentialRecorder(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *credentialRecorder {
	t.Helper()
	rec := &credentialRecorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.seen = append(rec.seen, seenRequest{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			apiKey:        r.Header.Get("X-Api-Key"),
			queryToken:    r.URL.Query().Get("token"),
		})
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (r *credentialRecorder) requests() []seenRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]seenRequest(nil), r.seen...)
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }

func TestAuthCredentialsStayOnTheirOrigin(t *testing.T) {
	providers := []struct {
		name     string
		provider AuthProvider
		leaked   func(seenRequest) bool
	}{
		{"bearer", NewBearerTokenProvider("secret"), func(s seenRequest) bool { return s.authorization != "" }},
		{"basic", NewBasicAuthProvider("user", "secret"), func(s seenRequest) bool { return s.authorization != "" }},
		{"api key", NewAPIKeyProvider("secret"), func(s seenRequest) bool { return s.apiKey != "" }},
		{"query param", NewQueryParamAuthProvider("token", "secret"), func(s seenRequest) bool { return s.queryToken != "" }},
	}

	for _, p := range providers {
		t.Run(p.name+"/cross-origin redirect", func(t *testing.T) {
			other := newCredentialRecorder(t, ok)
			bound := newCredentialRecorder(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, other.srv.URL+"/landing", http.StatusFound)
			})

			client := must(NewClient(WithAuth(bound.srv.URL, p.provider)))
			resp, err := client.Get(bound.srv.URL + "/start")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			if got := bound.requests(); len(got) != 1 || !p.leaked(got[0]) {
				t.Fatalf("bound origin should receive the credential once, got %+v", got)
			}
			for _, s := range other.requests() {
				if p.leaked(s) {
					t.Fatalf("credential forwarded to another origin: %+v", s)
				}
			}
			if len(other.requests()) != 1 {
				t.Fatalf("redirect target should be reached once, got %d", len(other.requests()))
			}
		})

		t.Run(p.name+"/same-origin redirect", func(t *testing.T) {
			srv := newCredentialRecorder(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "/landing", http.StatusFound)
					return
				}
				w.WriteHeader(http.StatusOK)
			})

			client := must(NewClient(WithAuth(srv.srv.URL, p.provider)))
			resp, err := client.Get(srv.srv.URL + "/start")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			got := srv.requests()
			if len(got) != 2 || !p.leaked(got[0]) || !p.leaked(got[1]) {
				t.Fatalf("both same-origin hops should carry the credential, got %+v", got)
			}
		})

		t.Run(p.name+"/unbound origin", func(t *testing.T) {
			bound := newCredentialRecorder(t, ok)
			other := newCredentialRecorder(t, ok)

			client := must(NewClient(WithAuth(bound.srv.URL, p.provider)))
			resp, err := client.Get(other.srv.URL + "/direct")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()

			if s := other.requests(); len(s) != 1 || p.leaked(s[0]) {
				t.Fatalf("unbound origin must not receive the credential, got %+v", s)
			}
		})
	}
}

func TestAuthBindsEachOriginToItsOwnProvider(t *testing.T) {
	a := newCredentialRecorder(t, ok)
	b := newCredentialRecorder(t, ok)

	client := must(NewClient(
		WithAuth(a.srv.URL, NewBearerTokenProvider("token-a")),
		WithAuth(b.srv.URL, NewBearerTokenProvider("token-b")),
	))
	for _, u := range []string{a.srv.URL, b.srv.URL} {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	if got := a.requests()[0].authorization; got != "Bearer token-a" {
		t.Errorf("origin a got %q", got)
	}
	if got := b.requests()[0].authorization; got != "Bearer token-b" {
		t.Errorf("origin b got %q", got)
	}
}
