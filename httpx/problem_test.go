//go:build unit

package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type problemBody struct {
	io.Reader
	closed int
}

func (b *problemBody) Close() error { b.closed++; return nil }

func problemResponse(body, mediaType string) (*http.Response, *problemBody) {
	r := &problemBody{Reader: strings.NewReader(body)}
	return &http.Response{StatusCode: 422, Header: http.Header{"Content-Type": {mediaType}}, Body: r}, r
}

func TestDecodeProblem(t *testing.T) {
	const document = `{"type":"/problems/invalid","title":"Invalid input","status":400,"detail":"Fix the name","instance":"requests/7","code":"E2001","errors":[ {"pointer":"/name","detail":"required"} ],"large":9007199254740993}`
	resp, body := problemResponse(document, "Application/Problem+JSON; charset=utf-8")
	resp.Request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.example.test/v1/items", nil)
	p, err := DecodeProblem(resp)
	if err != nil {
		t.Fatal(err)
	}
	if p.Type != "https://api.example.test/problems/invalid" || p.Title != "Invalid input" || p.Status != 400 || resp.StatusCode != 422 || p.Detail != "Fix the name" || p.Instance != "https://api.example.test/v1/requests/7" {
		t.Fatalf("problem=%+v response status=%d", p, resp.StatusCode)
	}
	if string(p.Extensions["errors"]) != `[ {"pointer":"/name","detail":"required"} ]` || string(p.Extensions["large"]) != "9007199254740993" {
		t.Fatalf("extensions changed: %#v", p.Extensions)
	}
	var code string
	if err := p.Extension("code", &code); err != nil || code != "E2001" {
		t.Fatalf("code=%q err=%v", code, err)
	}
	if err := p.Extension("missing", &code); !errors.Is(err, ErrProblemExtensionNotFound) {
		t.Fatalf("missing: %v", err)
	}
	var number int
	if err := p.Extension("code", &number); err == nil {
		t.Fatal("accepted wrong extension type")
	}
	p.Extensions["status"] = json.RawMessage(`999`)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var again Problem
	if err := json.Unmarshal(data, &again); err != nil {
		t.Fatal(err)
	}
	if again.Status != 400 || string(again.Extensions["large"]) != "9007199254740993" {
		t.Fatalf("round trip: %+v", again)
	}
	if strings.Contains(p.Error(), p.Detail) || strings.Contains(p.Error(), p.Instance) {
		t.Fatal("error automatically included occurrence details")
	}
	retained, err := io.ReadAll(resp.Body)
	if err != nil || string(retained) != document || body.closed != 1 {
		t.Fatalf("retained=%s closed=%d err=%v", retained, body.closed, err)
	}
}

func TestProblemMemberTypes(t *testing.T) {
	for _, tt := range []struct {
		name, document, wantType string
		wantStatus               int
		invalid                  bool
	}{
		{"defaults", `{}`, "about:blank", 0, false},
		{"wrong types ignored", `{"type":12,"title":false,"status":"400","detail":[],"instance":{},"extra":null}`, "about:blank", 0, false},
		{"null members", `{"type":null,"status":null}`, "about:blank", 0, false},
		{"case-sensitive names", `{"Type":"custom","Status":400}`, "about:blank", 0, false},
		{"empty URI reference", `{"type":"","status":409}`, "", 409, false},
		{"integral decimal status", `{"status":422.0}`, "about:blank", 422, false},
		{"exponent status", `{"status":4.22e2}`, "about:blank", 422, false},
		{"fractional status ignored", `{"status":422.5}`, "about:blank", 0, false},
		{"invalid status ignored", `{"status":999}`, "about:blank", 0, false},
		{"array", `[]`, "", 0, true},
		{"null object", `null`, "", 0, true},
		{"trailing document", `{} {}`, "", 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var p Problem
			err := json.Unmarshal([]byte(tt.document), &p)
			if (err != nil) != tt.invalid {
				t.Fatalf("error=%v", err)
			}
			if !tt.invalid && (p.Type != tt.wantType || p.Status != tt.wantStatus) {
				t.Fatalf("problem=%+v", p)
			}
		})
	}
}

