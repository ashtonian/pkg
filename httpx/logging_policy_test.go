//go:build unit

package httpx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func privateRoute(u url.URL) (url.URL, error) {
	u.Path = "/download/{account}/{blob}"
	return u, nil
}

func TestLoggingURLPolicy(t *testing.T) {
	const secret = "private-marker"
	for _, raw := range []string{
		"https://user:private-marker@provider.test/download/private-marker/blob?token=private-marker&page=2#private-marker",
		"https://provider.test/download/%70rivate-marker%2Fblob",
	} {
		t.Run(raw, func(t *testing.T) {
			var entries []LogEntry
			var sent *http.Request
			original := &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{
				"Location":         {"/download/" + secret + "/new?token=" + secret},
				"Content-Location": {"https://provider.test/download/" + secret + "/new"},
				"Set-Cookie":       {"token=" + secret},
			}}
			transport := must(NewLoggingTransport(stubRoundTripper(func(r *http.Request) (*http.Response, error) {
				sent = r
				return original, nil
			}), &StructuredLoggingHook{LogFunc: func(e LogEntry) { entries = append(entries, e) }}, WithURLRedactor(privateRoute)))
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, raw, nil)
			req.Header.Set("Referer", "/download/"+secret+"?token="+secret)
			req.Header.Set("Authorization", "Bearer "+secret)
			resp, err := transport.RoundTrip(req)
			if err != nil || resp != original || sent != req || sent.Context() != req.Context() || req.URL.String() != raw || !strings.Contains(req.Header.Get("Referer"), secret) {
				t.Fatalf("wire changed: err=%v", err)
			}
			if len(entries) != 2 {
				t.Fatalf("entries=%d", len(entries))
			}
			for _, entry := range entries {
				if strings.Contains(fmt.Sprint(entry), secret) || !strings.Contains(entry.URL, "/download/") || entry.Method != "GET" {
					t.Fatalf("entry=%+v", entry)
				}
			}
			if entries[1].StatusCode != 200 || entries[1].Duration < 0 {
				t.Fatalf("response=%+v", entries[1])
			}
			if !strings.Contains(resp.Header.Get("Location"), secret) {
				t.Fatal("response mutated")
			}
		})
	}
}

func TestLoggingURLPolicyErrorTraversal(t *testing.T) {
	const secret = "private-marker"
	for _, raw := range []string{
		"/download/private-marker?token=private-marker",
		"https://provider.test/download/%70rivate-marker",
		"/download/private-marker/\"private-marker\"",
	} {
		t.Run(raw, func(t *testing.T) {
			hook := &recordingLogHook{}
			original := errors.Join(fmt.Errorf("wrapped: %w", &url.Error{Op: "Get", URL: raw, Err: io.EOF}),
				fmt.Errorf("second: %w", &url.Error{Op: "Get", URL: "/download/private-marker/other", Err: io.ErrUnexpectedEOF}),
				errors.New("proxy https://proxy.test/download/private-marker?token=private-marker"))
			transport := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) { return nil, original }), hook, WithURLRedactor(privateRoute)))
			req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://provider.test/download/private-marker", nil)
			_, err := transport.RoundTrip(req)
			if err != original || !errors.Is(err, io.EOF) || !strings.Contains(err.Error(), secret) {
				t.Fatal("caller error changed")
			}
			if hook.err == nil || strings.Contains(hook.err.Error(), secret) || errors.Unwrap(hook.err) != nil || errors.Is(hook.err, io.EOF) {
				t.Fatalf("hook error=%v", hook.err)
			}
		})
	}
}

func TestLoggingURLPolicyFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		policy func(url.URL) (url.URL, error)
	}{
		{"error", func(u url.URL) (url.URL, error) { return u, errors.New("private-marker") }},
		{"panic", func(url.URL) (url.URL, error) { panic("private-marker") }},
		{"invalid host", func(u url.URL) (url.URL, error) { u.Host = "[private-marker"; return u, nil }},
		{"missing host", func(u url.URL) (url.URL, error) { u.Scheme, u.Host = "https", ""; return u, nil }},
		{"opaque", func(u url.URL) (url.URL, error) { u.Opaque = "private-marker"; return u, nil }},
		{"empty", func(url.URL) (url.URL, error) { return url.URL{}, nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var entries []LogEntry
			original := &http.Response{StatusCode: 503, Body: http.NoBody, Header: http.Header{"Location": {"/private-marker"}}}
			cause := &url.Error{Op: "Get", URL: "https://provider.test/private-marker", Err: io.EOF}
			transport := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) { return original, cause }), &StructuredLoggingHook{LogFunc: func(e LogEntry) { entries = append(entries, e) }}, WithURLRedactor(tt.policy)))
			req, _ := http.NewRequestWithContext(t.Context(), "GET", cause.URL, nil)
			req.Header.Set("Referer", "/private-marker")
			resp, err := transport.RoundTrip(req)
			if resp != original || err != cause {
				t.Fatal("policy failure changed wire outcome")
			}
			if len(entries) != 2 || entries[1].StatusCode != 503 {
				t.Fatalf("entries=%+v", entries)
			}
			for _, entry := range entries {
				if strings.Contains(fmt.Sprint(entry), "private-marker") || entry.URL != Redacted || entry.Method != "GET" {
					t.Fatalf("entry=%+v", entry)
				}
			}
		})
	}
	if _, err := NewLoggingTransport(http.DefaultTransport, nil, WithURLRedactor(nil)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil policy: %v", err)
	}
}

func TestLoggingURLPolicyDetachment(t *testing.T) {
	hook := &recordingLogHook{}
	var retained url.URL
	policy := func(u url.URL) (url.URL, error) {
		if u.RawPath != "" || u.User != nil || u.Fragment != "" || u.Query().Get("token") != Redacted {
			t.Fatalf("policy input not sanitized: %+v", u)
		}
		retained = u
		u.Path = "/route"
		u.RawPath = "/private-marker"
		u.User = url.UserPassword("user", "private-marker")
		u.RawQuery = "token=private-marker"
		u.Fragment = "private-marker"
		return u, nil
	}
	response := &http.Response{StatusCode: 200, Header: http.Header{"X-Test": {"wire"}}, Body: http.NoBody}
	transport := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) { return response, nil }), hook, WithURLRedactor(policy)))
	req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://user:private-marker@provider.test/%70rivate-marker?token=private-marker#private-marker", nil)
	req.Header.Set("X-Test", "wire")
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if hook.req.URL.RawPath != "" || hook.req.URL.Path != "/route" || strings.Contains(hook.req.URL.String(), "private-marker") {
		t.Fatalf("snapshot URL=%+v", hook.req.URL)
	}
	retained.Path, retained.Host = "mutated", "mutated"
	if hook.req.URL.Host != "provider.test" {
		t.Fatal("policy retained snapshot alias")
	}
	hook.req.URL.Path = "/hook"
	hook.req.Header.Set("X-Test", "hook")
	hook.resp.Header.Set("X-Test", "hook")
	if req.URL.Path != "/private-marker" || req.Header.Get("X-Test") != "wire" || response.Header.Get("X-Test") != "wire" {
		t.Fatal("hook can mutate live request/response")
	}
}

func TestLoggingURLPolicyConcurrent(t *testing.T) {
	var mu sync.Mutex
	var entries []LogEntry
	transport := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}), &StructuredLoggingHook{LogFunc: func(e LogEntry) {
		mu.Lock()
		defer mu.Unlock()
		entries = append(entries, e)
	}}, WithURLRedactor(privateRoute)))
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://provider.test/private-marker", nil)
			if _, err := transport.RoundTrip(req); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	if len(entries) != 40 {
		t.Fatalf("entries=%d", len(entries))
	}
	for _, entry := range entries {
		if strings.Contains(fmt.Sprint(entry), "private-marker") {
			t.Fatalf("entry=%+v", entry)
		}
	}
}
