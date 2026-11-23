# tools/doccheck

Holds the Markdown of every module in the workspace to the code it documents, and the modules' Go comments to whole sentences. A renamed or removed API, a moved guide section, a file a code map still lists, or a sentence left broken where a reference was cut from a comment fails CI instead of surviving in prose. It is a development module, never published, run by `make docs-check`.

## What it checks

For every module `go.work` uses, over its Markdown (every `.md` file in its tree but changelogs, outside `testdata`, `node_modules` and the trees of nested modules) and, for comment sentences, its Go files:

| Check | Rule |
|---|---|
| Identifiers | Each code span's Go identifiers resolve against the module's exported API, read from its non-test Go files under every build constraint: `pkg.Name` where `pkg` names an importable package of the module, `pkg.Type.Member` and `Type.Member` (a method or field, declared or promoted through an embedded type; `Type[T]` as written for a generic type) where `Type` is a type it declares, and a functional option `WithX` named on its own, which may be any workspace module's: a provider's guide names the options of the module it plugs into. `Name*` names a family by its prefix. A qualifier naming no package of the module, or a module package named like a library one of its packages imports (a `msgpack` codec over a msgpack library), is not checked. Fenced code blocks are not read: quick starts are compiled by the modules' own README tests. |
| Links | Each relative link and reference definition outside code names a file or directory in the repository, and its fragment, into Markdown, names a heading (by GitHub's anchor: the heading's text lowercased, punctuation dropped, spaces as hyphens, `-1`, `-2` for repeats) or an `<a id>` / `<a name>` anchor. Absolute URLs are not fetched. |
| Code maps | In a table under a heading containing "Code map" or "Key files", each first-cell code span with a file extension or a slash is a path, relative to the Markdown file's directory or, for a guide that maps its module, to the module's: it names a file or directory, or is a pattern that matches one. |
| Comment sentences | No line comment of a Go file, tests included, opens on a `.`, `,`, `;` or `:` standing alone (followed by a space or nothing) or ends in a space and a full stop: what removing a reference from the middle of a sentence leaves. A line indented past the comment's first space is code, as in a doc comment's code block, and a line may open on a file suffix or a path (`.json`, `..data`). Files with a `// Code generated … DO NOT EDIT.` header and `/* */` comments are not read. |
| Files listed (opt-in) | Each non-test Go file that declares something is listed by its package's code map: the nearest README, from its directory up to the module's, that has one. A directory entry lists every file under it. A file holding only the package comment (`doc.go`) needs no row. |
| Exports named (opt-in) | Each exported top-level name of an importable package (not `main`, not under `internal/`) appears in a code span or code block of the module's Markdown, or belongs to a family a span names by its prefix. |
| Options tabled (opt-in) | Each option of an importable package (an exported function returning, or a variable of, a type named `…Option`) is named in a table row outside a code map: an option table, or a row linking to the guide that holds one. |

## Configuration

| Flag | Type | Default | Description |
|---|---|---|---|
| `-root` | path | `.` | The repository root: the directory holding `go.work` |
| `-allow` | path | `tools/doccheck/allow.json` under the root | The allow list |
| `-timeout` | duration | `2m` | Bound on the whole check, the `go work edit -json` it runs included |

The allow list, `allow.json`, is keyed by module directory:

```json
{
  "parse": {
    "reverse": true,
    "identifiers": {"pkg.Name": "why the prose may name what the module does not declare"},
    "unlisted_files": {"parse/x/*.go": "why no code map lists these"},
    "unnamed_exports": {"filter.Legacy*": "why no Markdown names these"},
    "untabled_options": {"filter.WithLegacy": "why no table row names it"}
  }
}
```

`reverse` opts a module into the three reverse checks. Keys are exact or `path.Match` patterns: references as the code span writes them, files from the repository root, exports as `package.Name`. Every entry needs a reason, and an entry that matches nothing is a finding, so the list only shrinks as the documentation catches up. Unknown members and modules outside the workspace are refused.

## External dependencies

The Go toolchain, for `go work edit -json`. The tool itself uses the standard library only; its tests use testify.

## Build, run and test

```bash
make docs-check                                  # tidy, vet, race tests, staticcheck, then the check
cd tools/doccheck && GOWORK=off go run . -root ../..
cd tools/doccheck && GOWORK=off go test -race ./...
```

It runs outside the workspace (`GOWORK=off`) and reads the root's `go.work` explicitly. CI runs it in the modules job whenever a change touches any workspace module's tree or the tool (`tools/ci_release_checks.py`).

## Operations

It prints one finding per line, `path:line: message` (or `path: message` for a file, a package or the allow list), and exits 1 when there is any, 2 when it cannot run.

- `X names nothing M declares`: rename the reference to the identifier's current name, or drop it. If the prose deliberately names something the module does not declare, allow it with the reason.
- `link …: … does not exist` / `has no heading or anchor`: the target or its heading moved. Anchors are GitHub's slugs; check the target heading's exact text.
- `code map entry …: no such file`: the file was renamed, split or removed; update the row.
- `… code map does not list it` / `no README code map covers its directory`: add a row naming the file to the nearest README's code map.
- `… is exported and no Markdown names it`: name it in the package's README, or in a guide; or allow it with the reason no reader needs it.
- `… is an option no table row names`: add a row to the option table its builder's guide holds, or a link row to that guide.
- `a comment line opens on … ` / `ends in a lone full stop`: a piece of the sentence was cut; rewrite the sentence whole, joining the line to the one before it or saying what the cut reference said.
- `entry … matches nothing; remove it`: the documentation caught up; delete the allow entry.

A run over the whole workspace parses every module's Go files once and every Markdown file once: seconds, growing linearly with the tree.

## Security

It reads the repository and runs `go work edit -json`; it writes nothing, opens no network connection and fetches no link.

## Code map

| File | Responsibility |
|---|---|
| `main.go` | Flags, exit status |
| `check.go` | Module discovery, the checks, the allow list, findings |
| `api.go` | A module's exported API from its Go files: declarations, members, embedding, imports |
| `comments.go` | The comment-sentence check, over each Go file's line comments |
| `markdown.go` | The Markdown a check reads: code spans, links, headings and their anchors, tables by section |
| `check_test.go` | Each check on a fixture workspace; anchors and code spans |
| `allow.json` | The allow list |
