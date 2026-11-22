# Contributing

[Module index](README.md) · [Gowalker guides](gowalker/docs/README.md) · [httpx guides](httpx/docs/README.md) · [Config documentation](config/docs/README.md) · [Cookbook](config/examples/cookbook/README.md)

## Development

The repository's `go.work` ties the published modules together, so a change to one is tested against the others' working trees.

| Command | What it runs |
|---|---|
| `make test-native-unit` / `make test-native-integration` / `make native-release-check` | Process/state/1Password tagged race tests and fresh candidate-archive consumers |
| `make test-gitx` | gitx race tests: real scratch repositories, in-memory trees and a rebase through the installed driver |
| `make test-jsonvalue` / `make jsonvalue-release-check` | JSON replacement race tests / fresh module-archive consumer and README example |
| `make test` | Every published module's tests, race detector on (modernc's SQLite builds uninstrumented); both httpx build tags. `-count=1` only where tests start processes, run the installed git or reach servers |
| `make test-httpx-unit` / `make test-httpx-integration` | httpx unit or real HTTP integration lane |
| `make test-parse-unit` / `make test-parse-race` / `make test-parse-race-all` | Parse unit tests and existing fuzz seeds without the race detector / under it, only the tests that run parse code on several goroutines (files tagged `concurrency`) / every unit test under it |
| `make test-parse-integration` | Parse integration race tests; SQLite always runs, PostgreSQL/PostGIS through explicit DSNs |
| `make test-parse-sdk` | TypeScript checks, lint, shared corpora and production build |
| `make parse-release-check` | Fresh module-archive consumer, tagged tests, README program and dependency audit |
| `make parse-sdk-release-check` | Packed SDK consumer, TypeScript declarations, runtime subpaths and sourcemaps |
| `make parse-sdk-registry-check` | Registry processing delay, integrity and deadline regressions |
| `make lint` / `make lint-cookbook` | `go vet` and staticcheck over the published modules, tagged ones in one pass with both tags / over the cookbook |
| `make release-check` | Every module as a consumer gets it, without the race detector: gitx and config standalone builds plus the module archive checks below. `RELEASE_CHECKS='parse-release-check'` runs a subset |
| `make gowalker-release-check` | Gowalker archive downloaded into a fresh consumer, its tests, the README quick start and complete usage examples |
| `make httpx-release-check` | Five module ZIPs downloaded by fresh consumers, both tagged lanes, Gin integration and benchmark smoke tests |
| `make tool-check` | Module archive tooling tests and static analysis |
| `make release-please-check` | The release workflow's release-please runner: first-parent release notes and their release-please wiring ([tools/release-please](tools/release-please/README.md)) |
| `make cookbook-check` | The [config cookbook](config/examples/cookbook), a module of runnable recipes |
| `make ci` | All of the above |

| Make variable | Default | Description |
|---|---|---|
| `PACKAGES` | `all` | The packages the `test-*` and `lint` targets cover: `all`, or space-separated `<module dir>:<package dir>` entries, as CI passes the packages a change reaches. `make test-parse-unit PACKAGES='parse:parse parse:parse/filter'` tests those two packages; a target whose modules have no entry runs nothing |
| `RELEASE_CHECKS` | every module's release check | The checks `make release-check` runs |

GitHub Actions runs workflows here only for the repository owner. Pull requests opened by a bot, such as Dependabot updates or a release PR opened without the `RELEASE_PLEASE_TOKEN` secret, therefore get no checks: run CI on the branch with `gh workflow run ci.yml -R ashtonian/pkg --ref <branch>`, and the pull request shows the results.

CI runs what a change reaches. The change-scope job takes the event's complete delta: a
pull request's merge against its base, or a push to `main` against the newest commit of
`main`'s history whose push run passed, so a failed, cancelled or superseded run leaves no
change unvalidated. `tools/ci_scope.py` lists the workspace's packages with `go list -test`
under every platform and build tag set CI compiles, and selects:

- The packages a change reaches: each package it edits, and every workspace package whose
  build or tests link one of them. A test file reaches only its own package. Any other file
  in a module (a README, test data, an embedded file) reaches every package of that module,
  because tests read and packages embed files anywhere in it.
- The jobs whose modules hold a reached package: the Ubuntu suites and lint for any, macOS
  for the platform modules, the PostgreSQL legs for parse, Go 1.26 for httpx and gitx. Their
  test and lint targets cover only the reached packages (`PACKAGES`).
