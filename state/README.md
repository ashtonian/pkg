# state

`state` stores durable local controller documents and coordinates cooperating processes through a trusted shared directory. It distinguishes exclusive creation from replacement and provides rooted leaf operations, private files, directory sync and canonical JSON identities. Business admission, native outcomes and distributed ownership remain with callers.

## Quick start

Requires Go 1.27 or later.

```go
package main

import (
    "context"
    "fmt"
    "os"
    "path/filepath"

    "github.com/ashtonian/pkg/state"
)

func main() {
    directory, err := os.MkdirTemp("", "state-example-")
    if err != nil { panic(err) }
    defer func() { if err := os.RemoveAll(directory); err != nil { panic(err) } }()
    file := filepath.Join(directory, "receipt.json")
    ctx := context.Background()
    if err := state.CreateJSONBounded(ctx, file, 1024, map[string]string{"phase":"prepared"}); err != nil { panic(err) }
    var receipt map[string]string
    if err := state.ReadJSONStrict(ctx, file, 1024, &receipt); err != nil { panic(err) }
    fmt.Println(receipt["phase"])
}
```

```text
prepared
```

## Configuration

| Input | Type | Default | Contract |
|---|---|---|---|
| `ctx` | context.Context | required | Cancellation/deadline for each operation |
| `file` | string | required | Path-based document; trusted parent directories |
| `root` | *os.Root | required for rooted APIs | Caller-owned directory handle |
| `name` | string | required for rooted APIs | One leaf filename, never traversal |
| `maxBytes` | int64 | required for bounded APIs | 1 byte through 128 MiB |
| `value` / `data` | any / []byte | required for writes | Complete caller-owned JSON value or byte-exact artifact |

`WriteJSON` is the unbounded convenience writer for trusted caller-owned values; use `WriteJSONBounded` for externally sized data. There are no environment variables, global settings or runtime dependencies.

## API and external dependencies

`ReadJSON` reads one bounded JSON value; `ReadJSONStrict` also rejects unknown schema fields. Reads return decoding errors and callers must reject the result on failure. `WriteJSON` and `WriteJSONBounded` atomically replace a path. `CreateJSONBounded` refuses existing files and symlinks. The path-based writers create missing parents with mode 0700 and retain existing parent permissions.

`ReadJSONStrictAt`, `WriteJSONAt` and `CreateJSONAt` use a caller-owned root and one leaf. Rooted reads reject nonregular/symlink entries. `WriteBytesAt` and `CreateBytesAt` preserve exact bytes, clone their input and share replacement/exclusive-create semantics. `SyncRoot` commits directory-entry durability without closing the caller's root.

`LockFile(ctx, root, name)` returns a nonblocking process-lifetime lock released by closing its file. Linux/macOS support is explicit; other platforms reject locking. All cooperating controllers must use the same trusted directory and lock identity.

`JSONDigest(data)` hashes one UTF-8 JSON document after object-order/spacing normalization, retaining exact numeric tokens. It distinguishes integers beyond float64 precision. It is not an RFC 8785 canonicalizer or semantic numeric equality. `ValidSHA256` validates canonical lowercase hexadecimal identities.

Runtime uses only the standard library. Testify is a test dependency. The filesystem must support same-directory hard links, rename and directory fsync.

## Build and test

`GOWORK=off go test -tags=unit,integration -race -count=1 ./...` covers real files, private modes, cancellation, oversized/invalid state, exclusive creation, rooted traversal, byte identity, lock contention and numeric digest identity. `make native-release-check` checks fresh module archives and executes the quick start.

## Operations

Writes stage mode-0600 files, sync their content, publish by exclusive link or replacement rename, then sync the directory. A sync error after publication means the entry may exist with uncertain durability; inspect the document before deciding what to do next. Never retry a native side effect merely because its receipt write failed. A killed process can leave a private `.ops-state-*` temporary file; confirm no controller owns the directory before removing abandoned files.

The package creates no scheduler, service, telemetry endpoint or background goroutine. Callers close root and lock handles after their owned operation and retain application-specific active markers until reconciliation completes.

## Security

Use private, trusted directories; this is not a sandbox against a peer who can rewrite those directories. File locks coordinate shared local storage, not independent hosts or copies of a receipt. Credentials and payload authorization belong to consumers. The package does not log contents. Rooted document reads deliberately differ from managed Secret symlink readers.

## Code map

| File | Responsibility |
|---|---|
| `files.go` | Bounded JSON/byte reads, exclusive creation and replacement |
| `sync.go` | Directory durability |
| `digest.go` | Exact numeric-token JSON identities |
| `lock*.go` | Local lifetime ownership and platform support |
| Tagged tests | Filesystem, ownership and digest workflows |

[MIT license](LICENSE).
