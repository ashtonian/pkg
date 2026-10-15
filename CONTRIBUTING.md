# Contributing

[Module index](README.md) · [HTTPx guides](httpx/docs/README.md) · [Config documentation](config/docs/README.md) · [Cookbook](config/examples/cookbook/README.md)

## Development

The repository's `go.work` ties the published modules together, so a change to one is tested against the others' working trees.

| Command | What it runs |
|---|---|
| `make test` | Every published module's tests, race detector on; both HTTPx build tags |
| `make test-httpx-unit` / `make test-httpx-integration` | HTTPx unit or real HTTP integration lane |
| `make lint` | `go vet` and staticcheck |
| `make release-check` | Config standalone builds plus the HTTPx archive check below |
| `make httpx-release-check` | Four module ZIPs downloaded by fresh consumers, both tagged race lanes, Gin integration and benchmark smoke tests |
| `make tool-check` | Module archive tooling tests and static analysis |
| `make cookbook-check` | The [config cookbook](config/examples/cookbook), a module of runnable recipes |
| `make ci` | All of the above |

A module that requires another module of this repository requires a released version of it. When it needs an unreleased change, release the dependency first, then raise the requirement.

## Versioning

HTTPx tests require `-tags=unit` or `-tags=integration`; plain `go test ./...`
skips them. The Gin example, comparison benchmarks and release tooling keep
separate development modules outside the default workspace. The archive
check tests them against candidate releases without local replacements.

Every module is tagged with its directory as the prefix, as Go expects for a module below a repository's root: `config/v0.1.0`, `config/kongprovider/v0.1.0`, `httpx/v0.1.0`, `httpx/codec/msgpack/v0.1.0`, `httpx/codec/proto/v0.1.0`, `httpx/compress/brotli/v0.1.0`. Versions follow [semantic versioning](https://semver.org); while a module is at `v0`, a minor version may change its API. Each module's `CHANGELOG.md` lists what changed.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from [Conventional Commits](https://www.conventionalcommits.org) (`feat(config): …`, `fix(kongprovider): …`). Each commit counts toward the modules whose files it touches. release-please keeps one combined release pull request covering the modules with unreleased changes; merging it tags those modules, writes their changelogs and publishes a GitHub release per module. (One pull request per module is not possible here: a module below another, `config/kongprovider` below `config`, would need a release branch nested under the other's.) `feat` raises the minor version and `fix` the patch; a breaking change (`feat(config)!: …`) raises the minor version while the module is at `v0`.

The four HTTPx components begin at `0.0.0` in the release manifest; that is
a bootstrap marker, never a dependency version or release tag. Each HTTPx
package also sets `initial-version: "0.1.0"` in the release configuration, so its first coordinated release is `v0.1.0` rather than the
Go strategy's default initial `v1.0.0`. Feature and fix commits drive normal
versioning after that first release. The optional modules already require
that parent version.
The workspace maps the initial parent version to `./httpx` so development
checks work before the first public tag; published `go.mod` files contain no
replacement. `make httpx-release-check` disables the workspace and validates
candidate archives through a temporary local proxy. After the first release, verify installation
from the public Go proxy. Later modules version independently; release a new
parent before raising an optional module's requirement.

The archive builder includes each module's MIT license and excludes nested
modules from its parent ZIP. Candidate checks use isolated checksums and never
write synthetic archive checksums into tracked `go.sum` files.

## Documentation checks

The HTTPx core and optional module quick starts are also checked against
compiled examples. The core quick start has real HTTP integration tests;
Gin tests build and run the client against the server. Keep the HTTPx README
focused on its quick start and guide links, with use cases and full options
in `httpx/docs/`.

The config and Kong provider quick starts match their compiled examples
verbatim. Cookbook excerpts are checked against their source files. Keep
examples and their documentation in sync, and run `make ci` from the
repository root before opening a pull request.
