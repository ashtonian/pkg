# httpx/codec

Optional codec implementations for `httpx`. Each codec lives in its own Go module so consumers pull only the wire formats they actually use; the core `httpx` package depends on none of them.

## Subpackages

| Package | Codec | Module |
|---|---|---|
| `httpx/codec/msgpack` | MessagePack | `github.com/vmihailenco/msgpack/v5` |
| `httpx/codec/proto` | Protocol Buffers | `google.golang.org/protobuf` |

Each subpackage has its own `go.mod` and README — see the individual READMEs for usage and import paths.

## References

- Parent: `httpx/README.md`
