//go:build integration

package proto_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
	"github.com/ashtonian/pkg/httpx/codec/proto"
	wire "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestProtocolBuffersHTTPInteroperability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != httpx.ContentTypeProtobuf {
			t.Error("request content type is not protobuf")
		}
		body, err := httpx.ReadAtMost(r.Body, 1<<20)
		if err != nil {
			t.Error(err)
			http.Error(w, "read request", 400)
			return
		}
		var input wrapperspb.StringValue
		if err := wire.Unmarshal(body, &input); err != nil {
			t.Error(err)
			http.Error(w, "decode request", 400)
			return
		}
		if input.GetValue() != "Ada" {
			t.Errorf("wire request = %q", input.GetValue())
		}
		output, err := wire.Marshal(wrapperspb.String("hello, " + input.GetValue()))
		if err != nil {
			t.Error(err)
			http.Error(w, "encode response", 500)
			return
		}
		w.Header().Set("Content-Type", httpx.ContentTypeProtobuf)
		if _, err := w.Write(output); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := httpx.EncodeRequest(req, proto.Codec, wrapperspb.String("Ada")); err != nil {
		t.Fatal(err)
	}
	got, resp, err := httpx.Do[*wrapperspb.StringValue](srv.Client(), req)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || got.GetValue() != "hello, Ada" {
		t.Fatalf("status=%d message=%v", resp.StatusCode, got)
	}
}

// An origin that serves protobuf whenever a request accepts it. Importing
// this module advertises protobuf on every typed request; a consumer of
// plain DTOs selects JSON for its endpoint while another decodes messages,
// concurrently and through the same process registry.
func TestEndpointsSelectTheirResponseCodecs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.Header.Get("Accept"), httpx.ContentTypeProtobuf) {
			output, err := wire.Marshal(wrapperspb.String("item"))
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Type", httpx.ContentTypeProtobuf)
			_, _ = w.Write(output)
			return
		}
		w.Header().Set("Content-Type", httpx.ContentTypeJSON)
		_, _ = w.Write([]byte(`{"name":"item"}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type item struct {
		Name string `json:"name"`
	}

	_, resp, err := httpx.Get[item](ctx, srv.Client(), srv.URL)
	closeErr := closeBody(resp)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if err == nil {
		t.Fatal("a plain DTO decoded the protobuf the registry advertised")
	}

	jsonOnly := httpx.WithResponseCodecs(httpx.JSON)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for range 20 {
		wg.Go(func() {
			v, resp, err := httpx.Get[item](ctx, srv.Client(), srv.URL, jsonOnly)
			if err == nil && v.Name != "item" {
				err = fmt.Errorf("DTO %+v", v)
			}
			errs <- errors.Join(err, closeBody(resp))
		})
		wg.Go(func() {
			msg, resp, err := httpx.Get[*wrapperspb.StringValue](ctx, srv.Client(), srv.URL)
			if err == nil && msg.GetValue() != "item" {
				err = fmt.Errorf("message %v", msg)
			}
			errs <- errors.Join(err, closeBody(resp))
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

// closeBody closes the body of a response a typed helper returned, which it
// returns alongside a decode error too.
func closeBody(resp *http.Response) error {
	if resp == nil {
		return nil
	}
	return resp.Body.Close()
}
