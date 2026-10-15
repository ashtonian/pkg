# pkg

Independent Go modules for services. Install the module you need; each has
its own dependencies, version, changelog and releases.

| Module | Import path | Purpose |
|---|---|---|
| [config](config/README.md) | `github.com/ashtonian/pkg/config` | Typed, layered configuration with validation, hot reload and redaction. |
| [httpx](httpx/README.md) | `github.com/ashtonian/pkg/httpx` | HTTP caching, compression and retry/backoff as composable `http.RoundTripper` layers. |
| [Kong provider](config/kongprovider/README.md) | `github.com/ashtonian/pkg/config/kongprovider` | CLI flags as config overrides, with help derived from the config struct. |

## Install

```bash
go get github.com/ashtonian/pkg/config@latest
go get github.com/ashtonian/pkg/httpx@latest
```

Requires **Go 1.26 or later**. Install only the modules you need. Add the Kong provider for Kong CLI integration;
add httpx's MessagePack, protobuf or Brotli module only for that wire format.

## Documentation

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
`config/v0.3.0`, `config/kongprovider/v0.1.0` and `httpx/v0.1.0`. During `v0`, minor releases
may change APIs; see [versioning](CONTRIBUTING.md#versioning) and each
module's changelog.

## License

[MIT](LICENSE).
