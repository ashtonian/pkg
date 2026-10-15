//go:build integration

package proto_test

import (
	"context"
	"net/http"
	"net/http/httptest"
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