func TestDecodeProblemBodyOwnership(t *testing.T) {
	for _, mediaType := range []string{"application/json", "application/problem+xml", "application/vendor+json", "", "application/problem+json; invalid"} {
		t.Run(mediaType, func(t *testing.T) {
			resp, body := problemResponse(`{"title":"failure"}`, mediaType)
			_, err := DecodeProblem(resp)
			if !errors.Is(err, ErrNotProblem) || body.closed != 0 {
				t.Fatalf("err=%v closed=%d", err, body.closed)
			}
			data, err := io.ReadAll(resp.Body)
			if err != nil || string(data) != `{"title":"failure"}` {
				t.Fatalf("body=%q err=%v", data, err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("oversized is retained", func(t *testing.T) {
		resp, body := problemResponse(`{"title":"failure"}`, ContentTypeProblemJSON)
		_, err := DecodeProblem(resp, WithMaxResponseBytes(4))
		if !errors.Is(err, ErrResponseTooLarge) || body.closed != 0 {
			t.Fatalf("err=%v closed=%d", err, body.closed)
		}
		data, err := io.ReadAll(resp.Body)
		if err != nil || string(data) != `{"title":"failure"}` {
			t.Fatalf("body=%q err=%v", data, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if body.closed != 1 {
			t.Fatalf("closed=%d", body.closed)
		}
	})
	t.Run("explicit media type", func(t *testing.T) {
		resp, _ := problemResponse(`{}`, "application/vnd.example+json; version=1")
		if _, err := DecodeProblem(resp, WithProblemMediaTypes("application/vnd.example+json")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("empty type reference", func(t *testing.T) {
		resp, _ := problemResponse(`{"type":""}`, ContentTypeProblemJSON)
		resp.Request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/items", nil)
		p, err := DecodeProblem(resp)
		if err != nil || p.Type != "https://example.test/items" || p.Instance != "" {
			t.Fatalf("problem=%+v err=%v", p, err)
		}
	})
	t.Run("invalid options preserve body", func(t *testing.T) {
		for _, option := range []DecodeOption{WithMaxResponseBytes(0), WithProblemMediaTypes("text/plain"), WithProblemMediaTypes("application/json; charset=utf-8")} {
			resp, body := problemResponse(`{}`, ContentTypeProblemJSON)
			if _, err := DecodeProblem(resp, option); !errors.Is(err, ErrInvalidConfig) || body.closed != 0 {
				t.Fatalf("err=%v closed=%d", err, body.closed)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
		}
	})
	if _, err := DecodeProblem(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("nil response: %v", err)
	}
	if _, err := DecodeProblem(&http.Response{Header: http.Header{"Content-Type": {ContentTypeProblemJSON}}}); !errors.Is(err, io.EOF) {
		t.Fatalf("nil body: %v", err)
	}
}

func TestTypedProblemErrors(t *testing.T) {
	for _, tt := range []struct {
		name, method, mediaType, document string
		status                            int
		opts                              []DecodeOption
		wantProblem, wantError            bool
	}{
		{"default", "GET", ContentTypeProblemJSON, `{"title":"refused"}`, 400, nil, false, false},
		{"opt in", "GET", ContentTypeProblemJSON, `{"title":"refused"}`, 400, []DecodeOption{WithProblemErrors()}, true, true},
		{"ordinary JSON", "GET", ContentTypeJSON, `{"title":"refused"}`, 400, []DecodeOption{WithProblemErrors()}, false, false},
		{"custom JSON", "GET", ContentTypeJSON, `{"title":"refused"}`, 400, []DecodeOption{WithProblemErrors(), WithProblemMediaTypes(ContentTypeJSON)}, true, true},
		{"malformed", "GET", ContentTypeProblemJSON, `{"title":`, 400, []DecodeOption{WithProblemErrors()}, false, true},
		{"oversized", "GET", ContentTypeProblemJSON, `{"title":"refused"}`, 400, []DecodeOption{WithProblemErrors(), WithMaxResponseBytes(4)}, false, true},
		{"HEAD", "HEAD", ContentTypeProblemJSON, "", 400, []DecodeOption{WithProblemErrors()}, false, false},
		{"success", "GET", ContentTypeJSON, `{"name":"ok"}`, 200, []DecodeOption{WithProblemErrors()}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := problemResponse(tt.document, tt.mediaType)
			resp.StatusCode = tt.status
			client := &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
				resp.Request = r
				return resp, nil
			}), Timeout: time.Second}
			req, _ := http.NewRequestWithContext(t.Context(), tt.method, "https://example.test/items", nil)
			_, got, err := Do[map[string]any](client, req, tt.opts...)
			var p *Problem
			if errors.As(err, &p) != tt.wantProblem || (err != nil) != tt.wantError || got != resp {
				t.Fatalf("response=%p err=%v", got, err)
			}
			if tt.wantProblem && !strings.Contains(req.Header.Get("Accept"), ContentTypeProblemJSON) {
				t.Fatalf("Accept=%q", req.Header.Get("Accept"))
			}
			if tt.status != 200 && !tt.wantError && body.closed != 0 {
				t.Fatal("unselected body was closed")
			}
			if err := got.Body.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
