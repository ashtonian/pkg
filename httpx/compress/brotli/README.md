# httpx/compress/brotli

Brotli compression for [`httpx`](../../README.md), over `github.com/andybalholm/brotli`. Importing the package registers brotli with the compression layer: `httpx.WithCompression()` then offers `br` in `Accept-Encoding` and decodes brotli responses.

## Installation

```bash
go get github.com/ashtonian/pkg/httpx/compress/brotli
```

## Quick Start

```go
//go:build unit

package brotli_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/ashtonian/pkg/httpx"
	"github.com/ashtonian/pkg/httpx/compress/brotli"
)

func Example() {
	// A server that answers in brotli when the client accepts it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), httpx.EncodingBrotli) {
			http.Error(w, "brotli not offered", http.StatusNotAcceptable)
			return
		}
		var buf bytes.Buffer
		body, err := brotli.NewWithLevel(6).Compress([]byte("hello over brotli"), &buf)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Encoding", httpx.EncodingBrotli)
		// The local client reads the whole response; write failures end this handler.
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	// Importing the package registers brotli: the compression layer offers
	// it in Accept-Encoding and decodes it.
	client, err := httpx.NewClient(httpx.WithCompression(), httpx.WithTimeout(5*time.Second))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get(srv.URL)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Println("unexpected status:", resp.Status)
		return
	}
	body, err := httpx.ReadAtMost(resp.Body, 1<<20)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(resp.StatusCode, string(body))
	// Output: 200 hello over brotli
}
```

## Configuration

| Selection | Type | Default | Description |
|---|---|---|---|
| Import | — | Registers `br` | `httpx.RegisterCompressor(httpx.EncodingBrotli, …)` with a level-4 compressor |
| `httpx.WithBrotliLevel(level)` | `int` compression option | `DefaultLevel` (4) | This transport's brotli level, 0 (fastest) to 11 (smallest); out of range, or brotli not imported, is `httpx.ErrInvalidConfig` |
| `New()` | `*Compressor` | `DefaultLevel` (4) | A compressor at the default level, for `httpx.WithCompressor` or direct use |
| `NewWithLevel(level)` | `int` | — | A compressor at `level`; the upstream encoder clamps a level outside 0–11 to that range |

The level affects only `Compress`. Decoding a response does not depend on
it.

## API / Interfaces

- `Compressor` implements `httpx.Compressor`: `Compress(data, buf)`, `Decompress(r)`, and `WithLevel(level)`, which `httpx.WithBrotliLevel` calls.
- `DefaultLevel` is 4.
- Importing the package calls `httpx.RegisterCompressor(httpx.EncodingBrotli, …)`.

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

The package holds no connections, goroutines, health endpoints or metrics.
Each `Compressor` pools its encoders, so repeated `Compress` calls reuse
encoder state; the pool empties under garbage collection.

- `br` missing from `Accept-Encoding`: the binary does not import this
  package, or the transport's `WithEncodings` list leaves `br` out.
- A read error beginning `brotli:` from a response body: the body is not a
  valid brotli stream. `CompressionTransport` decodes lazily, so this
  surfaces on the first body read, not from `RoundTrip`. The layer decodes
  every coding it supports, even when the caller set `Accept-Encoding`, so
  to capture the encoded bytes repeat the request through a transport
  without it.
- `httpx.ErrInvalidConfig` from `NewCompressionTransport`:
  `WithBrotliLevel` was given a level outside 0–11, or this package is not
  imported.

## Security

Brotli compresses redundant data by large ratios, so a small response can
decode to a very large body. Typed helpers apply `WithMaxResponseBytes`
(10 MiB by default) after decompression; a raw `client.Do` read needs its
own bound, such as `httpx.ReadAtMost`. The decoder's ring buffer is sized
by the stream's declared window, at most 16 MiB under RFC 7932; streams
using the non-standard large-window extension are rejected.

## External Dependencies

- `github.com/ashtonian/pkg/httpx` — compressor interface, registration and the compression transport, required at a published version; the repository workspace supplies local source during development.
- `github.com/andybalholm/brotli` — pure-Go brotli encoder and decoder. Versions are pinned in `go.mod`.

## Code Map

| File | Responsibility |
|---|---|
| `brotli.go` | `Compressor`, its encoder pool, and registration on import |
| `example_test.go` | The README quick start, compiled and run |
| `readme_test.go` | Fails when the README quick start and `example_test.go` differ |
| `brotli_test.go`, `brotli_integration_test.go` | Unit tests and loopback-server tests through the compression transport |

## References

- Parent: [`httpx`](../../README.md)
- Upstream: [andybalholm/brotli](https://github.com/andybalholm/brotli)
- [RFC 7932](https://www.rfc-editor.org/rfc/rfc7932): Brotli compressed data format
