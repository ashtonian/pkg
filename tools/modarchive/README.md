# tools/modarchive

`modarchive` writes module directories from the working tree as a
file-based Go module proxy, so a release check can build each published
module the way a fresh consumer would: outside the workspace, through
`GOPROXY`, at the version release-please will publish. Each archive is made
with `golang.org/x/mod/zip`, so module boundaries, file selection and size
limits are the go command's own. Before writing an archive, the tool
refuses one that would publish internal planning documents.

It is a development tool: a module of its own that is never published, run
by the `tools/check-*-release.sh` scripts.

## Usage

```sh
cd tools/modarchive
GOWORK=off go run . -out "$tmp/proxy" \
  -manifest "$repo_root/.release-please-manifest.json" \
  "$repo_root/httpx" "$repo_root/httpx/oauth2"
```

Then point the consumer build at the proxy, ahead of the real one:

```sh
GOWORK=off GOFLAGS=-mod=mod GONOSUMDB=github.com/ashtonian/pkg/httpx \
  GOPROXY="file://$tmp/proxy,$(go env GOPROXY)" go build ./...
```

The archives describe uncommitted source, so their checksums can differ
from an existing tag's: callers exempt those modules from the checksum
database with `GONOSUMDB` and drop their lines from copied `go.sum` files.

## Configuration

| Flag | Type | Default | Description |
|---|---|---|---|
| `-out` | path | — (required) | Proxy directory to write. Created if missing; files for the same module and version are replaced. |
| `-manifest` | path | — (required) | The release-please manifest. Each module directory is looked up by its path relative to the manifest's directory, for example `httpx/codec/proto`. |
| `-initial-version` | canonical semver | `v0.1.0` | The version written for a module whose manifest entry is `0.0.0`, the bootstrap marker before its first release. |
| `MODULE_DIR ...` | paths | — (at least one) | Module roots to archive, in order. |

The tool reads no environment variables. `git` comes from `PATH`, and runs
with `LC_ALL=C` so its diagnostics are matched independent of locale.

## Inputs

Each module directory needs:

- a `go.mod` with a module path and no `replace` directives (a published
  module must resolve without them; use `go.work` for local development);
- a non-empty module-local `LICENSE`;
- an entry in the manifest.

Inside a Git working tree, the archive holds the files Git lists as tracked
or untracked-but-not-ignored, with their current, possibly uncommitted,
contents: ignored build output is left out, as a tagged release would leave
it out. A tracked file deleted from the working tree is omitted, and so is
any file beneath a directory that has been replaced by a symlink; the tool
never reads through a symlinked directory into another tree. Outside a Git
repository it archives the directory with `zip.CreateFromDir`. Either way,
nested modules (directories with their own `go.mod`) and `vendor` content
follow the go command's rules.

## Outputs

For each module, under `-out`:

| File | Content |
|---|---|
| `<escaped module path>/@v/<version>.zip` | The module archive |
| `<escaped module path>/@v/<version>.mod` | The module's `go.mod`, verbatim |
| `<escaped module path>/@v/<version>.info` | `{"Version": "<version>", "Time": "<now, UTC>"}` |
| `<escaped module path>/@v/list` | `<version>`; the proxy serves only the version this run wrote |

Module paths are escaped as `GOPROXY` requires (`module.EscapePath`:
uppercase letters become `!` plus the lowercase letter).

On standard output, one line per module, tab-separated: module path,
version, absolute source directory. Release scripts read the candidate
versions from it, for example with
`awk -F '\t' '$1 == module { print $2 }'`.

Exit status is 0 on success. On the first failure the tool prints
`modarchive: <error>` to standard error and exits 1. An archive it could
not finish, or that failed the content check, is removed; archives written
before the failure remain.

## API / Interfaces

A command-line interface only; the package is `main`. The output layout is
the file-based module proxy protocol (`go help goproxy`).

## Build / Run / Test

