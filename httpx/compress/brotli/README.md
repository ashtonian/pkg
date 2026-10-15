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

| Function | Type | Default | Description |
|----------|------|---------|-------------|
| `New()` | `*Compressor` | level `DefaultLevel` (4) | Compressor at the default level |
| `NewWithLevel(level)` | `int` | — | Compressor at `level`, 0 (fastest) to 11 (smallest) |

## API

- `Compressor` implements `httpx.Compressor`: `Compress(data, buf)`, `Decompress(r)`, `WithLevel(level)`.
- Importing the package calls `httpx.RegisterCompressor(httpx.EncodingBrotli, …)`.

## Caveats

- The module requires the parent `httpx` module at a published version; inside this repository the Go workspace supplies it.

## References

- Parent: [`httpx`](../../README.md)
- Upstream: [andybalholm/brotli](https://github.com/andybalholm/brotli)
