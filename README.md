# pkg

Batteries and glue code for Go services. Each package here is an independent Go module with its own version and releases: import the one you need and take none of the others' dependencies.

| Package | Import path | What it does |
|---|---|---|
| [config](config) | `github.com/ashtonian/pkg/config` | Typed configuration on top of [koanf](https://github.com/knadh/koanf): defaults, files, environment and any koanf provider loaded through one validated pipeline, hot reload that publishes only a config that passed, changes pushed into the running process, and output that never prints a secret. |
| [config/kongprovider](config/kongprovider) | `github.com/ashtonian/pkg/config/kongprovider` | [Kong](https://github.com/alecthomas/kong) CLI flags as `config`'s highest-precedence source, with flag help taken from the config struct. |

## Install

```bash
go get github.com/ashtonian/pkg/config@latest
```

Go 1.26 or newer.

## Versioning

Every module is tagged with its directory as the prefix, as Go expects for a module below a repository's root: `config/v0.1.0`, `config/kongprovider/v0.1.0`. Versions follow [semantic versioning](https://semver.org); while a module is at `v0`, a minor version may change its API. Each module's `CHANGELOG.md` lists what changed.

## Development

The repository's `go.work` ties the published modules together, so a change to one is tested against the others' working trees.

| Command | What it runs |
|---|---|
| `make test` | Every module's tests, race detector on |
| `make lint` | `go vet` and staticcheck |
| `make release-check` | Each module outside the workspace, with the dependency versions its `go.mod` requires — what a consumer builds |
| `make cookbook-check` | The [config cookbook](config/examples/cookbook), a module of runnable recipes |
| `make ci` | All of the above |

A module that requires another module of this repository requires a released version of it. When it needs an unreleased change, release the dependency first, then raise the requirement.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from [Conventional Commits](https://www.conventionalcommits.org) (`feat(config): …`, `fix(kongprovider): …`). Each commit counts toward the modules whose files it touches, and release-please keeps one release pull request per module; merging it tags the module, writes its changelog and publishes a GitHub release. `feat` raises the minor version and `fix` the patch; a breaking change (`feat(config)!: …`) raises the minor version while the module is at `v0`.

## License

[MIT](LICENSE)