From the repository root, `make tool-check` runs, with `GOWORK=off`,
`go mod tidy -diff`, `go vet`, the race-enabled tests and staticcheck. From
this directory:

```sh
GOWORK=off go test -race ./...
```

The tests are untagged and build fixture modules under `t.TempDir()`.
Callers: `tools/check-httpx-release.sh`, `check-gowalker-release.sh`,
`check-jsonvalue-release.sh`, `check-native-release.sh` and
`check-parse-release.sh`.

## Operations

A one-shot command: no service, health endpoint or metrics. SIGINT and
SIGTERM cancel its context, which stops a running `git` command; that
module's unfinished archive is removed. Callers write to a fresh `mktemp -d`
directory and delete it on exit, so proxy trees do not accumulate.

Archive sizes are bounded by `golang.org/x/mod/zip`: at most 500 MiB for
the archive and for its uncompressed contents, and 16 MiB each for `go.mod`
and `LICENSE`. The content check reads each `.go`, `.md` and `.txt` file of
the finished archive into memory, one at a time.

| Error | Cause | Fix |
|---|---|---|
| `no release-please manifest entry` | The directory's path relative to the manifest is not a key in it | Add the module to the release-please config and manifest, or pass the right directory |
| `published module must not contain replace directives` | `go.mod` has a `replace` | Remove it; use `go.work` for local development |
| `module-local LICENSE: …` / `module-local LICENSE is empty` | No usable `LICENSE` in the module root | Copy the repository `LICENSE` into the module |
| `<file>: internal planning artifact in published module` | A file at a reserved internal-document location (see Security) | Move it out of the module |
| `<file>: internal RFC reference in published module` | A `.go`, `.md` or `.txt` file mentions a zero-padded `RFC-0xxx` design document | Remove the reference; IETF RFCs such as RFC 9110 are allowed |
| `identify archive working tree: …` | `git rev-parse` failed for a reason other than "not a git repository" | Check that `git` is on `PATH` and the directory is readable |
| `archive source is not a Git working tree` | The directory is inside a `.git` directory or a bare repository | Pass the module's working-tree directory |
| `initial version … is not a canonical semantic version` / `version … is not canonical` | `-initial-version` or a manifest version is not of the form `v1.2.3` | Fix the flag or the manifest entry |
| An error naming a file, from `golang.org/x/mod/zip` | A file name the module zip format refuses, a case-insensitive name collision, or a size limit | Rename or remove the file |

## Security

The tool reads only the module directories it is given, never follows a
symlinked directory, and writes only under `-out`. It runs `git` locally and
makes no network requests.

It keeps internal planning material out of published modules by refusing
the archive rather than filtering it, so a release never differs silently
from its source tree. Refused paths are any component named `.llm`,
`.claude`, `.codex`, `_review` or `_reviews`; anything under `docs/spec/`,
`docs/plans/`, `docs/reports/`, `docs/reviews/`, `docs/audits/`,
`docs/adoption/` or `docs/promotion/`; files named `audit.md`,
`audit-report.md`, `report.md`, `review.md`, `final-review.md`,
`implementation-plan.md`, `reflection-consolidation.md`, `promotion.md` or
`promotion-checklist.md`; and file names or `.go`, `.md` and `.txt` contents
containing a zero-padded internal RFC identifier (`RFC-0001`). Matching is
case-insensitive. The check runs on the finished archive, after nested
modules are excluded, so a nested module's documents are checked when that
module is archived.

## External Dependencies

- `golang.org/x/mod` — `modfile` parsing, module path and version checks,
  path escaping, and `zip` creation. Pinned in `go.mod`.
- `git` on `PATH`, when a module directory is inside a working tree.

## Code Map

| File | Responsibility |
|---|---|
| `main.go` | Flags, manifest lookup, version choice, the proxy files and the index |
| `worktree.go` | The file list from Git: tracked and unignored files, symlinked directories skipped |
| `content.go` | The published-content check on the finished archive |
| `main_test.go`, `worktree_test.go` | Fixture-module tests of archiving, refusal and file selection |
