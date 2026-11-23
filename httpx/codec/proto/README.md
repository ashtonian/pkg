# httpx/codec/proto

Protocol Buffers codec for [`httpx`](../../README.md), over `google.golang.org/protobuf`. Importing the package registers it for `application/protobuf`, so `httpx.Do[*pb.Message]` decodes protobuf responses by their `Content-Type`; `proto.Codec` encodes request bodies with `httpx.EncodeRequest`.

## Installation

```bash
go get github.com/ashtonian/pkg/httpx/codec/proto
```

## Quick Start

```go
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
```

## Configuration

The codec has no settings of its own. These choose where and how it is used:

| Selection | Type | Default | Description |
|---|---|---|---|
| `Codec` | `httpx.Codec` | Registered on import for `application/protobuf` | Encodes and decodes with `proto.Marshal` and `proto.Unmarshal` defaults |
| `httpx.WithResponseCodecs(codecs...)` | decode option | Every registered codec | Limits one call's `Accept` and response decoders; give endpoints that decode plain Go types `httpx.WithResponseCodecs(httpx.JSON)` |
| `httpx.WithMaxResponseBytes(n)` | `int64` decode option | 10 MiB | Bounds a typed helper's response read; direct `Codec.Decode` calls receive the caller's bytes |

## API / Interfaces

- `Codec httpx.Codec` — `ContentType()` is `httpx.ContentTypeProtobuf`. `Encode` takes a `proto.Message`. `Decode` takes a `proto.Message` or a pointer to a message pointer (`**pb.Message`, or a pointer to a defined pointer type), allocating the message when the pointer is nil; anything else fails.
- Importing the package calls `httpx.RegisterCodec(Codec)`.

## Caveats

- Only 2xx responses are decoded. Check `resp.StatusCode`, and close every non-nil `resp.Body`, including when `err` reports a decoding failure.
- Decode with a pointer message type (`httpx.Do[*pb.Message]`). The helpers decode a `T` through a `*T`, so the codec receives a `**pb.Message` and allocates the message; `httpx.DecodeResponse(resp, proto.Codec, &msg)` with `var msg *pb.Message` decodes the same way. Protobuf has no null: an empty body is a zero-valued message, never a nil pointer.
- Importing the module advertises `application/protobuf` on every typed request in the process. Give endpoints that decode plain Go types their own selection with `httpx.WithResponseCodecs(httpx.JSON)`, so an origin preferring protobuf doesn't answer them in it.

## Build / Run / Test

The quick start above is `example_test.go` verbatim; `readme_test.go` fails
the unit lane when the two differ. From this module:

```sh
go test -tags=unit -race .
go test -tags=integration -race .
```

Untagged `go test` runs nothing. The repository's `make httpx-release-check`
tests this module's archive against the `httpx` version its `go.mod`
requires, outside the workspace.

## Operations

The codec holds no connections, goroutines, health endpoints or metrics.
Every failure returns to the caller:

- `httpx.ErrNoCodec` from a typed helper: the response's `Content-Type` is
  not `application/protobuf` (or is malformed), or the call's
  `WithResponseCodecs` selection leaves protobuf out. Check the header the
  origin sent and the `Accept` on `resp.Request`.
- `httpx/proto: T is neither a proto.Message nor a pointer to one`: the
  destination is a message value (`Do[pb.Message]`) or not a message. Use
  `Do[*pb.Message]`.
- A `proto.Unmarshal` error such as `cannot parse invalid wire-format data`:
  the body is not this message's wire format. Typical causes are a truncated
  body, a different message type, or an HTML or JSON error page labelled
  `application/protobuf`. Inspect the bytes with `httpx.ReadAtMost` under
  the application's privacy policy.
- `httpx.ErrResponseTooLarge`: the body exceeded the typed helper's bound.
  Raise it deliberately with `WithMaxResponseBytes`, or stream.

## Security

Decoding uses `proto.Unmarshal` defaults. Unknown fields are kept on the
message and sent again if it is re-marshaled, so do not forward a decoded
message to a third party expecting only its known fields to travel. Nesting
is limited to protobuf-go's default recursion depth (10,000). Protobuf
checks wire format, not meaning: validate decoded messages before acting
on them. Typed helpers stop reading at 10 MiB by default; a direct
`Codec.Decode` call trusts the slice its caller supplies, so bound it first.

## External Dependencies

- `github.com/ashtonian/pkg/httpx` — codec interface, registration and typed helpers, required at a published version; the repository workspace supplies local source during development.
- `google.golang.org/protobuf` — protobuf wire encoding. Versions are pinned in `go.mod`.

## Code Map

| File | Responsibility |
|---|---|
| `proto.go` | `Codec`, decoding through message pointers, registration on import |
| `example_test.go` | The README quick start, compiled and run |
| `readme_test.go` | Fails when the README quick start and `example_test.go` differ |
| `proto_test.go`, `proto_integration_test.go` | Unit tests and loopback-server tests of the typed helpers |

## References

- Parent: [`httpx`](../../README.md)
- Upstream: [protobuf-go](https://pkg.go.dev/google.golang.org/protobuf)
