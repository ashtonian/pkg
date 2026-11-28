# Contributing

[Module index](README.md) · [Gowalker guides](gowalker/docs/README.md) · [httpx guides](httpx/docs/README.md) · [Config documentation](config/docs/README.md) · [Cookbook](config/examples/cookbook/README.md)

## Development

The repository's `go.work` ties the published modules together, so a change to one is tested against the others' working trees.

| Command | What it runs |
|---|---|
| `make test-native-unit` / `make test-native-integration` / `make native-release-check` | Process/state/1Password tagged race tests and fresh candidate-archive consumers |
| `make test-gitx` | gitx race tests: real scratch repositories, in-memory trees and a rebase through the installed driver |
| `make test-jsonvalue` / `make jsonvalue-release-check` | JSON replacement race tests / fresh module-archive consumer and README example |
| `make test` | Every published module's tests, race detector on (modernc's SQLite builds uninstrumented); both httpx build tags. The unit targets replay unchanged packages' passing results ([test cache](#test-cache)); the rest pass `-count=1` |
| `make test-httpx-unit` / `make test-httpx-integration` | httpx unit or real HTTP integration lane |
| `make test-parse-unit` / `make test-parse-race` / `make test-parse-race-all` | Parse unit tests without the race detector / under it, only the tests that run parse code on several goroutines (files tagged `concurrency`) / every unit test under it |
| `make test-parse-integration` | Parse integration race tests; SQLite always runs, PostgreSQL/PostGIS through explicit DSNs |
| `make test-parse-sdk` | The repository's pnpm contract, then TypeScript checks, lint, shared corpora and production build |
| `make parse-release-check` | Fresh module-archive consumer, tagged tests, README program and dependency audit |
| `make test-parse-client` / `make parse-client-release-check` | parse/client race tests against parse's recorded wire and corpora / fresh module-archive consumer whose module graph holds the client alone, standard-library imports and no `net/http`, and the README program |
| `make parse-sdk-release-check` | Packed SDK consumer, TypeScript declarations, runtime subpaths and sourcemaps |
| `make parse-sdk-registry-check` | Registry readiness and retries: a version that appears late, never appears, or fails otherwise, through scripted pnpm and through the real pnpm against a loopback registry; integrity checks and the bounds' configuration |
| `make lint` / `make lint-cookbook` | gofmt over every tracked Go file, then `go vet`, [`go fix -diff`](#gofmt-and-go-fix), staticcheck and golangci-lint ([new issues only](#lint-baseline)) over the published modules, tagged ones in one pass with both tags / `go vet`, `go fix -diff` and staticcheck over the cookbook |
| `make fix` | Applies what `make lint`'s gofmt and `go fix -diff` report |
| `make release-check` | Every module as a consumer gets it, without the race detector: gitx and config standalone builds plus the module archive checks below. `RELEASE_CHECKS='parse-release-check'` runs a subset |
| `make gowalker-release-check` | Gowalker archive downloaded into a fresh consumer, its tests, the README quick start and complete usage examples |
| `make httpx-release-check` | Five module ZIPs downloaded by fresh consumers, both tagged lanes, Gin integration and benchmark smoke tests |
| `make tool-check` | Module archive tooling tests and static analysis |
| `make docs-check` | Every module's Markdown, in the workspace or kept out of it, held to its code: the Go identifiers its code spans name, its relative links and their anchors, its code-map entries; for a module that opts in, every Go file in a code map, every export named and every option in a table row ([tools/doccheck](tools/doccheck/README.md)) |
| `make release-please-check` | The release workflow's release-please runner: first-parent release notes and their release-please wiring, the [release-notes check](#releases) of a pull request's commits ([tools/release-please](tools/release-please/README.md)), and the repository's pnpm contract |
| `make cookbook-check` | The [config cookbook](config/examples/cookbook), a module of runnable recipes |
| `make cookbook-integration` | The cookbook's database tests: the settings-store recipe on SQLite, and on PostgreSQL through `COOKBOOK_POSTGRES_DSN` (`make cookbook-postgres-up` starts a server; see the [cookbook's README](config/examples/cookbook/README.md#build-run-and-test)) |
| `make parse-example-check` | The [Parse server example](parse/examples/server), a module of its own, against the Parse working tree: tidy, vet, staticcheck and its tagged tests under race |
| `make ci` | All of the above |

| Make variable | Default | Description |
|---|---|---|
| `PACKAGES` | `all` | The packages the `test-*` and `lint` targets cover: `all`, or space-separated import paths, as CI passes the packages a change reaches. `make test-parse-unit PACKAGES='github.com/ashtonian/pkg/parse github.com/ashtonian/pkg/parse/filter'` tests those two packages; each target takes its own modules' paths, and one with none runs nothing |
| `RELEASE_CHECKS` | every module's release check | The checks `make release-check` runs |
| `LINT_ALL` | empty | Empty: golangci-lint in `make lint` reports only the issues on lines changed since the merge base with `origin/main`. `1`: every issue |
| `UNIT_TEST_COUNT` | empty | `go test`'s count flag for the unit targets: `test-native-unit`, `test-jsonvalue`, `test-gowalker`, `test-config`, `test-httpx-unit`, `test-parse-unit`, `test-parse-race` and `test-parse-race-all`. Empty: a package replays its last passing result, shown `(cached)`, while its test binary and what its tests read are unchanged ([test cache](#test-cache)). `-count=1` reruns every test, as the nightly run does. Every other target passes `-count=1` itself |

GitHub Actions runs workflows here only for the repository owner. Pull requests opened by a bot, such as Dependabot updates or a release PR opened without the `RELEASE_PLEASE_TOKEN` secret, therefore get no checks: run CI on the branch with `gh workflow run ci.yml -R ashtonian/pkg --ref <branch>`, and the pull request shows the results. [Dependency updates](#dependency-updates) gives the procedure for Dependabot's.

CI runs what a change reaches. Its change-scope job runs go-ci-actions `scope`, configured
in `ci.yml`. A pull request is measured from the base its merge commit merges into, and a
push to `main` from the newest green `ci.yml` run on `main`, so a failed, cancelled or
superseded run leaves no change unvalidated. The scope lists the workspace's packages with
`go list -test` and selects:

- The packages a change reaches: each package holding a changed file (its directory, its
  `testdata` tree, or an embedded file), and every workspace package whose build or tests
  depend on one. A file a test reads from outside its package, such as a README program
  under `examples/`, is declared in the scope's `go-owners`, so a change to it reaches the
  package that reads it; `tools/test_ci_release_checks.py` fails when a test reads a tracked
  file outside its directory that `go-owners` does not name.
- The jobs whose packages or paths the change reaches: the Ubuntu suites and lint for any
  package, macOS for the platform modules, the PostgreSQL legs for parse, and for the
  cookbook's settings-store recipe when the recipe, its database fixture or the cookbook's
  dependencies change or a change reaches the config package, Go 1.26 for httpx and gitx,
  the SDK job for the SDK, the corpora in `parse/testdata` or its check scripts.
  Their test and lint targets cover only the reached packages (`PACKAGES`), and of those
  only the ones with a file under the target's build tags, as a `path/...` pattern does: go
  refuses a package named explicitly whose files the tags all exclude, such as
  `parse/internal/dbtest` under `-tags=concurrency`.
- The release checks of the module trees the change touches (`tools/ci_release_checks.py`,
  below); the modules job runs only when one is selected.

`go-ignore` lists what no workspace Go code reads: `CONTRIBUTING.md`, the root `README.md`,
changelogs, the Markdown guides in `config/docs`, `gowalker/docs` and `httpx/docs` (parse's
are read by parse's tests, which render their tables from the code), the SDK, the release-please manifest and configuration, the release, SDK and
benchmark scripts, the local PostgreSQL script (`tools/postgres-up.sh`, which `tools/test_ci_postgres_up.py` checks), golangci-lint's `.golangci.yml`, and the modules outside the workspace (the cookbook, the Gin example, httpx's
benchmarks, the Parse server example, the ruleguard rules, the archive, documentation and release tooling), whose own checks read them. A change to
`.golangci.yml` runs lint over every package and nothing else; a change to `tools/gorules/`, a module the
documentation check reads, runs that check too. A module manifest
(`go.mod`, `go.sum`), `go.work`, `go.work.sum` or any other file no package owns reaches
every package; workflows, the Makefile and the CI scripts run every job. So do explicit
workflow dispatch, the `full-ci` label on a pull request (it applies from the next push or
run), a delta the scope cannot compute, and a package `go list` cannot load. Renames
include both paths. When a job you expected is skipped, or ran unexpectedly, read the scope
step's log: each job's line names the file or package that selected it, and `reason` and
`go-reason` say what the change was measured from and why the Go scope widened.

Release checks run per module: each reads only its module's tree, its own script and the
archive tooling, so a change runs the checks of the modules it touches (the cookbook's with
either config module, the Parse server example's with Parse; a nested module such as
`parse/client` is its own module, not part of its parent's archive or replacement), and shared build files, workflow, tooling, manifests and unknown
paths run all of them. The documentation check reads every module, in the workspace or not, so a change to any of them runs it. The suites run once each:

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
  the floor's build check; the cookbook lints there whenever its check runs or lint covers
  every package. Staticcheck and golangci-lint run on Go 1.27.4, the first 1.27 release whose export data
  keeps generic methods in declaration order; on earlier ones its facts about methods are
  misattributed (golang/go#82106). httpx and gitx run their tests on Go 1.26, their floor.
- Parse's SQLite integration runs beside PostgreSQL 17 and 18.
- The settings-store recipe's database tests run on SQLite and PostgreSQL 19, in their own
  job (`cookbook-postgres`), so the recipe's PostgreSQL behaviour runs on a real server
  without a service container in the modules job, which runs on every change.
- The SDK runs every check on its Node.js floor, then only its runtime tests and packed
  consumer on the newest LTS.
- Unit tests replay passing results across runs and runners: setup gives every tracked file
  an mtime derived from its content, so `go test`'s cache key matches between checkouts and
  the shared build cache answers. A change reruns the packages it reaches and their importers;
  the rest report `(cached)`. Tests that start processes, run the installed git or reach
  servers or databases pass `-count=1` ([test cache](#test-cache)).
- Release checks repeat the tests once, without the race detector, against each archive and
  the dependency versions its go.mod requires. Their fresh consumers build with `-trimpath`,
  which keeps the scratch module cache's random path out of build-cache keys, so the
  dependencies come from the shared build cache; the job logs a cache summary per go command.

A single merge onto `main` reuses its pull request's passing jobs (go-ci-actions
`acceptance`). The PR run records a receipt naming the run, the commit it checked out,
the workflow and a digest of the repository variables; a run dispatched on a bot's pull
request branch records one for the branch head. The push uses it only on its first
attempt, when the merge sits on `before`, the push's base has a green run, and the PR
run's latest attempt passed within seven days on GitHub's merge of the head into `before`
(a dispatched run: on the head itself) with the push's tree. It then skips each job its
own scope selects that GitHub's jobs API shows passed in that run and whose PR selection
covers the push's: `tools/ci_release_checks.py` writes what each job checks, the modules
job's release checks and the cookbook lint included, for the push and again from the
merge's first parent, as the PR measured it. The rest run. Anything else, including a
squash or rebase merge, a direct push, a rerun, a PR run on an older `main`, or missing
evidence, runs the push's scoped jobs. Benchmark artifacts come from pushes only: a push
records them, reused or not, when it reaches a parse package with benchmarks or changes
`tools/bench-parse.sh`, which runs them. The script finds those packages under the `unit`
tag (`tools/bench-parse.sh --list`: today `parse`, `parse/core`, `parse/filter`,
`parse/internal/jsonkeys` and `parse/parsehttp`) and runs each one's benchmarks on its own, since `go test` profiles
a single package: the `parse-benchmarks-<commit>` artifact holds, per package directory,
`benchmarks.txt` (`-count=3`, for `benchstat`), `cpu.pprof` and `allocs.pprof`. The
profiles carry their symbols, so `go tool pprof -top cpu.pprof` reads one without the test
binary. The scope's package list for the job is a literal like every job's;
`tools/test_ci_parse_benchmarks.py` fails when it differs from the script's.
A newer run of the same PR cancels the older one. Runs on `main` all finish: each push is
measured from the last green run, so a cancelled one would hold the next push's base back,
and a push waiting behind another is replaced by the next, which covers both. Dispatched
runs finish.

One check runs apart from `ci.yml`: the `release notes` workflow ([Releases](#releases))
reads a pull request's commits and description rather than its tree. It runs on every pull
request and again on each edit of the description, which reruns nothing else; no push runs
it, so `ci.yml`'s scope never selects it and acceptance neither records nor requires it
(`tools/test_ci_release_notes.py` holds both).

The `nightly` workflow runs `ci.yml` on main's current commit once a day, and on
`gh workflow run nightly.yml -R ashtonian/pkg`: every suite and release check at full scope,
every parse unit test under the race detector, every unit test rerun (`UNIT_TEST_COUNT=-count=1`)
rather than replayed from the [test cache](#test-cache), and the benchmarks. It is the test
cache's backstop: a result a replay hid fails there, and it records no results of its own for
pull requests to replay. A scheduled run selects nothing when the last
green scheduled run was on the same commit; a dispatched one always runs everything. Its failure is a regression that scoping or the targeted race run let through, or a
new toolchain release breaking a floating Go or Node.js version: reproduce it with the failing
step's make target (`make test-parse-race-all` for a parse race report) and fix it on a branch.
GitHub turns off a public repository's schedules after 60 days without activity; turn the
workflow back on from the Actions tab.

A module that requires another module of this repository requires a released version of it. When it needs an unreleased change, release the dependency first, then raise the requirement.

Workspace development requires Go 1.27. The TypeScript SDK's targets also need Node.js
22.13 or later (its `engines` floor, which CI runs exactly alongside the newest LTS) and pnpm 12; `make release-please-check` needs both too. Every pnpm project here (`parse/sdk/ts`,
`tools/release-please`) pins the same pnpm release in `packageManager`. Both targets start with
`tools/check-pnpm.mjs`, which refuses any other pnpm on `PATH` and names the command that installs the pinned one
(`npm install --global pnpm@X.Y.Z`). Raise the pin in every project together.
Gowalker is its own module at
`github.com/ashtonian/pkg/gowalker`, released with `gowalker/v…` tags. Public modules contain source, tests,
licenses and user documentation; project plans, reviews and audits stay outside
the public repository. Its archive check runs the README quick start and usage programs verbatim.

### Test cache

`go test` replays a package's last passing result, shown `(cached)`, when its test binary
and its tests' inputs are unchanged. The binary's key covers the package, every package it
links across the workspace, the build flags and tags (`-race` included) and the toolchain.
The inputs are what the test process itself read: the environment variables it looked up,
and the files and directories it opened or stat'ed inside its module, keyed by path, size and
modification time (a directory by every entry's name and stat). Nothing else is seen: not
files outside the module, not what a subprocess such as `go list` or `git` reads, not the
network, a database or the clock.

The unit targets take `UNIT_TEST_COUNT` and replay; every other `go test` passes `-count=1`:
the integration targets (processes, servers, PostgreSQL), `test-gitx` (the installed git),
the release checks and module ZIP consumers (archives and the dependency versions their
go.mod requires), and the cookbook, example and tooling checks. `tools/test_ci_test_cache.py`
fails the change-scope job when a `go test` in the Makefile passes neither, when a release
check or a workflow step's `go test` omits `-count=1`, and when the nightly run would not
rerun the unit targets.

A unit test therefore depends only on what it reads itself, inside its module. A test that
needs more reads those inputs in-process or moves to an integration target. Parse's type-text
check is the example: it type-checks every package `go list ./...` finds, so it walks the
module's directories itself (`readModuleTree`), and a package added where no test imports it
reruns the check instead of replaying its last pass. `TestTransportFreePackages_ImportNoNetHTTP`
links every package it lists for `go list -deps`, so a change to what they import changes the
test binary.

Across checkouts the key matches because setup (go-ci-actions `test-input-mtimes: content`)
gives every tracked file an mtime derived from its bytes and every directory holding one a
fixed mtime. Without it, each checkout stamps files with the checkout time, and a test that
reads a file misses; it never replays a stale result. The self-hosted runners keep results in
the shared remote build cache, where a pull request reads `main`'s results and writes its own;
the macOS job keeps them in the GitHub cache. A local working tree replays from the local
`GOCACHE`.

| Symptom | Cause | Fix |
|---|---|---|
| A `(cached)` pass you suspect is stale | The test reads something the key does not cover | Rerun: `make <target> UNIT_TEST_COUNT=-count=1`. If it now fails, move what the test reads inside its module and the test process, or the test to an integration target |
| A package reruns on every run | Its tests read a file written during the job, or one newer than two seconds (untracked files keep their times), or an environment variable that differs per run. `t.TempDir` reads `GOTMPDIR`, so a wrapper that sets a fresh `GOTMPDIR` per run defeats replay for every test that uses it | Read the fixture from the tracked tree; leave `GOTMPDIR` unset or fixed; `GODEBUG=gocachetest=1 go test …` prints why each result missed |
| `go clean -testcache` in a job did not rerun a package | `go clean -testcache` expires results older than its stamp by the time the cache reports for each; the remote cache reports when it copied the result into the job | Pass `UNIT_TEST_COUNT=-count=1` |

### Dependency updates

Dependabot (`.github/dependabot.yml`) opens one grouped pull request a week per ecosystem,
for releases at least seven days old:

| Ecosystem | Directories | Commit prefix |
|---|---|---|
| Go modules | Every module in the repository, published or not: the modules `make docs-check` reads | `chore(deps)` |
| npm | The pnpm projects: the parse TypeScript SDK and the release-please runner. A `package.json` range is raised, never widened | `chore(deps)`, or `chore(deps-dev)` for development dependencies |
| GitHub Actions | The workflows, and each composite action under `.github/actions`. The owner's own actions skip the cooldown | `ci` |

`github.com/quasilyte/go-ruleguard/dsl` is left out: its version is the lint baseline's
([Lint baseline](#lint-baseline)). The configuration names every directory, and
`tools/test_ci_dependabot.py`, which the change-scope job runs, derives each list from the
tracked tree: it fails when a module, pnpm project or composite action is missing, or when a
listed directory holds none. A change that adds one adds its directory there.

A Dependabot pull request gets no checks of its own, and nothing runs its branch
automatically. That is deliberate: CI runs on self-hosted runners with the repository's
secrets, and a dependency update is code from outside the repository. For each one:

1. Review the diff: the requirement and lockfile or `go.sum` changes, and the linked
   release notes. A new dependency, or a module path that changed, needs more care than a
   version bump.
2. If the branch is behind `main`, comment `@dependabot rebase` on the pull request and wait
   for Dependabot's push.
3. Run CI on the branch: `gh workflow run ci.yml -R ashtonian/pkg --ref <head branch>` (the
   branch is `dependabot/…`). A dispatched run runs every job at full scope and reports on
   the head commit, so the pull request shows the checks.
4. Merge with a signed merge commit. The merge push reuses the dispatched run's passing jobs
   under the acceptance rules above, which for a dispatched run require the merge to have the
   head's tree: the branch already contained `main` (step 2). Otherwise the push runs its own
   scoped jobs. The merge subject decides the release as [Releases](#releases) describes:
   Dependabot's `chore(deps): …` releases nothing.

A new push to the branch, from a rebase or Dependabot's next update, needs a new run.

### Lint baseline

golangci-lint runs the lint baseline the owner's Go repositories share. Two files are
copies from the owner's private lint-baseline repository at commit `45fcf98f43fb`:

| Source | Copy here | Changed in the copy |
|---|---|---|
| `lint/golangci.yml` | `.golangci.yml` | The header comment, the ruleguard rules' path, and `issues.new-from-merge-base` |
| `lint/rules.go` | `tools/gorules/rules.go` | The comment naming where it comes from |

`tools/gorules` is a module of its own, outside `go.work`, whose `go.mod` requires
`github.com/quasilyte/go-ruleguard/dsl` at the version the baseline's header names. gocritic
type-checks the rules through `go/build`, which resolves that import only in a workspace
holding a module that requires it, and `go.work` holds the published modules alone. So
`make lint` writes `.lint.work` (ignored): `go.work` and `go.work.sum` copied, with
`tools/gorules` added. golangci-lint runs under it on Go 1.27.4, over the same packages and
tags as `go vet` and staticcheck; `GOLANGCI_LINT_VERSION` in the Makefile pins the release
(v2.15.0).

Only issues on lines changed since the merge base with `origin/main` fail
(`issues.new-from-merge-base`), so the baseline's linters judge new code while the findings
that predate it are fixed; `make lint LINT_ALL=1` lists them all, and once it reports none
the setting goes. CI's lint job checks out main's history for the merge base.

To take a newer baseline, copy both files again from that commit, keep the changes the table
lists, raise the dsl requirement in `tools/gorules/go.mod` if the header names a newer
version, update the commit here, and run `golangci-lint cache clean`: golangci-lint's cache
does not track the rules file.

| Failure | Cause | Fix |
|---|---|---|
| `ruleguard init error … could not import github.com/quasilyte/go-ruleguard/dsl` | golangci-lint ran outside `.lint.work`, or `tools/gorules/go.mod` lost the requirement | Run it through `make lint`; restore the `require` |
| `… origin/main …: unknown revision` or a merge-base error | The checkout has no `origin/main` or a shallow history | `git fetch origin main`; in CI, the lint job's checkout keeps `fetch-depth: 0` |
| ruleguard results don't change after the rules did | golangci-lint's cache | `golangci-lint cache clean` |

### gofmt and go fix

`make lint` fails on any tracked Go file `gofmt -l` lists, and on any rewrite `go fix -diff`
proposes for the packages and tags it vets. Unlike the baseline's linters, both judge all
code, not only changed lines. `make lint-cookbook`, `make parse-example-check`,
`make tool-check` and `make docs-check` run `go fix -diff` over their modules too. Both tools
come from Go 1.27.4, the release staticcheck and golangci-lint run on, so a newer
toolchain's fixers reach the code only when the Makefile's `GOFIX` moves to it.

`GOFIX` turns off three fixers:

| Fixer | Why it is off |
|---|---|
| `embedlit` | It flattens composite literals of embedded structs; the tests of promoted fields spell the embedding out on purpose |
| `omitzero` | It deletes `omitempty` from struct-typed fields, where only `encoding/json` ignores it. config's JSON Schema and parse's patch decoding read it, and their tests declare it on struct fields to pin that |
| `stditerators` | It turns `Value.Field(i)` walks into `Value.Fields()`, which also builds each field's `StructField`: about ten times the cost of the walk it replaces |

Read a fixer's rewrite before keeping it: `make fix` applies them as `go fix` writes them.

| Failure | Cause | Fix |
|---|---|---|
| `gofmt: run make fix` and a list of files | Those files are not gofmt-formatted | `make fix`, or `gofmt -w` the files |
| `make lint` stops on gofmt's `<file>:<line>:<col>: expected …` | gofmt cannot parse a tracked Go file, including one no build tag compiles | Fix the syntax; a deliberately malformed fixture belongs in a file without the `.go` extension |
| `make lint` prints a diff and fails after `go vet` | A `go fix` fixer has a rewrite for that code | `make fix`, review the result, commit it |
| A fixer's rewrite is wrong for the code | The fixer does not know a constraint the code has | Restructure the code so it states the constraint (in place of a loop `slices.Contains` would replace, `slices.ContainsFunc` compares a `[]byte` without copying it), or turn the fixer off in `GOFIX` and add it to the table above |

## Versioning

httpx tests require `-tags=unit` or `-tags=integration`; plain `go test ./...`
skips them. The Gin example, comparison benchmarks and release tooling keep
separate development modules outside the default workspace. The archive
check tests them against candidate releases without local replacements.

Every module is tagged with its directory as the prefix, as Go expects for a module below a repository's root: `config/vX.Y.Z`, `config/kongprovider/vX.Y.Z`, `httpx/codec/msgpack/vX.Y.Z`. `release-please-config.json` lists every released module under `packages`, keyed by its directory. Versions follow [semantic versioning](https://semver.org); while a module is at `v0`, a minor version may change its API. Each module's `CHANGELOG.md` lists what changed.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please) from [Conventional Commits](https://www.conventionalcommits.org) (`feat(config): …`, `fix(kongprovider): …`). Each commit counts toward the modules whose files it touches. release-please keeps one combined release pull request covering the modules with unreleased changes; merging it tags those modules, writes their changelogs and publishes a GitHub release per module. (One pull request per module is not possible here: a module below another, `config/kongprovider` below `config`, would need a release branch nested under the other's.) `feat` raises the minor version and `fix` the patch; a breaking change (`feat(config)!: …`) raises the minor version while the module is at `v0`.

Land feature branches with main as the first parent of a signed merge commit and a conventional subject describing the change. Its diff must contain the feature, rather than only a later main-to-feature synchronization. This preserves the module change and release classification when older feature commits precede the latest release boundary.

Release notes and versions come from main's first-parent history. The `release-please` workflow runs release-please through [`tools/release-please`](tools/release-please/README.md), whose `first-parent` plugin counts each pull request once, as its merge commit. The conventional commits on its branch hand that merge what they call for: the highest type (a `feat` merged as `fix(…)` still bumps the minor version), `BREAKING CHANGE` notes, `Release-As`, and closed issues. Each module counts only the branch commits that touched it, so a pull request that breaks one module and only adds a test to another releases the first and not the second: mark the breaking branch commit itself (`feat(httpx)!: …` or a `BREAKING CHANGE:` footer). The subject counts instead, as a floor, in every module the merge touched when no branch commit carries its type or `!`, and in one module when a branch commit touching it is older than its last release or not conventional. The merge subject is still the pull request's release-note entry, so make it the conventional summary of the whole change, with `!` when it breaks anything. The job warns when a branch commit raises a merge's type. Branch commits older than the last release, on a pull request merged after it, never reach release-please, so for such a pull request the merge subject alone decides its entry and bump. A merge whose subject release-please cannot parse leaves its branch commits listed instead, so nothing is dropped. The stock `release-please-action` cannot load the plugin and refuses this repository's config. From a full clone, `pnpm -C tools/release-please install --frozen-lockfile`, then `GITHUB_TOKEN=… node tools/release-please/run.mjs --repo ashtonian/pkg --dry-run`, shows the release PR the current main would produce.

release-please drops a commit its parser cannot read, and with it the commit's type, `BREAKING CHANGE` notes and closed issues, and nothing fails. So every commit a pull request brings to main follows two rules:

- release-please must parse it. A body line whose first word runs straight into `(` reads as a footer token with a scope, and a second `(` or a line break before its `)` fails the whole message: `parse_decimal(CAST('10.50' AS BLOB)) was '10.5'` at the start of a line cost #287 its breaking note (#289). Reword such a line, or start it with another word.
- A commit, or each entry of a message that holds several, carries at most one `BREAKING CHANGE` (or `BREAKING-CHANGE`) footer or paragraph: release-please keeps only the last. State every break in one.

A pull request's `BEGIN_COMMIT_OVERRIDE … END_COMMIT_OVERRIDE` block, which release-please reads in place of each of its commits ([tools/release-please](tools/release-please/README.md)), follows the same rules, and one more: release-please starts a new entry only at a blank line followed by a header without `!` (`feat(…): `, `fix(…): `, or another of the standard types). A further `type(scope)!: ` header is read into the entry before it, which then loses its own breaking note or takes the next one's. Put each further breaking entry in its own `BEGIN_NESTED_COMMIT … END_NESTED_COMMIT` block, one entry per block.

The `release notes` workflow checks this on every pull request, and again when its description changes. It reads, with release-please's own parser, each commit the pull request brings and the override, and fails naming each commit or override entry whose type, notes or references release-please would lose. A merge carries no release note of its own, so merges are not read; nor are a commit and its revert on the same branch, which count for nothing. With an override, a commit's problems only warn: release-please reads the override in its place. Run the check before pushing: `pnpm -C tools/release-please install --frozen-lockfile`, then `node tools/release-please/check-commits.mjs --base origin/main`. On main, the release job warns naming each merge whose entry lacks what release-please could not read of one of its branch commits; before the release PR merges, give that pull request's description an override stating its whole change. Once the release is published, repair it by hand: add what release-please would have written (the entry under its type's section, the `BREAKING CHANGE` note, the references) to that version's section of the module's `CHANGELOG.md`, in a `docs` commit of a feature pull request, which releases nothing, never in a release commit, and edit the GitHub release's notes to match the section.

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
A README that pins its module's version for `go run` or `go install` wraps
each pinned block in `<!-- x-release-please-start-version -->` and
`<!-- x-release-please-end -->` and lists the README as a `generic` extra file
of its package, so each release moves the pin; `make release-please-check`
fails on a pin outside the markers.
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

Parse requires Go 1.27 and publishes as `github.com/ashtonian/pkg/parse`, with `parse/v…` tags. CI runs unit tests (under the race detector nightly, and on pull requests only the tests tagged `concurrency`), SQLite integration beside PostgreSQL 17/18 with PostGIS 3.6 under the race detector, and the TypeScript SDK's shared corpora. modernc's SQLite builds without race or checkptr instrumentation: it is C translated to Go, and instrumenting it made the SQLite-backed suites about 30 times slower without checking this repository's code. The SDK release check installs its tarball in a fresh consumer and checks public declarations, runtime subpaths and self-contained JavaScript sourcemaps. The SDK package version follows the Parse release component. CI stores the benchmark text, CPU profile and allocation profile of every parse package with benchmarks, from main pushes that change the Go code they build and from the nightly run.

`eval "$(make -s parse-postgres-up)"` starts PostGIS 3.6 on PostgreSQL 18 in Docker, the server CI provisions, waits until it accepts connections, and exports `PARSE_POSTGRES_DSN` and `PARSE_POSTGIS_DSN` pointing at it; `make parse-postgres-down` removes it. For an existing server, set the two variables yourself. Then run `make test-parse-integration`. Each case gets a fresh schema, first on the search path of every connection it opens. A requested server that is unavailable fails; an unrequested PostgreSQL case skips, unless `PARSE_REQUIRE_PG=1`, which CI's `parse-postgres` job sets so that a missing variable fails the job rather than skipping its PostgreSQL coverage. The release check clears the DSN variables and `PARSE_REQUIRE_PG`. `make parse-release-check` verifies a candidate archive independently of the workspace; publication additionally requires clean public-proxy installation of the immutable release.

| Variable | Default | Description |
|---|---|---|
| `PARSE_POSTGRES_DSN` | unset | The PostgreSQL server the integration tests run against, in either form pgx reads: a URL (`postgres://parse:parse@127.0.0.1:55432/parse?sslmode=disable`) or keyword/value pairs (`host=127.0.0.1 port=55432 user=parse password=parse dbname=parse sslmode=disable`). Unset, its tests skip |
| `PARSE_POSTGIS_DSN` | unset | The PostGIS server of the geo tests, in either form; the fixture enables the extension in its `public` schema |
| `PARSE_REQUIRE_PG` | unset | `1` fails a test whose server's variable is unset instead of skipping it |
| `PARSE_TEST_TIMEOUT` | `30s` | A Go duration bounding the fixture's statements, connecting included, and each `parsepgx` PostgreSQL test; raise it on a contended host |
| `PARSE_PG_IMAGE` | `postgis/postgis:18-3.6` | Make variable: the image `parse-postgres-up` runs, as `linux/amd64`; postgis/postgis publishes no arm64 image, so arm64 hosts emulate it |
| `PARSE_PG_NAME` | `parse-postgres` | Make variable: the container's name, which `parse-postgres-down` removes |
| `PARSE_PG_PORT` | `55432` | Make variable: the server's port on 127.0.0.1 |
| `PARSE_PG_READY_SECONDS` | `180` | Make variable: how many seconds from the container's start `parse-postgres-up` waits for the server to accept connections. A server that stops before then, or is not ready by then, fails the target |

`parse-postgres-up` runs [`tools/postgres-up.sh`](tools/postgres-up.sh), as `cookbook-postgres-up` does. It writes the exports alone to stdout and everything else to stderr. On a failure it prints the container's log and removes the container, so a rerun starts cleanly; it removes only a container it created.

- **`<name>: stopped while starting: exited (exit code N)`**, then the log: the server exited, and the log says why. Correct the cause and rerun.
- **`<name>: not ready after Ns`**, then the log: the server was still starting. An arm64 host emulates the image and starts it slower, more so under load: raise `PARSE_PG_READY_SECONDS`.
- **`Conflict. The container name "/parse-postgres" is already in use`**, then `<name>: docker run failed`: a server of that name is up, perhaps one other work shares, and the target leaves it running. Use it (with its exports), or choose another `PARSE_PG_NAME` and `PARSE_PG_PORT`; `make parse-postgres-down` removes it.
- **`port is already allocated`**, then `<name>: docker run failed`: another process holds `PARSE_PG_PORT`; choose another.

## Parse SDK publication

The `release-please` workflow builds `@ashtonian/parse` from a published stable `parse/vX.Y.Z` tag on main. It requires the tag, release manifest and SDK package to agree on the version, verifies the packed consumer, and retains the exact tarball before publishing. Repeating a run accepts an existing registry version only when its package integrity matches: `tools/check-parse-sdk-registry.mjs needs-publish` reads npm's version document (`/@ashtonian%2Fparse/X.Y.Z`), with a 30-second request deadline.

npm scans packages before making them installable, so successful publication can precede availability by several minutes, and availability is not one fact. Each client resolves a version from its own form of the package metadata, which npm's CDN caches per form (`cache-control: public, max-age=300`, `vary: accept`): pnpm 12 reads the abbreviated document (`Accept: application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*`), npm 12 the full one (`Accept: application/json`), and the version document is not cached at all. A probe of one form says nothing about another, so readiness is the consumer's own installation. The `wait` step (`tools/check-parse-sdk-registry.mjs wait`) runs the `pnpm add` the consumer check runs, from a new project, store and metadata cache each attempt, until it installs the exact version; then npm's version document must name the bytes pnpm locked, and those must be the retained package's. The consumer check (`tools/check-parse-sdk-release.sh --registry X.Y.Z`) installs through the same code and retries as well, since a cached copy can still lag the one the wait read.

Both retry only pnpm's reports that the registry view it read lacks the version: `ERR_PNPM_NO_MATCHING_VERSION` (npm's `ETARGET`), a 404 for the package metadata or the tarball (npm's `E404`), and a version document not found after pnpm installed. The pause starts at 5 seconds and doubles to 30; no attempt starts after the bound, so a version that never appears fails once the attempt running at the bound ends. Every other failure stops at once: authentication and other HTTP errors, malformed metadata, integrity mismatches, consumer failures, and a `pnpm add` still running after 5 minutes. Neither step republishes the package.

| Variable | Default | Description |
|---|---|---|
| `PARSE_SDK_REGISTRY_WAIT_SECONDS` | `900` | Whole seconds the `wait` step retries installing the published version before it fails; npm's processing time. In the workflow, the step's `timeout-minutes: 16` stops it beyond that |
| `PARSE_SDK_REGISTRY_RETRY_SECONDS` | `300` | Whole seconds `check-parse-sdk-release.sh --registry` retries a version not yet visible; the metadata cache's lifetime. `0` tries once. In the workflow, the step's `timeout-minutes: 10` stops it beyond that |
| `PARSE_SDK_DIR` | `parse/sdk/ts` | The SDK whose tarball the release check packs and whose TypeScript compilers it runs |

Each install passes pnpm `--ignore-scripts`, the public registry, `--config.fetch-timeout=30000`, `--config.fetch-retries=1` and `--config.minimum-release-age=0`, which admits the version however recently it was published.

Configure npm trusted publishing for repository `ashtonian/pkg` and workflow filename `release-please.yml` in the package's npm settings. The workflow uses a GitHub-hosted runner, and `pnpm publish` exchanges the job's OIDC token with npm; it does not require a stored npm token. Creating the package for the first time requires an authenticated package owner. Dispatch the immutable tag with `publish=false`, download its retained tarball, publish those exact bytes with `pnpm publish <tarball> --access public --registry=https://registry.npmjs.org/`, then configure the trusted publisher for subsequent releases. Follow [npm's trusted publisher requirements](https://docs.npmjs.com/trusted-publishers/).

The repository owner can prepare an already published tag without registry writes (the dispatch default):

```bash
gh workflow run release-please.yml -R ashtonian/pkg -f tag=parse/vX.Y.Z
```

Add `--ref parse/vX.Y.Z -f publish=true` to publish through the configured trusted publisher and verify registry consumption. Publication requires the workflow's commit to equal the selected release commit so npm provenance identifies the actual package source. A build-only run verifies registry consumption when that exact package already exists; an absent version leaves publication and registry acceptance unexecuted. When release-please creates a Parse release, its output starts the publishing job directly in the same workflow, including when it uses `GITHUB_TOKEN`. Manual dispatch supports artifact preparation and retries; it does not create a release PR or a GitHub release. For tags that predate this workflow's publication support, use the authenticated first-publication procedure above. A tag whose SDK has a `package-lock.json` and no `packageManager` field is built with npm: dispatch it with `--ref <tag>`, which runs that tag's own workflow, because this workflow sets pnpm up from the release's `packageManager` field. On authentication failure, verify the npm publisher's repository and workflow filename. On an integrity mismatch, inspect the retained artifact and registry version; do not change an existing tag or overwrite a published version.

The registry steps log each retry as `@ashtonian/parse@X.Y.Z: not installable from https://registry.npmjs.org/ yet (pnpm add exited with status 1 (ERR_PNPM_…)); retrying in N s`, with pnpm's own output above it.

- **`… not installable from https://registry.npmjs.org/ within 900 s (N attempts; last: …); inspect npm processing/review status before retrying acceptance`** (the `wait` step): npm has not made the version installable. Check the package's page and the publisher's npm notifications for a held or rejected version. Once a fresh `pnpm add @ashtonian/parse@X.Y.Z` installs it, rerun the failed jobs or dispatch the tag (`publish=false` accepts the existing version).
- **`… within 300 s …`** (the consumer step, after `wait` passed): a cached copy of the metadata or tarball lagged past its lifetime. Rerun the failed jobs; the `wait` step passes at once and the consumer retries again.
- **`installing @ashtonian/parse@X.Y.Z from …: pnpm add exited with status 1 (ERR_PNPM_…)`**: a failure that is not a missing version, such as `HTTP status client error (403 Forbidden)` or a 5xx from the registry; pnpm's output above it says which. Nothing was retried. Rerun once the registry answers.
- **`… published version has different bytes`**: the registry's version is not the retained tarball. Treat it as the integrity mismatch above.
- **`TS2305: Module '"@ashtonian/parse"' has no exported member …`** from `check-parse-sdk-release.sh --registry` on an older release: the consumer program exercises the current SDK's API. Run that release tag's own `tools/check-parse-sdk-release.sh` against its SDK instead.
