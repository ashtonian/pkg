# pkg

Independent Go modules for services. Install the module you need; each has
its own dependencies, version, changelog and releases.

| Module | Import path | Purpose |
|---|---|---|
| [gowalker](gowalker/README.md) | `github.com/ashtonian/pkg/gowalker` | Build libraries around struct tags: discover fields, inspect annotations and reuse field readers/writers. |
| [config](config/README.md) | `github.com/ashtonian/pkg/config` | Typed, layered configuration with validation, hot reload and redaction. |
| [httpx](httpx/README.md) | `github.com/ashtonian/pkg/httpx` | HTTP standards and resilience: retries, circuit breaking, caching, compression and request budgets for `net/http`. |
| [Kong provider](config/kongprovider/README.md) | `github.com/ashtonian/pkg/config/kongprovider` | CLI flags as config overrides, with help derived from the config struct. |

## Install

```bash
go get github.com/ashtonian/pkg/config@latest
go get github.com/ashtonian/pkg/httpx@latest
go get github.com/ashtonian/pkg/gowalker@latest
```

Gowalker, config and its Kong provider require **Go 1.27 or later**; httpx requires
**Go 1.26 or later**. Install only the modules you need. Add the Kong provider for Kong CLI integration;
add httpx's MessagePack, protobuf or Brotli module only for that wire format.

## httpx

Compose `http.RoundTripper` layers and keep using an ordinary `http.Client`:

- **Recover from failures:** safe retries, exponential backoff/jitter, `Retry-After`, per-origin circuit breakers and slow-request hedging.
- **Control request budgets:** per-origin rate/burst limits, in-flight concurrency limits, timeouts and cancellation.
- **Reuse responses:** ETag / Last-Modified revalidation, cache freshness and `Vary`, cache keys that include authorization/cookies and configured headers, stale-if-error fallback and background refresh.
- **Handle representations:** gzip/deflate and optional Brotli, streaming/ranged responses, bounded reads and optional typed decoding.
- **Operate clients:** origin-bound credentials, redacted logging and retry/cache/latency metrics hooks.

HTTP error statuses remain responses; transport and admission failures remain
Go errors. **Selecting a backup host/region is application-owned**—httpx
retries the current target and can serve permitted stale cached responses.
See the [supported patterns and failure behavior](httpx/README.md#common-http-patterns)
or the [standard-client example](httpx/README.md#quick-start).

## Documentation

- [Gowalker quick start](gowalker/README.md): read struct tags and reuse field information in your library, with exact example output.
- [Gowalker guides](gowalker/docs/README.md): tagged columns, custom field rules, returned values, API contracts and allocation costs.
- [httpx quick start](httpx/README.md): add compression, retry/backoff and ETag caching to a standard `http.Client`.
- [httpx guides](httpx/docs/README.md): transport composition, use cases, HTTP behavior, streaming and complete reference.
- [httpx Gin example](httpx/examples/gin/README.md): a runnable server and client with integration tests.
- [Config quick start](config/README.md): define a struct and load a config.
- [Config guides](config/docs/README.md): schemas, sources, secrets, reloads and reference.
- [Kong integration](config/kongprovider/README.md): flags and generated help.
- [Runnable cookbook](config/examples/cookbook/README.md): complete programs with tests.
- [Contributing and releases](CONTRIBUTING.md): workspace checks, versioning and release workflow.

## Versioning

Modules release independently with path-prefixed tags such as
`gowalker/v0.1.0`, `config/v0.3.0`, `config/kongprovider/v0.1.0` and `httpx/v0.1.0`. During `v0`, minor releases
may change APIs; see [versioning](CONTRIBUTING.md#versioning) and each
module's changelog.

## License

[MIT](LICENSE).
