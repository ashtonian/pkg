//go:build unit

package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// fixedResponse is a client whose every response has status, Content-Type
// and body; sent counts requests.
func fixedResponse(status int, contentType, body string, sent *atomic.Int32) *http.Client {
	return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if sent != nil {
			sent.Add(1)
		}
		h := http.Header{}
		if contentType != "" {
			h.Set("Content-Type", contentType)
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
}

type named struct {
	Name string `json:"name"`
}

const itemEndpoint = "https://api.example.com/item"

func TestDoSkipsBodylessSuccessResponses(t *testing.T) {
	for _, tt := range []struct {
		name   string
		method string
		status int
	}{
		{"HEAD", http.MethodHead, http.StatusOK},
		{"no content", http.MethodGet, http.StatusNoContent},
		{"reset content", http.MethodPost, http.StatusResetContent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, contentType := range []string{ContentTypeJSON, "application/x-unknown"} {
				client := fixedResponse(tt.status, contentType, "", nil)
				req, err := http.NewRequestWithContext(t.Context(), tt.method, itemEndpoint, nil)
				if err != nil {
					t.Fatal(err)
				}
				value, resp, err := Do[*named](client, req)
				if resp != nil {
					if closeErr := resp.Body.Close(); closeErr != nil {
						t.Fatal(closeErr)
					}
				}
				if err != nil || value != nil || resp == nil || resp.StatusCode != tt.status {
					t.Fatalf("Content-Type %q: value=%v response=%v error=%v", contentType, value, resp, err)
				}
			}
		})
	}
}

func TestDecodeRejectsBodiesOverTheLimit(t *testing.T) {
	body := `{"name":"` + strings.Repeat("x", 64) + `"}`

	_, resp, err := Get[named](context.Background(), fixedResponse(200, ContentTypeJSON, body, nil), itemEndpoint, WithMaxResponseBytes(16))
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Get: want ErrResponseTooLarge, got %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != body {
		t.Fatalf("the response must still read the whole body, got %q", got)
	}

	direct := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
	var v named
	if err := DecodeResponse(direct, JSON, &v, WithMaxResponseBytes(16)); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("DecodeResponse: want ErrResponseTooLarge, got %v", err)
	}
	if got, _ := io.ReadAll(direct.Body); string(got) != body {
		t.Fatalf("DecodeResponse must leave the whole body readable, got %q", got)
	}
}

func TestDecodeAcceptsABodyAtTheLimit(t *testing.T) {
	body := `{"name":"ok"}`
	v, resp, err := Get[named](context.Background(), fixedResponse(200, ContentTypeJSON, body, nil), itemEndpoint, WithMaxResponseBytes(int64(len(body))))
	if err != nil || v.Name != "ok" {
		t.Fatalf("got %+v, %v", v, err)
	}
	if got, _ := io.ReadAll(resp.Body); string(got) != body {
		t.Fatalf("resp.Body should hold the bytes read, got %q", got)
	}
}

func TestDecodeRejectsAnInvalidLimitBeforeSending(t *testing.T) {
	var sent atomic.Int32
	_, _, err := Get[named](context.Background(), fixedResponse(200, ContentTypeJSON, `{}`, &sent), itemEndpoint, WithMaxResponseBytes(0))
	if !errors.Is(err, ErrInvalidConfig) || sent.Load() != 0 {
		t.Fatalf("err %v, sent %d", err, sent.Load())
	}
}

