# Contributing

[Module index](README.md) · [Gowalker guides](gowalker/docs/README.md) · [httpx guides](httpx/docs/README.md) · [Config documentation](config/docs/README.md) · [Cookbook](config/examples/cookbook/README.md)

## Development

The repository's `go.work` ties the published modules together, so a change to one is tested against the others' working trees.

| Command | What it runs |
|---|---|
| `make test` | Every published module's tests, race detector on; both httpx build tags |
| `make test-httpx-unit` / `make test-httpx-integration` | httpx unit or real HTTP integration lane |
| `make lint` | `go vet` and staticcheck |
| `make release-check` | Gowalker and config standalone builds plus the module archive checks below |
| `make gowalker-release-check` | Gowalker archive downloaded into a fresh consumer, race tests, the README quick start and complete usage examples |
| `make httpx-release-check` | Four module ZIPs downloaded by fresh consumers, both tagged race lanes, Gin integration and benchmark smoke tests |
| `make tool-check` | Module archive tooling tests and static analysis |
| `make cookbook-check` | The [config cookbook](config/examples/cookbook), a module of runnable recipes |
| `make ci` | All of the above |

A module that requires another module of this repository requires a released version of it. When it needs an unreleased change, release the dependency first, then raise the requirement.

Workspace development requires Go 1.27. Gowalker is its own module at
`github.com/ashtonian/pkg/gowalker`, released with `gowalker/v…` tags. Its
initial release is configured as `v0.1.0`. Public modules contain source, tests,
licenses and user documentation; project plans, reviews and audits stay outside
the public repository. Its archive check runs the README quick start and usage programs verbatim.

## Versioning

httpx tests require `-tags=unit` or `-tags=integration`; plain `go test ./...`
skips them. The Gin example, comparison benchmarks and release tooling keep
separate development modules outside the default workspace. The archive
check tests them against candidate releases without local replacements.

Every module is tagged with its directory as the prefix, as Go expects for a module below a repository's root: `gowalker/v0.1.0`, `config/v0.1.0`, `config/kongprovider/v0.1.0`, `httpx/v0.1.0`, `httpx/codec/msgpack/v0.1.0`, `httpx/codec/proto/v0.1.0`, `httpx/compress/brotli/v0.1.0`. Versions follow [semantic versioning](https://semver.org); while a module is at `v0`, a minor version may change its API. Each module's `CHANGELOG.md` lists what changed.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from [Conventional Commits](https://www.conventionalcommits.org) (`feat(config): …`, `fix(kongprovider): …`). Each commit counts toward the modules whose files it touches. release-please keeps one combined release pull request covering the modules with unreleased changes; merging it tags those modules, writes their changelogs and publishes a GitHub release per module. (One pull request per module is not possible here: a module below another, `config/kongprovider` below `config`, would need a release branch nested under the other's.) `feat` raises the minor version and `fix` the patch; a breaking change (`feat(config)!: …`) raises the minor version while the module is at `v0`.

New modules use `initial-version` in the release configuration to choose
an initial version; a `0.0.0` manifest entry is only a pre-release marker.
The httpx modules are configured to start at `v0.1.0`. Before their first tag,
the workspace maps the optional modules' parent requirement to `./httpx`.
Published modules contain no local replacements. Validate candidate archives
with `make httpx-release-check`, and verify public-proxy installation after
release. Release a dependency before raising another module's requirement.

The archive builder includes each module's MIT license and excludes nested
modules from its parent ZIP. It rejects internal planning documents and
references in published archives; retain only public API documentation,
examples, tests and normal module files. References to HTTP standards remain
valid. Candidate checks use isolated checksums and never
write synthetic archive checksums into tracked `go.sum` files.

## Documentation checks

The httpx core and optional module quick starts are also checked against
compiled examples. The core quick start has real HTTP integration tests;
Gin tests build and run the client against the server. Keep the httpx README
focused on its quick start and guide links, with use cases and full options
in `httpx/docs/`.

The config and Kong provider quick starts match their compiled examples
verbatim. Cookbook excerpts are checked against their source files. Keep
examples and their documentation in sync, and run `make ci` from the
repository root before opening a pull request.
