# httpx/codec/msgpack

MessagePack codec for [`httpx`](../../README.md), over `github.com/vmihailenco/msgpack/v5`. Importing the package registers it for `application/msgpack`, so `httpx.Get`, `httpx.Do` and the other decoding helpers decode MessagePack responses by their `Content-Type`; `msgpack.Codec` encodes request bodies with `httpx.EncodeRequest`.

## Installation

```bash
go get github.com/ashtonian/pkg/httpx/codec/msgpack
```

## Quick Start

```go
//go:build unit

package msgpack_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/ashtonian/pkg/httpx"
	"github.com/ashtonian/pkg/httpx/codec/msgpack"
)

type User struct {
	ID   int    `msgpack:"id"`
	Name string `msgpack:"name"`
}

func Example() {
	// A server that reads and answers MessagePack.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := httpx.ReadAtMost(r.Body, 1<<20)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var in User
		if err := msgpack.Codec.Decode(data, &in); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := msgpack.Codec.Encode(User{ID: in.ID, Name: in.Name + " (saved)"})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", msgpack.Codec.ContentType())
		// The local client reads the whole response; write failures end this handler.
		_, _ = w.Write(out)
	}))
	defer srv.Close()

	// Encode the body as MessagePack; httpx.Do decodes the response by its
	// Content-Type, which importing the package registered.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := httpx.EncodeRequest(req, msgpack.Codec, User{ID: 1, Name: "Ada"}); err != nil {
		fmt.Println(err)
		return
	}
	user, resp, err := httpx.Do[User](srv.Client(), req)
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
	fmt.Println(resp.StatusCode, user.Name)
	// Output: 200 Ada (saved)
}
```

## API

- `Codec httpx.Codec` — `ContentType()` is `httpx.ContentTypeMsgpack`; `Encode` / `Decode` are `msgpack.Marshal` / `msgpack.Unmarshal`.
- Importing the package calls `httpx.RegisterCodec(Codec)`.

## Caveats

- Only 2xx responses are decoded. Check `resp.StatusCode`, and close every non-nil `resp.Body`, including when `err` reports a decoding failure.
- Field names come from `msgpack` struct tags, not `json` tags.
- The module requires the parent `httpx` module at a published version; inside this repository the Go workspace supplies it.

## References

- Parent: [`httpx`](../../README.md)
- Upstream: [vmihailenco/msgpack](https://github.com/vmihailenco/msgpack)