func TestDecodePointerTargets(t *testing.T) {
	v, _, err := Get[*named](context.Background(), fixedResponse(200, ContentTypeJSON, `{"name":"ok"}`, nil), itemEndpoint)
	if err != nil || v == nil || v.Name != "ok" {
		t.Fatalf("got %+v, %v", v, err)
	}

	var rec recordingCodec
	if _, err := decodeInto[*named](&rec, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if rec.target != reflect.TypeFor[**named]() {
		t.Fatalf("a pointer T decodes through *T, codec received %s", rec.target)
	}
	if _, err := decodeInto[named](&rec, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if rec.target != reflect.TypeFor[*named]() {
		t.Fatalf("a value T decodes through *T, codec received %s", rec.target)
	}
}

// The typed helpers and DecodeResponse hand the codec the same destination,
// so JSON null leaves a pointer nil on both paths.
func TestDecodePreservesNullForPointers(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name, body string
		wantNil    bool
	}{
		{"null", "null", true},
		{"zero-valued object", "{}", false},
		{"object", `{"name":"Ada"}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := fixedResponse(200, ContentTypeJSON, tt.body, nil)
			p, resp, err := Get[*named](ctx, client, itemEndpoint)
			if err != nil || (p == nil) != tt.wantNil {
				t.Fatalf("Get[*named] = %v, %v", p, err)
			}
			var explicit *named
			if err := DecodeResponse(resp, JSON, &explicit); err != nil || (explicit == nil) != tt.wantNil {
				t.Fatalf("DecodeResponse = %v, %v", explicit, err)
			}
			if !tt.wantNil && (*p != *explicit) {
				t.Fatalf("Get decoded %+v, DecodeResponse %+v", *p, *explicit)
			}
			defined, _, err := Get[itemPtr](ctx, client, itemEndpoint)
			if err != nil || (defined == nil) != tt.wantNil {
				t.Fatalf("Get[itemPtr] = %v, %v", defined, err)
			}
		})
	}

	type xmlItem struct {
		Name string `xml:"name"`
	}
	p, _, err := Get[*xmlItem](ctx, fixedResponse(200, ContentTypeXML, `<xmlItem><name>Ada</name></xmlItem>`, nil), itemEndpoint)
	if err != nil || p == nil || p.Name != "Ada" {
		t.Fatalf("Get[*xmlItem] = %v, %v", p, err)
	}
}

// brokenBody yields prefix and then fails with err; closed counts Close.
type brokenBody struct {
	prefix *strings.Reader
	err    error
	closed int
}

func (b *brokenBody) Read(p []byte) (int, error) {
	if b.prefix.Len() > 0 {
		return b.prefix.Read(p)
	}
	return 0, b.err
}

func (b *brokenBody) Close() error { b.closed++; return nil }

// A body read can fail after the status and headers arrived. Every decoding
// helper returns that response with the read error, the body closed and
// resp.Body replaying what was received before failing the same way.
func TestDecodeKeepsTheResponseWhenTheBodyFails(t *testing.T) {
	const prefix = `{"name":`
	respond := func(status int, contentType string) (*http.Client, *brokenBody) {
		body := &brokenBody{prefix: strings.NewReader(prefix), err: io.ErrUnexpectedEOF}
		return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			h := http.Header{"Content-Type": {contentType}, "X-Request-Id": {"example-42"}}
			return &http.Response{StatusCode: status, Header: h, Body: body, Request: r}, nil
		})}, body
	}
	check := func(t *testing.T, resp *http.Response, err error, body *brokenBody, status int) {
		t.Helper()
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
		}
		if resp == nil || resp.StatusCode != status || resp.Header.Get("X-Request-Id") != "example-42" {
			t.Fatalf("response %+v", resp)
		}
		if body.closed != 1 {
			t.Fatalf("failed body closed %d times, want 1", body.closed)
		}
		got, readErr := io.ReadAll(resp.Body)
		if string(got) != prefix || !errors.Is(readErr, io.ErrUnexpectedEOF) {
			t.Fatalf("resp.Body reads %q, %v; want the prefix and the same error", got, readErr)
		}
		if err := resp.Body.Close(); err != nil || body.closed != 1 {
			t.Fatalf("Close = %v, underlying closed %d times", err, body.closed)
		}
	}

	t.Run("typed success", func(t *testing.T) {
		client, body := respond(200, ContentTypeJSON)
		_, resp, err := Get[named](context.Background(), client, itemEndpoint)
		check(t, resp, err, body, 200)
	})
	t.Run("typed problem", func(t *testing.T) {
		client, body := respond(422, ContentTypeProblemJSON)
		_, resp, err := Get[named](context.Background(), client, itemEndpoint, WithProblemErrors())
		check(t, resp, err, body, 422)
	})
	t.Run("DecodeResponse", func(t *testing.T) {
		client, body := respond(200, ContentTypeJSON)
		resp, err := client.Get(itemEndpoint)
		if err != nil {
			t.Fatal(err)
		}
		var v named
		check(t, resp, DecodeResponse(resp, JSON, &v), body, 200)
	})
	t.Run("DecodeProblem", func(t *testing.T) {
		client, body := respond(422, ContentTypeProblemJSON)
		resp, err := client.Get(itemEndpoint)
		if err != nil {
			t.Fatal(err)
		}
		_, err = DecodeProblem(resp)
		check(t, resp, err, body, 422)
	})
}

// recordingCodec records the type it was asked to decode into.
type recordingCodec struct{ target reflect.Type }

func (*recordingCodec) ContentType() string            { return "application/x-recording" }
func (*recordingCodec) Encode(any) ([]byte, error)     { return nil, nil }
func (c *recordingCodec) Decode(_ []byte, v any) error { c.target = reflect.TypeOf(v); return nil }

func TestReadAtMost(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		limit   int64
		want    string
		wantErr error
	}{
		{name: "under the limit", in: "{}", limit: 3, want: "{}"},
		{name: "exactly the limit", in: "{}", limit: 2, want: "{}"},
		{name: "one byte over is not a prefix", in: "{}x", limit: 2, wantErr: ErrResponseTooLarge},
		{name: "empty", in: "", limit: 1, want: ""},
		{name: "invalid limit", in: "{}", limit: 0, wantErr: ErrInvalidConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadAtMost(strings.NewReader(tt.in), tt.limit)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || string(got) != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestReadAtMostReportsReadFailure(t *testing.T) {
	broken := errors.New("connection reset")
	_, err := ReadAtMost(io.MultiReader(strings.NewReader("{"), errReader{broken}), 10)
	if !errors.Is(err, broken) {
		t.Fatalf("err = %v, want the read error, not a short success", err)
	}
}
