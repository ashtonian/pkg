# gitx

## Overview

`gitx` is the git-side plumbing a repository's own checks need when several people or
agents share one branch and merge or rebase onto it many times a day:

- the **`gitx` package** lists a repository's files as git records them (`Tracked`,
  `Untracked`, `TrackedRegular`), so a check reads the same set in every clone, a sparse
  one included;
- **`conflictmarkers`** (a package and a command) fails on a merge-conflict marker left
  in a tracked file, each kind on its own, so a hand resolution that deleted some marker
  lines and missed others is caught before it lands;
- **`mergeinserts`** (a package and a command) is a git merge driver for hand-edited
  markdown that keeps both sides' insertions at one point — the appended note, table
  row or log entry that git refuses to order — and leaves every other conflict exactly
  as git does.

Everything is the standard library plus the `git` binary on `PATH`.

## Quick start

Check a repository for leftover markers, from its root:

```sh
go run github.com/ashtonian/pkg/gitx/cmd/conflictmarkers@v0.1.0 -root .
```

A repository whose machines write records into one directory, where people still edit
the markdown beside them, skips the records and keeps the markdown:

```sh
go run github.com/ashtonian/pkg/gitx/cmd/conflictmarkers@v0.1.0 -root . -skip records/ -keep .md
```

Install the merge driver into the clone and route markdown through it:

```sh
GOBIN="$PWD/.tools" go install github.com/ashtonian/pkg/gitx/cmd/mergeinserts@v0.1.0
git config --local merge.keep-inserts.name "markdown: keep both sides' insertions at one point"
git config --local merge.keep-inserts.driver 'if [ -x .tools/mergeinserts ]; then exec .tools/mergeinserts %O %A %B %L %P %S %X %Y; else exec git merge-file --marker-size=%L -L %X -L %S -L %Y %A %O %B; fi'
printf '%s\n' '*.md merge=keep-inserts' >> .gitattributes
```

The driver command runs the built tool when the clone has it and git's own merge
otherwise, so a fresh clone or a linked worktree conflicts as git does rather than
leaving one side's text unmarked. A `make git-config` target that runs the three
commands above, and a `make gate` that depends on it, keep every clone configured.

## API / interfaces

```go
import "github.com/ashtonian/pkg/gitx"

func Tracked(ctx context.Context, root string) ([]string, error)        // every index entry
func Untracked(ctx context.Context, root string) ([]string, error)      // neither tracked nor ignored
func TrackedRegular(ctx context.Context, root string) ([]string, error) // no links, no submodules, merges once
```

Paths are slash-separated, relative to the root and in git's order. A path a sparse
checkout leaves out of the working tree is still listed, so a consumer that reads the
files decides what an absent one means. An error names the `ls-files` arguments, the
root and git's own message.

```go
import "github.com/ashtonian/pkg/gitx/conflictmarkers"

func Markers(data []byte) []Marker
func Check(fsys fs.FS, paths []string, read func(path string) bool) (Result, error)
```

`Markers` finds every line git itself counts as a conflict marker. `Check` reads the
paths the predicate admits from the file system and returns a `Finding` per marker,
`file:line: kind` in its text form, plus the number of text files read.

```go
import "github.com/ashtonian/pkg/gitx/mergeinserts"

func Merge(ours, base, theirs string, markerSize int, st Style, l Labels) (Result, error)
```

`Merge` runs `git merge-file` over the three files and rewrites its output: a conflict
whose common-ancestor section is empty becomes ours's lines then theirs's; the rest are
written with git's markers in `StyleMerge` or `StyleDiff3`.

### Commands

| Command | Role | Exit status |
|---|---|---|
| `cmd/conflictmarkers` | a check, run from the repository root | 0 no findings, 1 findings, 2 could not run |
| `cmd/mergeinserts` | a merge driver git runs, not a command people run | 0 merged, 1 conflicts remain, 2 neither the driver nor its fallback merged |

`conflictmarkers` prints one `file:line: kind` line per finding, in git's file order,
then a summary line. `mergeinserts` prints one stderr line per file where it kept
insertions.

## Configuration

`conflictmarkers` flags:

| Flag | Type | Default | Description |
|---|---|---|---|
| `-root` | string | `.` | Repository root; `git ls-files` runs there and files are read from its working tree. |
| `-skip` | string, repeatable | none | A path prefix whose files are not read. |
| `-keep` | string, repeatable | none | A file-name suffix read even under a skipped prefix. |

It reads no environment.

`mergeinserts` is configured in git:

| Input | Type | Default | Description |
|---|---|---|---|
| `merge.keep-inserts.driver` (git config) | string | unset | The driver command above. |
| `merge=keep-inserts` (`.gitattributes`) | attribute | none | The files merged through the driver; `*.md` is the usual pattern. |
| `merge.conflictStyle` (git config) | `merge`, `diff3`, `zdiff3` | `merge` | How an unresolved conflict is written; `zdiff3` is written as `diff3`. |

The positional arguments are the ones git substitutes in `mergeinserts %O %A %B %L %P %S %X %Y`:

| Argument | Meaning |
|---|---|
| `%O` | the common ancestor's version (a temporary file) |
| `%A` | the current side's version; the result is written back here |
| `%B` | the other side's version |
| `%L` | the conflict-marker length git uses (7 unless `conflict-marker-size` is set) |
| `%P` | the path being merged, for the stderr line |
| `%S`, `%X`, `%Y` | the ancestor's, the current side's and the other side's labels, shell-quoted by git |

