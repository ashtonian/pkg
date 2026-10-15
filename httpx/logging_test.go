//go:build unit

package httpx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// recordingLogHook captures exactly what a LoggingHook receives.
type recordingLogHook struct {
	req  *http.Request
	resp *http.Response
	err  error
}

func (h *recordingLogHook) LogRequest(req *http.Request) { h.req = req }
func (h *recordingLogHook) LogResponse(req *http.Request, resp *http.Response, _ time.Duration, err error) {
	h.resp, h.err = resp, err
}

type stubRoundTripper func(*http.Request) (*http.Response, error)

func (f stubRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoggingRedactsHookSnapshots(t *testing.T) {
	var sent *http.Request
	base := stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		sent = r
		h := http.Header{}
		h.Set("Set-Cookie", "session=abc")
		h.Set("X-Request-Id", "r-1")
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader("payload")), Request: r}, nil
	})
	hook := &recordingLogHook{}
	lt := must(NewLoggingTransport(base, hook, WithSensitiveHeaders("x-tenant-secret"), WithSensitiveQueryParams("Session")))

	req, _ := http.NewRequest(http.MethodPost, "https://user:pass@api.example.com/v1?access_token=t0k&session=s1&page=2", strings.NewReader("body"))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header["x-api-key"] = []string{"k"} // non-canonical write
	req.Header.Set("X-Tenant-Secret", "ts")
	req.Header.Set("Accept", "application/json")

	resp, err := lt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}

	// The wire request and the caller's response are untouched.
	if sent.Header.Get("Authorization") != "Bearer secret" || sent.URL.User == nil {
		t.Fatal("redaction must not modify the request sent upstream")
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "payload" {
		t.Fatalf("caller body consumed or replaced: %q", b)
	}

	for name, want := range map[string]string{
		"Authorization":   Redacted,
		"X-Api-Key":       Redacted,
		"X-Tenant-Secret": Redacted,
		"Accept":          "application/json",
	} {
		if got := hook.req.Header.Get(name); got != want {
			t.Errorf("request header %s = %q, want %q", name, got, want)
		}
	}
	if hook.req.URL.User != nil {
		t.Error("URL userinfo reached the hook")
	}
	q := hook.req.URL.Query()
	if q.Get("access_token") != Redacted || q.Get("session") != Redacted || q.Get("page") != "2" {
		t.Errorf("query redaction wrong: %v", q)
	}
	if hook.req.Body != http.NoBody {
		t.Error("request snapshot must not carry a body")
	}
	if got := hook.resp.Header.Get("Set-Cookie"); got != Redacted {
		t.Errorf("Set-Cookie = %q", got)
	}
	if got := hook.resp.Header.Get("X-Request-Id"); got != "r-1" {
		t.Errorf("X-Request-Id = %q", got)
	}
	if hook.resp.Body != http.NoBody {
		t.Error("response snapshot must not carry a body")
	}
}

func TestLoggingReportsTransportErrors(t *testing.T) {
	boom := errors.New("dial failed")
	hook := &recordingLogHook{}
	lt := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) { return nil, boom }), hook))

	req, _ := http.NewRequest(http.MethodGet, "https://api.example.com/", nil)
	if _, err := lt.RoundTrip(req); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if hook.err == nil || hook.err.Error() != boom.Error() || hook.resp != nil {
		t.Fatalf("hook should receive detached error text and no response, got resp=%v err=%v", hook.resp, hook.err)
	}
	if errors.Is(hook.err, boom) || errors.Unwrap(hook.err) != nil {
		t.Fatal("hook can recover the original transport error")
	}
}

func TestLoggingRedactsUnparseableQuery(t *testing.T) {
	hook := &recordingLogHook{}
	lt := must(NewLoggingTransport(stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	}), hook))

	req := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "h", RawQuery: "token=%zz"}, Header: http.Header{}}
	if _, err := lt.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if hook.req.URL.RawQuery != Redacted {
		t.Errorf("unparseable query must be redacted whole, got %q", hook.req.URL.RawQuery)
	}
}

func TestStructuredLoggingHook(t *testing.T) {
	var entries []LogEntry
	hook := &StructuredLoggingHook{LogFunc: func(e LogEntry) { entries = append(entries, e) }}

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	req.Header.Set("Content-Type", "application/json")
	hook.LogRequest(req)
	hook.LogResponse(req, &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}}, 100*time.Millisecond, nil)
	hook.LogResponse(req, nil, time.Millisecond, errors.New("connection refused"))

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	if e := entries[0]; e.Type != "request" || e.Method != "GET" || e.URL != "http://example.com/path" || e.ContentType != "application/json" {
		t.Errorf("request entry: %+v", e)
	}
	if e := entries[1]; e.Type != "response" || e.StatusCode != 200 || e.Duration != 100*time.Millisecond || e.Error != "" {
		t.Errorf("response entry: %+v", e)
	}
	if e := entries[2]; e.StatusCode != 0 || e.Error != "connection refused" {
		t.Errorf("error entry: %+v", e)
	}
}

