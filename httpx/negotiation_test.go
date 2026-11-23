//go:build unit

package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// mediaCodec is the JSON codec under another content type; decoded counts
// its Decode calls.
type mediaCodec struct {
	contentType string
	decoded     *atomic.Int32
}

func (c mediaCodec) ContentType() string        { return c.contentType }
func (mediaCodec) Encode(v any) ([]byte, error) { return json.Marshal(v) }
func (c mediaCodec) Decode(data []byte, v any) error {
	if c.decoded != nil {
		c.decoded.Add(1)
	}
	return json.Unmarshal(data, v)
}

// messageCodec stands in for a protobuf codec: it decodes only into a
// *message, the way protobuf decodes only into a proto.Message.
type messageCodec struct{}

type message struct{ Text string }

const contentTypeMessage = "application/x-message"

func (messageCodec) ContentType() string        { return contentTypeMessage }
func (messageCodec) Encode(any) ([]byte, error) { return nil, errors.ErrUnsupported }
func (messageCodec) Decode(data []byte, v any) error {
	target, ok := v.(**message)
	if !ok {
		return fmt.Errorf("messageCodec: %T is not a **message", v)
	}
	*target = &message{Text: string(data)}
	return nil
}

// dualFormatOrigin serves an item as a message whenever the request accepts
// that media type, and as JSON otherwise, the way an origin preferring
// protobuf negotiates. It records each Accept it receives.
func dualFormatOrigin(accepts *[]string, mu *sync.Mutex) *http.Client {
	return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		accept := r.Header.Get("Accept")
		if accepts != nil {
			mu.Lock()
			*accepts = append(*accepts, accept)
			mu.Unlock()
		}
		contentType, body := ContentTypeJSON, `{"name":"item"}`
		if strings.Contains(accept, contentTypeMessage) {
			contentType, body = contentTypeMessage, "item"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
}

func TestDoMatchesResponseMediaTypesCaseInsensitively(t *testing.T) {
	isolateCodecs(t)
	var custom atomic.Int32
	RegisterCodec(mediaCodec{contentType: "Application/VND.Acme+JSON", decoded: &custom})

	for _, tt := range []struct {
		name, contentType string
		wantCustom        bool
		wantErr           error
	}{
		{name: "canonical", contentType: "application/json"},
		{name: "mixed case with a parameter", contentType: "Application/JSON; charset=utf-8"},
		{name: "upper case without a space", contentType: "APPLICATION/JSON;charset=UTF-8"},
		{name: "trailing semicolon", contentType: "application/json;"},
		{name: "malformed parameter", contentType: "application/json; charset", wantErr: ErrNoCodec},
		{name: "conflicting parameter", contentType: "application/json; charset=utf-8; charset=latin1", wantErr: ErrNoCodec},
		{name: "custom type in another case", contentType: "application/vnd.ACME+json", wantCustom: true},
		{name: "unregistered", contentType: "text/csv", wantErr: ErrNoCodec},
		{name: "no subtype", contentType: "json", wantErr: ErrNoCodec},
		{name: "not a media type", contentType: "application/json/extra", wantErr: ErrNoCodec},
	} {
		t.Run(tt.name, func(t *testing.T) {
			custom.Store(0)
			body := `{"name":"ok"}`
			v, resp, err := Get[named](context.Background(), fixedResponse(200, tt.contentType, body, nil), itemEndpoint)
			if !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if resp == nil {
				t.Fatal("no response")
			}
			defer resp.Body.Close()
			if tt.wantErr != nil {
				if got, _ := io.ReadAll(resp.Body); string(got) != body {
					t.Fatalf("an undecoded body reads %q, want it intact", got)
				}
				return
			}
			if v.Name != "ok" {
				t.Fatalf("decoded %+v", v)
			}
			if got := custom.Load() == 1; got != tt.wantCustom {
				t.Fatalf("custom codec used = %v, want %v", got, tt.wantCustom)
			}
		})
	}
}

func TestRegisterCodecKeysEquivalentSpellingsOnce(t *testing.T) {
	isolateCodecs(t)
	first, second := mediaCodec{contentType: "application/x-test"}, mediaCodec{contentType: "Application/X-Test; charset=utf-8"}
	RegisterCodec(first)
	RegisterCodec(second)

	if got, ok := GetCodec("APPLICATION/x-TEST;q=1"); !ok || got != Codec(second) {
		t.Fatalf("GetCodec = %v, %v; want the later registration", got, ok)
	}
	if !HasCodec("application/X-test") {
		t.Fatal("HasCodec misses another spelling")
	}
	n := 0
	for _, c := range RegisteredCodecs() {
		if strings.EqualFold(strings.SplitN(c.ContentType(), ";", 2)[0], "application/x-test") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d registrations of application/x-test, want 1", n)
	}
	accept := codecs.acceptValue()
	if strings.Count(accept, "application/x-test") != 1 || strings.Contains(accept, "X-Test") || strings.Contains(accept, "charset") {
		t.Fatalf("Accept = %q, want application/x-test listed once, canonical and without parameters", accept)
	}
	for _, malformed := range []string{"json", "application/x-test; charset"} {
		if _, ok := GetCodec(malformed); ok {
			t.Fatalf("GetCodec(%q) found a codec", malformed)
		}
	}

	for _, contentType := range []string{"", "json", "application/", "application/*", "*/*", "application/json; charset", "application/json/extra"} {
		t.Run(contentType, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("RegisterCodec(%q) did not panic", contentType)
				}
			}()
			RegisterCodec(mediaCodec{contentType: contentType})
		})
	}
}

