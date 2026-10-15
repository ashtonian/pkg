//go:build integration

package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type capturingLogHook struct {
	req  *http.Request
	resp *http.Response
	err  error
}

func (h *capturingLogHook) LogRequest(req *http.Request) { h.req = req }
func (h *capturingLogHook) LogResponse(_ *http.Request, resp *http.Response, _ time.Duration, err error) {
	h.resp, h.err = resp, err
}

func TestLoggingTransportThroughClient(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc"})
		_, _ = io.WriteString(w, "hello")
	}))
	defer server.Close()

	hook := &capturingLogHook{}
	client := must(NewClient(
		WithAuth(server.URL, NewBearerTokenProvider("secret")),
		WithLogging(hook),
	))

	req, _ := http.NewRequest(http.MethodGet, server.URL+"/?access_token=abc", nil)
	req.Header.Set("Cookie", "session=abc")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if gotAuth != "Bearer secret" || string(body) != "hello" || resp.Header.Get("Set-Cookie") == "" {
		t.Fatalf("the wire exchange must be unaffected: auth=%q body=%q", gotAuth, body)
	}
	if hook.req.Header.Get("Cookie") != Redacted || hook.req.URL.Query().Get("access_token") != Redacted {
		t.Errorf("request snapshot not redacted: %v %s", hook.req.Header, hook.req.URL)
	}
	if hook.resp.Header.Get("Set-Cookie") != Redacted {
		t.Errorf("response snapshot not redacted: %v", hook.resp.Header)
	}
}

func TestLoggingTransportReportsDialErrors(t *testing.T) {
	hook := &capturingLogHook{}
	client := must(NewClient(WithLogging(hook), WithTimeout(time.Second)))

	if _, err := client.Get("http://127.0.0.1:1"); err == nil {
		t.Fatal("expected a dial error")
	}
	if hook.resp != nil || hook.err == nil {
		t.Fatalf("hook should see the error and no response, got resp=%v err=%v", hook.resp, hook.err)
	}
}
