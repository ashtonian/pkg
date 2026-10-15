# httpx/compress

Optional compression middleware for `httpx`. Each compressor lives in its own Go module so consumers pull only the algorithms they need; the core `httpx` package depends on none of them.

## Subpackages

| Package | Algorithm | Module |
|---|---|---|
| `httpx/compress/brotli` | Brotli | `github.com/andybalholm/brotli` |

Each subpackage has its own `go.mod` and README.

## References

- Parent: `httpx/README.md`
