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
	if rec.target != reflect.TypeFor[*named]() {
		t.Fatalf("codec received %s, want *httpx.named", rec.target)
	}
	if _, err := decodeInto[named](&rec, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if rec.target != reflect.TypeFor[*named]() {
		t.Fatalf("a value T decodes through *T, codec received %s", rec.target)
	}
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