## What counts as a marker

A marker is a line git itself counts as one: seven of the same marker character at
the start of the line, then whitespace or the end of the line.

| Line | Finding | Where a merge writes it |
|---|---|---|
| seven `<`, then a label | `opening conflict marker` | before this side's text |
| seven `\|`, then a label | `base-section conflict marker` | before the common ancestor's text, under `merge.conflictStyle=diff3` or `zdiff3` |
| seven `=` | `conflict separator` | before the other side's text |
| seven `>`, then a label | `closing conflict marker` | after the other side's text |

Each kind is a finding on its own, so a lone leftover is caught. The base-section line
is the one a resolution written for the two-sided style misses. Eight or more of the
character, a run with text joined to it, and a run that does not start the line are
ordinary text, as they are to git. Binary files (a NUL byte in the first 8000 bytes, as
git decides), symbolic links and submodules are not read; a tracked file absent from
the working tree is skipped; a path with an unresolved merge is read once, from the
working tree, so a resolution is checked before it is staged.

## What the driver keeps

| Merge | Current side (`%A`) | Other side (`%B`) | Kept order |
|---|---|---|---|
| `git rebase` / `git pull --rebase` | upstream | the commit being replayed | upstream's text, then yours |
| `git merge` | your branch | the merged branch | yours, then theirs |

An edit to a line the other side also changed, or a deletion against an edit, still
stops the merge. The driver merges with markers longer than any run of marker
characters in the inputs, so a markdown setext underline or a documented conflict in
the text never parses as one of its own markers.

## External dependencies

The `git` binary on `PATH`: `git ls-files` lists the files, `git merge-file` does the
three-way merge, and `git config` reads the conflict style. Otherwise the standard
library. Go 1.27 or later.

## Build / run / test

```sh
go test -race ./...
go vet ./...
```

The `gitx` tests build real repositories in temporary directories with the system and
global git configuration switched off: an index holding a plain file, a path with a
newline in it, a symbolic link, a submodule and a path at three merge stages, and
ignore rules over a directory and a pattern. The `conflictmarkers` tests run over
in-memory trees. The `mergeinserts` tests run real `git merge-file` over table cases,
and an end-to-end test rebases in a scratch repository through the exact driver command
documented above, with the test binary as the tool: insertions merge, an edit conflict
stops with markers, and without the tool the fallback stops with markers too. Without
`git` on `PATH` the tests that need it skip.

## Operations

- **`opening conflict marker`, `closing conflict marker`:** a conflict was resolved by
  hand and one side's marker line survived. Read the lines around it against both sides
  (`git log -p -- <file>`) and delete the marker. If the text between the markers was
  never merged, merge it first.
- **`base-section conflict marker`:** the resolution kept both sides but not the
  ancestor's section header. Under the diff3 style, the lines from this marker to the
  separator are the common ancestor's text. Delete the marker and that section, and keep
  what the two sides need.
- **`conflict separator`:** a lone line of seven `=`. If it is a resolution's leftover,
  delete it. If it is a markdown setext heading underline, use an ATX heading (`#`) or a
  rule of a different length.
- **A document that has to show a conflict:** indent the example lines. A marker counts
  only at the start of a line.
- **`conflictmarkers` exits 2 with a `git ls-files` error:** run it from inside the
  repository, with git installed, or point `-root` at the repository.
- **A rebase stopped on a markdown file:** the conflict is a real one; both sides changed
  the same or adjacent lines. Resolve it as any other, then run the marker check, which
  refuses a leftover.
- **Insertions were not merged:** check `git config --get merge.keep-inserts.driver` and
  `git check-attr merge -- <file>`. An empty driver means the clone was never configured;
  `merge: unspecified` means the path carries no attribute. A linked worktree shares the
  driver definition but has no `.tools/mergeinserts` of its own until the tool is
  installed there, and merges as git does until then.
- **The kept order is wrong for a file:** two insertions at one point land in arrival
  order, upstream's first. Reorder them by hand if the file needs a different order; the
  merge never drops either.
- **`mergeinserts: <path>: …; merging as git does`:** the driver could not read a version
  or parse git's output, and fell back to `git merge-file`. The file then holds git's
  ordinary conflict markers.

## Security

`conflictmarkers` reads tracked text files and runs `git ls-files` in the root it is
given, with fixed arguments; no caller value reaches the command line except the root
and the paths git itself listed. It writes nothing. Git config can run commands, so the
driver definition is not a tracked file that every pull would make live: a repository
sets it in each clone's own config from a make target, the same trust as any other
target. The driver reads the three versions git hands it and writes only the `%A` file.

## Code map

| Path | What |
|---|---|
| `gitx.go` | `Tracked`, `Untracked`, `TrackedRegular`, and the `ls-files` call they share |
| `conflictmarkers/` | `Markers`, `Check`, `Finding`; tests over in-memory trees |
| `mergeinserts/` | `Merge`: the internal three-way merge, the conflict parser and the rewrite; table tests over `git merge-file` |
| `cmd/conflictmarkers/` | the check: flags, the scope, the report |
| `cmd/mergeinserts/` | the driver: arguments, the conflict style, the write-back and the fallback; the end-to-end rebase test |

## License

MIT, see [LICENSE](LICENSE).