func TestWithResponseCodecs(t *testing.T) {
	isolateCodecs(t)
	RegisterCodec(messageCodec{})
	ctx := context.Background()

	t.Run("registry default advertises a codec the destination cannot use", func(t *testing.T) {
		_, resp, err := Get[named](ctx, dualFormatOrigin(nil, nil), itemEndpoint)
		if err == nil {
			t.Fatal("a plain DTO decoded a message")
		}
		resp.Body.Close()
	})

	t.Run("a selection advertises and decodes only its codecs", func(t *testing.T) {
		var accepts []string
		var mu sync.Mutex
		client := dualFormatOrigin(&accepts, &mu)
		v, resp, err := Get[named](ctx, client, itemEndpoint, WithResponseCodecs(JSON))
		if err != nil || v.Name != "item" {
			t.Fatalf("Get = %+v, %v", v, err)
		}
		resp.Body.Close()
		msg, resp, err := Get[*message](ctx, client, itemEndpoint)
		if err != nil || msg == nil || msg.Text != "item" {
			t.Fatalf("Get[*message] = %+v, %v", msg, err)
		}
		resp.Body.Close()
		if accepts[0] != ContentTypeJSON || !strings.Contains(accepts[1], contentTypeMessage) {
			t.Fatalf("Accept values %q", accepts)
		}
	})

	t.Run("a response outside the selection is ErrNoCodec", func(t *testing.T) {
		_, resp, err := Get[named](ctx, fixedResponse(200, ContentTypeJSON, `{}`, nil), itemEndpoint, WithResponseCodecs(XML))
		if !errors.Is(err, ErrNoCodec) {
			t.Fatalf("err = %v, want ErrNoCodec", err)
		}
		resp.Body.Close()
	})

	t.Run("an explicit Accept is sent unchanged and the selection still decides decoding", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, itemEndpoint, nil)
		req.Header.Set("Accept", contentTypeMessage)
		_, resp, err := Do[named](dualFormatOrigin(nil, nil), req, WithResponseCodecs(JSON))
		if !errors.Is(err, ErrNoCodec) || resp.Request.Header.Get("Accept") != contentTypeMessage {
			t.Fatalf("err = %v, Accept %q", err, resp.Request.Header.Get("Accept"))
		}
		resp.Body.Close()
	})

	for _, tt := range []struct {
		name string
		opts []DecodeOption
		want string
	}{
		{"selection order", []DecodeOption{WithResponseCodecs(XML, JSON)}, "application/xml, application/json"},
		{"repeated options add", []DecodeOption{WithResponseCodecs(XML), WithResponseCodecs(JSON)}, "application/xml, application/json"},
		{"problem types follow, each once", []DecodeOption{
			WithResponseCodecs(JSON), WithProblemErrors(), WithProblemMediaTypes(ContentTypeJSON, "application/vnd.acme.error+json"),
		}, "application/json, application/problem+json, application/vnd.acme.error+json"},
		{"registry with problem types", []DecodeOption{WithProblemErrors()}, codecs.acceptValue() + ", application/problem+json"},
		{"registry alone", nil, codecs.acceptValue()},
	} {
		t.Run("Accept: "+tt.name, func(t *testing.T) {
			var accepts []string
			var mu sync.Mutex
			_, resp, _ := Get[named](ctx, dualFormatOrigin(&accepts, &mu), itemEndpoint, tt.opts...)
			resp.Body.Close()
			if accepts[0] != tt.want {
				t.Fatalf("Accept = %q, want %q", accepts[0], tt.want)
			}
		})
	}

	for _, tt := range []struct {
		name string
		opt  DecodeOption
	}{
		{"empty", WithResponseCodecs()},
		{"nil codec", WithResponseCodecs(JSON, nil)},
		{"one media type twice", WithResponseCodecs(JSON, mediaCodec{contentType: "Application/JSON; charset=utf-8"})},
		{"no subtype", WithResponseCodecs(mediaCodec{contentType: "json"})},
		{"media range", WithResponseCodecs(mediaCodec{contentType: "application/*"})},
	} {
		t.Run("invalid: "+tt.name, func(t *testing.T) {
			var sent atomic.Int32
			body := &problemBody{Reader: strings.NewReader("payload")}
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, itemEndpoint, body)
			_, resp, err := Do[named](fixedResponse(200, ContentTypeJSON, `{}`, &sent), req, tt.opt)
			if !errors.Is(err, ErrInvalidConfig) || resp != nil || sent.Load() != 0 || body.closed != 1 {
				t.Fatalf("err = %v, resp %v, sent %d, body closed %d", err, resp, sent.Load(), body.closed)
			}
		})
	}
}

