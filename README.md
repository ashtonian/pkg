# pkg

Independent Go modules for services. Install the module you need; each has
its own dependencies, version, changelog and releases.

| Module | Import path | Purpose |
|---|---|---|
| [config](config/README.md) | `github.com/ashtonian/pkg/config` | Typed, layered configuration with validation, hot reload and redaction. |
| [Kong provider](config/kongprovider/README.md) | `github.com/ashtonian/pkg/config/kongprovider` | CLI flags as config overrides, with help derived from the config struct. |

## Install

```bash
go get github.com/ashtonian/pkg/config@latest
```

Requires **Go 1.26 or later**. Add the Kong provider only when you use Kong.

## Documentation

- [Config quick start](config/README.md): define a struct and load a config.
- [Config guides](config/docs/README.md): schemas, sources, secrets, reloads and reference.
- [Kong integration](config/kongprovider/README.md): flags and generated help.
- [Runnable cookbook](config/examples/cookbook/README.md): complete programs with tests.
- [Contributing and releases](CONTRIBUTING.md): workspace checks, versioning and release workflow.

## Versioning

Modules release independently with path-prefixed tags such as
`config/v0.3.0` and `config/kongprovider/v0.1.0`. During `v0`, minor releases
may change APIs; see [versioning](CONTRIBUTING.md#versioning) and each
module's changelog.

## License

[MIT](LICENSE).
