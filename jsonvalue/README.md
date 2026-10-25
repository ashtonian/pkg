# jsonvalue

## Overview

Decode JSON into a fresh Go value, then replace the destination only after decoding succeeds. Reused maps retain no old keys, omitted struct fields return to their zero values, and decode errors leave the destination unchanged. `UnmarshalPresent` additionally rejects a nil Go representation for callers that distinguish an absent value from a stored document.

## Quick start

Requires Go 1.27 or later. Install with `go get github.com/ashtonian/pkg/jsonvalue@latest`.

```go
package main

import (
    "encoding/json"
    "fmt"

    "github.com/ashtonian/pkg/jsonvalue"
)

func main() {
    value := map[string]int{"old": 1}
    if err := jsonvalue.UnmarshalFresh([]byte(`{"new":2}`), &value); err != nil {
        panic(err)
    }
    fmt.Println(value)
    fmt.Println(jsonvalue.UnmarshalPresent([]byte("null"), &value) != nil)
    fmt.Println(value)

    var raw json.RawMessage
    if err := jsonvalue.UnmarshalPresent([]byte("null"), &raw); err != nil {
        panic(err)
    }
    fmt.Println(string(raw))
}
```

```text
map[new:2]
true
map[new:2]
null
```

## API / interfaces

| Function | Contract |
|---|---|
| `UnmarshalFresh(data []byte, v any) error` | Decode into a fresh value of the pointed-to type, then replace it on success. |
| `UnmarshalPresent(data []byte, v any) error` | Apply the same replacement, rejecting a resulting nil map, slice, pointer or interface. |

Both require a non-nil pointer and return `*json.InvalidUnmarshalError` for an invalid destination. Decoding uses `encoding/json`; syntax, type and custom decoder errors retain their original identity. Custom unmarshal methods receive fresh zero-valued receivers. External effects performed by those methods cannot be rolled back.

Presence means a non-nil **Go representation**, not a JSON token restriction. Empty objects and arrays remain present. Use `json.RawMessage` to retain JSON null as a document. For non-nilable scalar and struct destinations, JSON null leaves the fresh zero value, as `encoding/json` specifies. An interface destination decodes according to its fresh interface type, rather than reusing its previous dynamic value.

The operation replaces the destination value; it does not mutate aliases to the old map, slice or pointed-to value. Callers must synchronize concurrent access to a shared destination. If additional schema or domain validation is required, decode into a caller-owned temporary and publish it only after that validation succeeds.

## Configuration

| Setting | Type | Default | Description |
|---|---|---|---|
| None | — | — | No environment variables, global settings or runtime options. |

## External dependencies

Runtime uses only the Go standard library. Tests use Testify. No services, network connections or background processes are required.

## Build / run / test

From this module directory:

```sh
GOWORK=off go test -race -count=1 ./...
GOWORK=off go vet ./...
GOWORK=off go mod tidy -diff
```

From the repository root, `make test-jsonvalue` runs the workspace race tests. `make jsonvalue-release-check` installs a candidate module ZIP into a fresh consumer, checks the absence of replacements and runtime dependencies, runs race tests and executes the quick start above.

## Operations

The package has no health endpoint, metrics, logs, goroutines or lifecycle to manage. A decoding failure leaves the destination intact; inspect errors with `errors.As` or `errors.Is` where their type or identity is meaningful. For a presence error, choose a destination that preserves the intended document (for example `json.RawMessage`) or use `UnmarshalFresh` when nil is allowed. Do not retry malformed input unchanged.

## Security

Bound input size before decoding untrusted data. This package does not impose document-size limits or validate application schemas, authorization, SQL keys or business rules. It returns errors to the caller without logging input; callers should avoid exposing sensitive document contents through custom decoder errors. Custom unmarshal methods remain trusted application code.

## Code map

| Path | Purpose |
|---|---|
| `jsonvalue.go` | Public decode functions and atomic replacement implementation. |
| `jsonvalue_test.go` | Replacement, failure isolation, custom decoder, target and presence regressions. |
| `../tools/check-jsonvalue-release.sh` | Fresh candidate-archive consumer and documentation checks. |

## License

[MIT](LICENSE).