// Two consumers share the process registry; one selects JSON for a plain
// DTO while the other decodes the message the origin prefers.
func TestWithResponseCodecsConcurrentConsumers(t *testing.T) {
	isolateCodecs(t)
	RegisterCodec(messageCodec{})
	client := dualFormatOrigin(nil, nil)
	ctx := context.Background()
	jsonOnly := WithResponseCodecs(JSON)

	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for range 100 {
		wg.Go(func() {
			v, resp, err := Get[named](ctx, client, itemEndpoint, jsonOnly)
			if err == nil && v.Name != "item" {
				err = fmt.Errorf("DTO %+v", v)
			}
			if resp != nil {
				resp.Body.Close()
			}
			errs <- err
		})
		wg.Go(func() {
			msg, resp, err := Get[*message](ctx, client, itemEndpoint)
			if err == nil && (msg == nil || msg.Text != "item") {
				err = fmt.Errorf("message %+v", msg)
			}
			if resp != nil {
				resp.Body.Close()
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// problemAwareOrigin answers 422 problem details only to a request that
// accepts them, and 204 otherwise.
func problemAwareOrigin() *http.Client {
	return &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.Header.Get("Accept"), ContentTypeProblemJSON) {
			return &http.Response{StatusCode: 422, Header: http.Header{"Content-Type": {ContentTypeProblemJSON}}, Body: io.NopCloser(strings.NewReader(`{"title":"refused"}`)), Request: r}, nil
		}
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
	})}
}

func TestDoNegotiatesOnACopyOfTheRequest(t *testing.T) {
	client := problemAwareOrigin()
	call := func(t *testing.T, req *http.Request, problems bool) (status int, problem bool) {
		t.Helper()
		var opts []DecodeOption
		if problems {
			opts = append(opts, WithProblemErrors())
		}
		_, resp, err := Do[named](client, req, opts...)
		var p *Problem
		if err != nil && !errors.As(err, &p) {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, p != nil
	}
	for _, order := range [][]bool{{false, true}, {true, false}} {
		t.Run(fmt.Sprintf("problem errors %v", order), func(t *testing.T) {
			reused, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, itemEndpoint, nil)
			for _, problems := range order {
				fresh, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, itemEndpoint, nil)
				wantStatus, wantProblem := call(t, fresh, problems)
				status, problem := call(t, reused, problems)
				if status != wantStatus || problem != wantProblem {
					t.Fatalf("reused request: %d problem=%v; a fresh one: %d problem=%v", status, problem, wantStatus, wantProblem)
				}
				if wantProblem != problems || len(reused.Header) != 0 {
					t.Fatalf("problem=%v for problem errors %v; caller's headers now %v", wantProblem, problems, reused.Header)
				}
			}
		})
	}

	t.Run("a nil Header map", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, itemEndpoint, nil)
		req.Header = nil
		if status, problem := call(t, req, true); status != 422 || !problem || req.Header != nil {
			t.Fatalf("status %d problem=%v header %v", status, problem, req.Header)
		}
	})

	t.Run("an explicit Accept is the caller's", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, itemEndpoint, nil)
		req.Header.Set("Accept", ContentTypeXML)
		if status, problem := call(t, req, true); status != 204 || problem || req.Header.Get("Accept") != ContentTypeXML {
			t.Fatalf("status %d problem=%v Accept %q", status, problem, req.Header.Get("Accept"))
		}
	})

	t.Run("the copy carries the caller's request", func(t *testing.T) {
		type key struct{}
		reqCtx := context.WithValue(context.Background(), key{}, "value")
		req, _ := http.NewRequestWithContext(reqCtx, http.MethodPost, itemEndpoint, bytes.NewReader([]byte("payload")))
		req.Header.Set("X-Caller", "kept")
		var seen *http.Request
		var body []byte
		client := &http.Client{Transport: stubRoundTripper(func(r *http.Request) (*http.Response, error) {
			seen = r
			body, _ = io.ReadAll(r.Body)
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody, Request: r}, nil
		})}
		if _, _, err := Do[named](client, req); err != nil {
			t.Fatal(err)
		}
		switch {
		case seen == req:
			t.Fatal("Do sent the caller's request")
		case seen.Context().Value(key{}) != "value", seen.GetBody == nil, string(body) != "payload":
			t.Fatalf("context %v, GetBody %v, body %q", seen.Context().Value(key{}), seen.GetBody != nil, body)
		case seen.Header.Get("X-Caller") != "kept", seen.Header.Get("Accept") == "":
			t.Fatalf("sent headers %v", seen.Header)
		case len(req.Header) != 1 || req.Header.Get("Accept") != "":
			t.Fatalf("caller's headers now %v", req.Header)
		}
	})
}
