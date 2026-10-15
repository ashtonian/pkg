//go:build unit

package proto_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/ashtonian/pkg/httpx"
	"github.com/ashtonian/pkg/httpx/codec/proto"
)

func Example() {
	// A server that reads and answers Protocol Buffers.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := httpx.ReadAtMost(r.Body, 1<<20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		in := new(wrapperspb.StringValue)
		if err := proto.Codec.Decode(data, in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := proto.Codec.Encode(wrapperspb.String("hello, " + in.GetValue()))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", proto.Codec.ContentType())
		// The local client reads the whole response; write failures end this handler.
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	// Encode the body as protobuf; httpx.Do decodes the response into the
	// message type named by its type parameter.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := httpx.EncodeRequest(req, proto.Codec, wrapperspb.String("Ada")); err != nil {
		fmt.Println(err)
		return
	}
	reply, resp, err := httpx.Do[*wrapperspb.StringValue](srv.Client(), req)
	if resp != nil {
		defer resp.Body.Close() // Also close bodies returned alongside decode errors.
	}
	if err != nil {
		fmt.Println(err)
		return
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Println("unexpected status:", resp.Status)
		return
	}
	fmt.Println(reply.GetValue())
	// Output: hello, Ada
}