- The release checks of the module trees the change touches, and the SDK job when the SDK,
  the corpora in `parse/testdata` or its check scripts change.

`CONTRIBUTING.md`, the root `README.md` and the Markdown guides in `config/docs`,
`gowalker/docs`, `httpx/docs` or `parse/docs` reach no package. Module manifests (`go.mod`,
`go.sum`), `go.work`, `go.work.sum`, the Makefile, workflows, CI scripts, release
configuration and unknown paths reach everything, as do a delta the job cannot compute, a
package `go list` cannot read, and a module outside the workspace that requires a workspace
module. Renames include both paths. Explicit workflow dispatch and the nightly run always
run the full suite. When a job you expected is skipped, the change-scope job's summary lists
the selected jobs, packages and release checks, or why it selected everything.

Release checks run per module: each reads only its module's tree, its own script and the
archive tooling, so a change runs the checks of the modules it touches (the cookbook's with
either config module), and shared build files, workflow, tooling, manifests and unknown
paths run all of them. The suites run once each:

- Ubuntu runs the reached packages' race tests (parse's unit tests as below) on the newest
  Go 1.27 patch release; macOS runs only the modules with platform code (config, process,
  state, 1Password).
- Parse's unit tests run without the race detector, then under it only the tests that run
  parse code on several goroutines: files tagged `concurrency`. Parse starts no goroutines
  of its own, so no other test can fail under race; racing the rest would add minutes of
  instrumented compiling and matrix suites to every pull request. A unit test file that
  starts goroutines or calls `t.Parallel` carries the tag; `tools/test_ci_parse_race.py`
  fails the change-scope job when one does not. The nightly run races every parse test.
- Lint vets every reached package and test on Go 1.27.0, the declared floor, so it is also
  the floor's build check; the cookbook lints there whenever its check runs. httpx and gitx
  run their tests on Go 1.26, their floor.
- Parse's SQLite integration runs beside PostgreSQL 17 and 18.
- The SDK runs every check on its Node.js floor, then only its runtime tests and packed
  consumer on the newest LTS.
- Release checks repeat the tests once, without the race detector, against each archive and
  the dependency versions its go.mod requires. Their fresh consumers build with `-trimpath`,
  which keeps the scratch module cache's random path out of build-cache keys, so the
  dependencies come from the shared build cache; the job logs a cache summary per go command.

A single signed merge onto `main` may reuse successful PR or owner-dispatched branch
coverage from the preceding six hours. The receipt must match the complete source tree,
workflow, repository variables, run attempt and selected scope: jobs, packages and release
checks. Every job the scope selected passed, and none it left out ran.
PR receipts also match both tested merge parents; a dispatched branch must be the actual
merged head with the same resulting tree. Failed, cancelled, incomplete or missing evidence
runs validation. Post-merge benchmark artifacts remain independent because PRs do not
produce them; a push records them only when it reaches `parse/filter`, the package they
build. Explicit reruns execute validation even if an older receipt matches.
A newer run of the same PR or of `main` cancels the older one, since it covers every change
the older run would have; manual acceptance runs finish.

The `nightly` workflow runs `ci.yml` on main's current commit once a day, and on
`gh workflow run nightly.yml -R ashtonian/pkg`: every suite and release check at full scope,
every parse unit test under the race detector, and the benchmarks. It records no acceptance
receipt. Its failure is a regression that scoping or the targeted race run let through, or a
new toolchain release breaking a floating Go or Node.js version: reproduce it with the failing
step's make target (`make test-parse-race-all` for a parse race report) and fix it on a branch.
GitHub turns off a public repository's schedules after 60 days without activity; turn the
workflow back on from the Actions tab.

A module that requires another module of this repository requires a released version of it. When it needs an unreleased change, release the dependency first, then raise the requirement.

Workspace development requires Go 1.27. The TypeScript SDK's targets also need Node.js
22.13 or later (its `engines` floor, which CI runs exactly alongside the newest LTS) and pnpm 12, at the version `parse/sdk/ts/package.json` pins in `packageManager`;
`make test-parse-sdk` refuses any other pnpm release.
Gowalker is its own module at
`github.com/ashtonian/pkg/gowalker`, released with `gowalker/v…` tags. Its
initial release is configured as `v0.1.0`. Public modules contain source, tests,
licenses and user documentation; project plans, reviews and audits stay outside
the public repository. Its archive check runs the README quick start and usage programs verbatim.