func TestStructuredLoggingHookWithoutLogFunc(t *testing.T) {
	hook := &StructuredLoggingHook{}
	hook.LogRequest(nil)
	hook.LogResponse(nil, nil, 0, nil)
}

// Credentials outside the request URL's query and the sensitive headers —
// fragments, URL-valued headers, a redirect response, error text, a rejected
// origin — never reach a logging hook.
func TestLoggingRedactsResidualCredentialSurfaces(t *testing.T) {
	const secret = "credential-marker"
	t.Run("URL-valued headers and fragments", func(t *testing.T) {
		var entries []LogEntry
		lt := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 302, Header: http.Header{
				"Location":         {"https://example.test/callback?code=" + secret},
				"Content-Location": {"https://user:" + secret + "@example.test/x"},
			}, Body: http.NoBody}, nil
		}), &StructuredLoggingHook{LogFunc: func(e LogEntry) { entries = append(entries, e) }}))
		r, _ := http.NewRequest(http.MethodGet, "https://example.test/#access_token="+secret, nil)
		r.Header.Set("Referer", "https://example.test/?access_token="+secret)
		resp, err := lt.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, e := range entries {
			if strings.Contains(fmt.Sprint(e), secret) {
				t.Errorf("%s entry logged the credential: %+v", e.Type, e)
			}
		}
	})
	t.Run("redirect response", func(t *testing.T) {
		h := &recordingLogHook{}
		lt := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		}), h))
		r, _ := http.NewRequest(http.MethodGet, "https://example.test/landing", nil)
		r.Response = &http.Response{Header: http.Header{"Set-Cookie": {secret}}, Body: io.NopCloser(strings.NewReader(secret))}
		resp, err := lt.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if h.req.Response != nil {
			t.Error("the request snapshot carries the redirect response and its body")
		}
	})
	t.Run("error text", func(t *testing.T) {
		h := &recordingLogHook{}
		original := &url.Error{Op: "Get", URL: "https://example.test/?token=" + secret, Err: io.EOF}
		lt := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, original
		}), h))
		r, _ := http.NewRequest(http.MethodGet, "https://user:"+secret+"@example.test/", nil)
		_, err := lt.RoundTrip(r)
		if err != original || !errors.Is(err, io.EOF) {
			t.Fatalf("the caller received %v, want the original error", err)
		}
		var hookURL *url.Error
		if strings.Contains(h.err.Error(), secret) || errors.Unwrap(h.err) != nil || errors.As(h.err, &hookURL) {
			t.Errorf("hook error %q: want detached redacted text with no original cause", h.err)
		}
	})
	t.Run("rejected origin", func(t *testing.T) {
		for _, raw := range []string{"https://user:" + secret + "@example.test/", "https://user:" + secret + "@example.test:bad/"} {
			if _, err := ParseOrigin(raw); err == nil || strings.Contains(err.Error(), secret) {
				t.Errorf("ParseOrigin error %v repeats the credential", err)
			}
		}
	})
	t.Run("nil provider", func(t *testing.T) {
		_, err := NewAuthTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) {
			return nil, io.EOF
		}), AuthBinding{URL: "https://user:" + secret + "@example.test/"})
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("nil-provider error repeats the credential: %v", err)
		}
	})
}

func TestLoggingRedactsEveryErrorURL(t *testing.T) {
	for _, cause := range []error{
		errors.Join(
			&url.Error{Op: "Get", URL: "https://first.example/?token=first-secret", Err: io.EOF},
			&url.Error{Op: "Get", URL: "https://second.example/?code=second-secret", Err: io.ErrUnexpectedEOF},
		),
		errors.Join(
			&url.Error{Op: "Get", URL: "/first?token=first-secret", Err: io.EOF},
			&url.Error{Op: "Get", URL: "/second?code=second-secret", Err: io.ErrUnexpectedEOF},
		),
		errors.Join(
			&url.Error{Op: "Get", URL: "/callback?token=first-secret-remaining-secret", Err: io.EOF},
			&url.Error{Op: "Get", URL: "/callback?token=first-secret", Err: io.ErrUnexpectedEOF},
		),
		fmt.Errorf("redirect target https://other.example/callback?code=test-secret rejected: %w", io.EOF),
		fmt.Errorf("proxy HTTPS://user:test-secret@other.example/request failed: %w", io.EOF),
	} {
		t.Run(cause.Error(), func(t *testing.T) {
			hook := &recordingLogHook{}
			tr := must(NewLoggingTransport(stubRoundTripper(func(*http.Request) (*http.Response, error) {
				return nil, cause
			}), hook))
			req, _ := http.NewRequest(http.MethodGet, "https://api.example/items", nil)
			_, got := tr.RoundTrip(req)
			if got != cause || !errors.Is(got, io.EOF) {
				t.Fatal("redaction changed the caller's error or lost its cause")
			}
			var hookURL *url.Error
			if errors.Unwrap(hook.err) != nil || errors.Is(hook.err, cause) || errors.As(hook.err, &hookURL) {
				t.Fatal("hook can recover an original credential-bearing cause")
			}
			if strings.Contains(hook.err.Error(), "secret") {
				t.Fatalf("hook error contains credentials: %s", hook.err)
			}
		})
	}
}
