# Contributing

[Module index](README.md) · [Config documentation](config/docs/README.md) · [Cookbook](config/examples/cookbook/README.md)

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

## Versioning

Every module is tagged with its directory as the prefix, as Go expects for a module below a repository's root: `config/v0.1.0`, `config/kongprovider/v0.1.0`. Versions follow [semantic versioning](https://semver.org); while a module is at `v0`, a minor version may change its API. Each module's `CHANGELOG.md` lists what changed.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from [Conventional Commits](https://www.conventionalcommits.org) (`feat(config): …`, `fix(kongprovider): …`). Each commit counts toward the modules whose files it touches. release-please keeps one release pull request for every module with unreleased changes; merging it tags each module, writes each changelog and publishes a GitHub release per module. (One pull request per module is not possible here: a module below another, `config/kongprovider` below `config`, would need a release branch nested under the other's.) `feat` raises the minor version and `fix` the patch; a breaking change (`feat(config)!: …`) raises the minor version while the module is at `v0`.

## Documentation checks

The config and Kong provider quick starts match their compiled examples
verbatim. Cookbook excerpts are checked against their source files. Keep
examples and their documentation in sync, and run `make ci` from the
repository root before opening a pull request.