## Versioning

httpx tests require `-tags=unit` or `-tags=integration`; plain `go test ./...`
skips them. The Gin example, comparison benchmarks and release tooling keep
separate development modules outside the default workspace. The archive
check tests them against candidate releases without local replacements.

Every module is tagged with its directory as the prefix, as Go expects for a module below a repository's root: `gowalker/v0.1.0`, `config/v0.1.0`, `config/kongprovider/v0.1.0`, `httpx/v0.1.0`, `httpx/oauth2/v0.1.0`, `httpx/codec/msgpack/v0.1.0`, `httpx/codec/proto/v0.1.0`, `httpx/compress/brotli/v0.1.0`. Versions follow [semantic versioning](https://semver.org); while a module is at `v0`, a minor version may change its API. Each module's `CHANGELOG.md` lists what changed.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from [Conventional Commits](https://www.conventionalcommits.org) (`feat(config): …`, `fix(kongprovider): …`). Each commit counts toward the modules whose files it touches. release-please keeps one combined release pull request covering the modules with unreleased changes; merging it tags those modules, writes their changelogs and publishes a GitHub release per module. (One pull request per module is not possible here: a module below another, `config/kongprovider` below `config`, would need a release branch nested under the other's.) `feat` raises the minor version and `fix` the patch; a breaking change (`feat(config)!: …`) raises the minor version while the module is at `v0`.

Land feature branches with main as the first parent of a signed merge commit and a conventional subject describing the change. Its diff must contain the feature, rather than only a later main-to-feature synchronization. This preserves the module change and release classification when older feature commits precede the latest release boundary.

Release notes and versions come from main's first-parent history. The `release-please` workflow runs release-please through [`tools/release-please`](tools/release-please/README.md), whose `first-parent` plugin counts each pull request once, as its merge commit. The conventional commits on its branch hand that merge what they call for: the highest type (a `feat` merged as `fix(…)` still bumps the minor version), `BREAKING CHANGE` notes, `Release-As`, and closed issues. The merge subject is still the pull request's release-note entry, so make it the conventional summary of the whole change, with `!` when it breaks anything. The job warns when a branch commit raises a merge's type. Branch commits older than the last release, on a pull request merged after it, never reach release-please, so for such a pull request the merge subject alone decides its entry and bump. A merge whose subject release-please cannot parse leaves its branch commits listed instead, so nothing is dropped. The stock `release-please-action` cannot load the plugin and refuses this repository's config. From a full clone, `pnpm -C tools/release-please install --frozen-lockfile`, then `GITHUB_TOKEN=… node tools/release-please/run.mjs --repo ashtonian/pkg --dry-run`, shows the release PR the current main would produce.

Merge a release PR with a signed merge commit whose diff includes every
released module. If a changelog needs a follow-up edit, do not fast-forward
only that final commit onto main: release-please finds each module's boundary
by the files changed in its release commit. A final commit that changes only
one module can cause another module's old changes to appear unreleased.
The configured `last-release-sha` is the shared history floor after config
`v0.6.0` and gowalker `v0.2.0`; newer per-module releases still determine their
own boundaries. Leave this floor in place unless intentionally resetting the
release history.

When editing a release PR description, preserve the standalone `---` lines
around the component `<details>` blocks. Release-please parses those boundaries
to identify the versions to publish after merge.

New modules use `initial-version` in the release configuration to choose
an initial version; a `0.0.0` manifest entry is only a pre-release marker.
The httpx modules are configured to start at `v0.1.0`. Before their first tag,
the workspace maps the optional modules' parent requirement to `./httpx`.
Published modules contain no local replacements. Validate candidate archives
with `make httpx-release-check`, and verify public-proxy installation after
release. Release a dependency before raising another module's requirement.

Candidate archives include tracked files and nonignored new files, respecting Git ignore rules after SDK builds. Deleted files and symlinked subtrees stay out. Source exports outside Git retain directory-based archive support; unexpected Git failures fail the check. The archive builder includes each module's MIT license and excludes nested
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

### Private references

This repository is public. Docs, comments, tests, fixtures and tooling
describe code outside it generically ("a downstream consumer", "any server
built on parse") and cite only this repository's files; an "Imported by" line
lists this repository's packages. CI's change-scope job runs
`tools/private_references.py`, which fails on any tracked text line matching
a pattern in the `PRIVATE_REFERENCE_PATTERNS` Actions secret: one POSIX
extended regular expression per line, matched case-insensitively, `#` lines
ignored. The patterns stay in the secret because they name what they guard,
and the check reports only `path:line`. Without the secret the check fails.
Run it locally with a file of the same patterns:

```bash
python3 tools/private_references.py ~/path/to/private-patterns
```

To change the patterns, replace the secret
(`gh secret set PRIVATE_REFERENCE_PATTERNS -R ashtonian/pkg < patterns`).

## Parse SQL matrix

Parse requires Go 1.27 and publishes as `github.com/ashtonian/pkg/parse`, with `parse/v…` tags. CI runs unit tests and existing fuzz seeds (under the race detector nightly, and on pull requests only the tests tagged `concurrency`), SQLite integration beside PostgreSQL 17/18 with PostGIS 3.6 under the race detector, and the TypeScript SDK's shared corpora. modernc's SQLite builds without race or checkptr instrumentation: it is C translated to Go, and instrumenting it made the SQLite-backed suites about 30 times slower without checking this repository's code. The SDK release check installs its tarball in a fresh consumer and checks public declarations, runtime subpaths and self-contained JavaScript sourcemaps. The SDK package version follows the Parse release component. CI stores benchmark text and CPU/allocation profiles from main pushes that change the Go code they build, and from the nightly run; it does not run dedicated fuzzing sessions.

For an existing test server, set `PARSE_POSTGRES_DSN` and `PARSE_POSTGIS_DSN` and run `make test-parse-integration`. Each case gets a fresh schema. A requested server that is unavailable fails; an unrequested PostgreSQL case skips. `make parse-release-check` verifies a candidate archive independently of the workspace; publication additionally requires clean public-proxy installation of the immutable release.

## Parse SDK publication

The `release-please` workflow builds `@ashtonian/parse` from a published stable `parse/vX.Y.Z` tag on main. It requires the tag, release manifest and SDK package to agree on the version, verifies the packed consumer, and retains the exact tarball before publishing. Repeating a run accepts an existing registry version only when its package integrity matches. Before the final fresh-consumer check, the workflow waits up to 15 minutes for that integrity to appear in both version and installation metadata, with 30-second request deadlines. npm scans packages before making them installable; successful publication can precede availability by several minutes. An availability timeout requires inspecting npm's processing/review status and retrying acceptance after the version becomes visible. Authentication, malformed metadata, integrity mismatches and consumer failures stop immediately; the wait never republishes the package.

Configure npm trusted publishing for repository `ashtonian/pkg` and workflow filename `release-please.yml` in the package's npm settings. The workflow uses a GitHub-hosted runner, and `pnpm publish` exchanges the job's OIDC token with npm; it does not require a stored npm token. Creating the package for the first time requires an authenticated package owner. Dispatch the immutable tag with `publish=false`, download its retained tarball, publish those exact bytes with `pnpm publish <tarball> --access public --registry=https://registry.npmjs.org/`, then configure the trusted publisher for subsequent releases. Follow [npm's trusted publisher requirements](https://docs.npmjs.com/trusted-publishers/).

The repository owner can prepare an already published tag without registry writes (the dispatch default):

```bash
gh workflow run release-please.yml -R ashtonian/pkg -f tag=parse/vX.Y.Z
```

Add `--ref parse/vX.Y.Z -f publish=true` to publish through the configured trusted publisher and verify registry consumption. Publication requires the workflow's commit to equal the selected release commit so npm provenance identifies the actual package source. A build-only run verifies registry consumption when that exact package already exists; an absent version leaves publication and registry acceptance unexecuted. When release-please creates a Parse release, its output starts the publishing job directly in the same workflow, including when it uses `GITHUB_TOKEN`. Manual dispatch supports artifact preparation and retries; it does not create a release PR or a GitHub release. For tags that predate this workflow's publication support, use the authenticated first-publication procedure above. A tag whose SDK has a `package-lock.json` and no `packageManager` field is built with npm: dispatch it with `--ref <tag>`, which runs that tag's own workflow, because this workflow sets pnpm up from the release's `packageManager` field. On authentication failure, verify the npm publisher's repository and workflow filename. On an integrity mismatch, inspect the retained artifact and registry version; do not change an existing tag or overwrite a published version.
