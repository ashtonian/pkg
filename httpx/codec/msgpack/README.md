# httpx/codec/msgpack

MessagePack codecs for [`httpx`](../../README.md), using `github.com/vmihailenco/msgpack/v5`. The default `Codec` carries the API's JSON data model in MessagePack; `NativeCodec` explicitly selects native MessagePack semantics. Importing the package registers the default for `application/msgpack` typed responses, and `httpx.EncodeRequest` accepts either codec for requests.

## Installation

```bash
go get github.com/ashtonian/pkg/httpx/codec/msgpack
```

Requires Go 1.26 or later.

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
	ID   int    `json:"id"`
	Name string `json:"name"`
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

## Configuration

| Selection | Type | Default | Description |
|---|---|---|---|
| `Codec` | `httpx.Codec` | Registered on import | JSON tags, field selection, omission rules and JSON/text marshal methods |
| `NativeCodec` | `httpx.Codec` | Explicit opt-in | Native MessagePack tags, binary values, extensions and marshal methods |
| `httpx.WithMaxResponseBytes(n)` | `int64` decode option | 10 MiB | Bounds a typed helper's response read; direct codec calls receive the caller's byte slice |

To use the native model for all typed MessagePack responses, call
`httpx.RegisterCodec(msgpack.NativeCodec)` at startup. Both codecs use
`application/msgpack`; endpoints must agree on the data model. Direct calls
to `Codec` or `NativeCodec` do not depend on which one is registered.

## Representation contract

`Codec` uses `encoding/json` for struct selection and typed values. `json:"-"`,
`json:"-,"`, embedded fields, `omitempty`, `omitzero`, pointers, maps and slices
have the same meaning as in JSON. `msgpack` tags do not override that model.
Byte slices become base64 strings; UUIDs, timestamps and other custom JSON/text
values use their JSON form. Decoding invokes the target's JSON unmarshaler.

Signed and unsigned 64-bit integers retain their full precision. JSON integer
literals outside those ranges are rejected; other JSON numbers use finite
float64. Nonfinite numbers, non-string map keys, invalid UTF-8, unsupported
extensions, malformed/trailing data and nesting beyond JSON's 10,000-level
bound are rejected. Native binary and timestamp values decode to their JSON
string forms; native extension callbacks are not invoked by the default codec.

Encoding and decoding materialize a JSON document and intermediate values.
They buffer complete representations and are not streaming operations.
`NativeCodec` delegates directly to the native library and supports its tags,
binary/time values and custom extensions without JSON transcoding.

## API / Interfaces

- `Codec httpx.Codec` — JSON-model `Encode`, `Decode` and `ContentType`.
- `NativeCodec httpx.Codec` — native `msgpack.Marshal` / `msgpack.Unmarshal` and the same content type.
- Importing the package calls `httpx.RegisterCodec(Codec)`.

## Build / Run / Test

The quick start above is mirrored in `example_test.go`. From this module:

```sh
go test -tags=unit -race .
go test -tags=integration -race .
```

The repository's `make httpx-release-check` also tests downloaded module
archives and examples outside the workspace.

## Operations

The codecs hold no connections, goroutines, health endpoints or metrics.
Encoding/decoding failures return to the caller. For mismatched fields, check
the selected JSON/native model and both endpoints' tags. For malformed or
unsupported values, inspect the representation under the application's
privacy policy. For `ErrResponseTooLarge`, deliberately change the typed
helper's bound or use a streaming protocol.

Typed helpers decode successful representations by content type. Check
`resp.StatusCode` and close every non-nil response body, including after a
decode error; see [body ownership](../../docs/requests.md#optional-typed-calls).

## Security

The default follows JSON's field visibility and custom marshaler contract.
`NativeCodec` does **not** enforce `json:"-"`: a native peer must use appropriate
`msgpack` tags and its own exposure policy. Choose the model explicitly before
sharing API structs with a native protocol. Apply response-size limits before
decoding untrusted bodies; direct codec calls impose no separate byte limit.

## External Dependencies

- `github.com/ashtonian/pkg/httpx` — codec interface, registration and typed helpers, required at a published version; the repository workspace supplies local source during development.
- `github.com/vmihailenco/msgpack/v5` — MessagePack wire encoding and native codec behavior. Versions are pinned in `go.mod`.

## Code Map

| File | Responsibility |
|---|---|
| `msgpack.go` | `Codec`, `NativeCodec` and default registration |
| `jsonmodel.go` | JSON-model transcoding and wire validation |

## References

- Parent: [`httpx`](../../README.md)
- Upstream: [vmihailenco/msgpack](https://github.com/vmihailenco/msgpack)
