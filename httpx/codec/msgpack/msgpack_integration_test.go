//go:build integration

package msgpack_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ashtonian/pkg/httpx"
	"github.com/ashtonian/pkg/httpx/codec/msgpack"
	wire "github.com/vmihailenco/msgpack/v5"
)

func TestMessagePackHTTPInteroperability(t *testing.T) {
	type message struct {
		ID   int    `msgpack:"id"`
		Name string `msgpack:"name"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != httpx.ContentTypeMsgpack {
			t.Error("request content type is not MessagePack")
		}
		var input message
		if err := wire.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
			t.Error(err)
			http.Error(w, "decode request", 400)
			return
		}
		if input.ID != 7 || input.Name != "Ada" {
			t.Errorf("wire request: %+v", input)
		}
		w.Header().Set("Content-Type", httpx.ContentTypeMsgpack)
		if err := wire.NewEncoder(w).Encode(message{ID: input.ID, Name: input.Name + " saved"}); err != nil {
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
	if err := httpx.EncodeRequest(req, msgpack.Codec, message{ID: 7, Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	got, resp, err := httpx.Do[message](srv.Client(), req)
	if resp != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || got.ID != 7 || got.Name != "Ada saved" {
		t.Fatalf("status=%d message=%+v", resp.StatusCode, got)
	}
}
