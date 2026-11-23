# tools/release-please

[Contributing](../../CONTRIBUTING.md#releases)

## Overview

The `release-please` workflow runs release-please from here instead of through `release-please-action`, so it can load the `first-parent` plugin. A pull request lands as a merge commit whose subject is conventional (`fix(parse)!: … (#251)`), and the commits on its branch are conventional too. release-please reads the target branch's whole history, so it would list each change once per branch commit and again as its merge. The plugin keeps the merge: release notes and version bumps come from main's first-parent history, one entry per pull request.

For each release path, the plugin's `processCommits` hook, which runs before versions and notes are computed, takes the conventional commits release-please gathered for that path and:

- keeps commits on main's first-parent chain: merge commits, release commits, anything pushed to main directly;
- drops each commit a merge brought in when the merge has a header: the plugin parses the merge's whole message with release-please's own parser, as release-please does, and takes the entry its first line gave (the parser can respace it), not a trailer such as `Co-authored-by:` that it parses into an entry of its own. In a path the merge's changes reach, that entry takes over what the dropped commit calls for:
  - its type, when that ranks higher (`feat` over any other type the changelog shows, over hidden types such as `chore`, `refactor` and `test`), so a feature merged as `fix(…)` still bumps the minor version and a fix merged as `refactor(…)` still cuts a release and is listed. The job warns when this happens;
  - its `BREAKING CHANGE` notes. A breaking branch commit still raises the version, and its description replaces the note the merge's `!` gives it, which only repeats the subject;
  - its `Release-As` footer;
  - its issue references (`Closes #215`), one per issue.

  A branch commit that a revert on the same branch undid (git's `This reverts commit <sha>`), and the revert, take nothing over.

  The subject names the whole pull request, and a batched one changes each release path differently: a breaking core change, a fix to one sub-module, only a test of another. So where the merge's branch is complete in a path, its branch commits there decide its type and whether it breaks, and the subject's type and `!` count for nothing more. A pull request whose `feat(httpx)!:` merge also brought `test(httpx/oauth2): …` lists `httpx/oauth2` as a hidden `test` entry, which cuts no release, and its `!` is dropped from that entry's header, since the changelog writer would otherwise read it as a breaking change. The branch is complete in a path when each of its non-merge commits whose files release-please would list there (its own split by path, nested paths and `exclude-paths` included, read from git) reached the plugin as a conventional commit. It is not when one is older than the path's last release (see below), or is a commit release-please's parser can't read (`wip`, `fixup! …`).

  Elsewhere the subject is a floor the branch commits only raise: in a path where the branch is not complete, and in every path for a subject that claims more than its branch shows anywhere. That is a subject type that no branch commit's type ranks as high as, or a `!` (or `BREAKING CHANGE` footer) with no breaking branch commit. A pull request whose subject says `feat(parse)!:` over unmarked branch commits breaks as before, so mark the breaking branch commit itself when a pull request reaches more than one release path;

  In a path the merge's net changes don't reach, a branch commit there was taken back before the merge, so nothing takes it over;
- drops every branch commit of a merge whose own pull request sets `BEGIN_COMMIT_OVERRIDE`: release-please lists that merge as the override, which its author wrote to stand for the whole change (as #60 and #62 did). A commit carrying the override of a stacked pull request, one merged into this one's branch, folds into the merge it landed through like any other branch commit;
- keeps a branch commit whose merge has no header, so a change is duplicated rather than lost: a merge whose message release-please can't parse (a body that breaks the parser even though the subject parses, or a bare `Merge pull request #1 from …`), and a merge release-please lists without an entry the plugin can take as its header (with a warning). GitHub's default merge message, `Merge pull request #9 from a/b` above the pull request's title, counts as that title when it is the message's one conventional entry;
- keeps a commit newer than the history it read, which it cannot place.

`processCommits` is not told which path it is handling. release-please calls the plugin's `preconfigure` hook first, with each path's commits, and then hands `processCommits` its own parse of one path's; the plugin parses each path's commits in `preconfigure` the same way and finds the path by the parse. Paths with the same commits share a parse, so a merge counts as complete only when it is complete in each of them. If no path matches, the job warns and every merge's subject stays a floor.

release-please cuts the commits it gathers at each path's last release in history (date) order. A branch commit older than that release, on a pull request merged after it, never reaches the plugin, so in that path the branch is not complete and the merge subject stays a floor. That is one more reason the merge subject must summarize the whole change.

`run.mjs` does what the action does: it creates the GitHub releases a merged release PR describes, then opens or updates the release PR, and writes the action's step outputs. `release-please-config.json` names the plugin under `plugins`, so the stock action, which cannot load it, refuses the config (`Unknown plugin type: first-parent`) instead of producing duplicate notes. When the target branch's config doesn't name it, the runner adds it anyway and logs a warning. Either way the runner puts it ahead of the config's other plugins, so a plugin that rewrites messages (`sentence-case`) runs after it has found each merge's entry.

### What release-please cannot read

release-please drops a commit its parser cannot read, and with it the commit's type, `BREAKING CHANGE` notes and references, logging only `commit could not be parsed` at debug level; of several `BREAKING CHANGE` notes in one message it can read, it keeps the last. Nothing fails. `commit-messages.mjs` reads a message as release-please does: `entries` splits it into the texts release-please parses one by one (each `BEGIN_NESTED_COMMIT … END_NESTED_COMMIT` block is one; the rest splits at a blank line followed by a header of a standard type, `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`, `build`, `ci`, `chore` or `revert`, without `!`), and `messageProblems` parses each with release-please's parser and names what release-please would lose:

- an entry it cannot parse, with the parser's error. A body line whose first word runs straight into `(` reads as a footer token with a scope, so `parse_decimal(CAST('10.50' AS BLOB)) was '10.5', …` at the start of a line gives `unexpected token '(' at 4:19, valid tokens [)]`: that is how #287 lost its breaking note (#289);
- an entry with more than one `BREAKING CHANGE` or `BREAKING-CHANGE` footer or paragraph, of which release-please keeps the last;
- a header after a blank line within one entry: a `type(scope)!: ` header, at which release-please does not split, or any header inside a nested commit. The entry before it absorbs it, and either loses its own breaking note to the header's footer or drops the header's entry altogether.

Two places use it:

- `check-commits.mjs`, run by the `release notes` workflow on every pull request and each edit of its description. It reads every commit `--head` brings onto `--base`, and the commit override in the description, and fails naming each with a problem, the parser's error included. Merges are not read: a merge carries no release note of its own. Nor are a commit and its revert within the range, which the plugin reads as nothing. release-please reads a pull request's override in place of each of its commits, so with one, the commits' problems are warnings and only the override's fail.
- The plugin warns, for each merge among a path's commits, about every branch commit release-please cannot read whole, naming the merge whose entry lacks it, unless the merge's own pull request sets an override.

## Configuration

| Input | Type | Default | Description |
|---|---|---|---|
| `GITHUB_TOKEN` (env) | string | required | Token release-please reads and writes the repository with. The workflow passes `RELEASE_PLEASE_TOKEN`, else the job's `GITHUB_TOKEN`; a release PR opened with the latter runs no CI |
| `GITHUB_OUTPUT` (env) | path | set by Actions | Step output file; required unless `--dry-run` |
| `--repo` | `owner/name` | `$GITHUB_REPOSITORY` | Repository to release |
| `--config-file` | path in the repository | `release-please-config.json` | release-please config, read from the target branch on GitHub |
| `--manifest-file` | path in the repository | `.release-please-manifest.json` | Released versions, read from the target branch on GitHub |
| `--checkout` | local path | `.` | A clone of the repository with its full history (`fetch-depth: 0`). The runner fetches the target branch into it and reads the history there |
| `--remote` | remote name | `origin` | The clone's remote for the repository; the target branch is fetched from it as GitHub has it when the run starts |
| `--skip-github-release` | flag | off | Don't create releases; the `release-pr` job's run |
| `--skip-github-pull-request` | flag | off | Don't open or update the release PR, and read no history; the `release-please` job's run |
| `--dry-run` | flag | off | Print the release PRs release-please would open, and write nothing to GitHub. It still fetches the target branch into the clone, which moves `FETCH_HEAD` and the remote-tracking branch, and it shows no release a merged release PR is waiting on |

`check-commits.mjs` takes:

| Input | Type | Default | Description |
|---|---|---|---|
| `--base` | revision | required | The branch the pull request merges into; commits it holds are not the pull request's. The workflow passes `origin/<base branch>` as checked out |
| `--head` | revision | `HEAD` | The pull request's head commit |
| `--body-file` | path | none | The pull request's description, read for its `BEGIN_COMMIT_OVERRIDE` block; without it, no override |
| `--checkout` | local path | `.` | A clone with the whole history (`fetch-depth: 0`) |
| `GITHUB_ACTIONS` (env) | `true` or unset | set by Actions | `true` writes each problem as an error or warning annotation |

release-please's own settings (release type, components, `exclude-paths`, `last-release-sha`) stay in `release-please-config.json`. The plugin ranks types by `changelog-sections`, which every path must then share: `processCommits` is not told which path it is handling, so a config whose paths set different sections is refused.

## External dependencies

- `release-please` 17.11.2, pinned in `package.json` and `pnpm-lock.yaml`. `plugin.mjs` imports `ManifestPlugin` from `release-please/build/src/plugin.js`, `parseConventionalCommits` from `release-please/build/src/commit.js`, `ROOT_PROJECT_PATH` from `release-please/build/src/manifest.js`, and `CommitSplit` and `CommitExclude` from `release-please/build/src/util/`, paths outside its documented exports. `plugin.test.mjs` builds the plugin through release-please's plugin factory, drives it as the manifest does (`preconfigure`, then each path's parse through `processCommits`), and writes the notes with release-please's `DefaultChangelogNotes`, so an upgrade that moves any of them, changes how the parser splits a merge's trailers, or makes the changelog writer read a header differently fails `make release-please-check`.
- `@conventional-commits/parser`, the parser `parseConventionalCommits` runs, at the version release-please's own install resolves (0.4.1 in `pnpm-lock.yaml`); `commit-messages.mjs` loads it through release-please rather than depending on it. `parseConventionalCommits` logs a parse error at debug level and keeps no syntax tree, so `commit-messages.mjs` parses each entry with that parser and splits a message as release-please's `splitMessages`, which it does not export, does. `commit-messages.test.mjs` parses each case's entries one by one through `parseConventionalCommits` and requires the result to equal its parse of the whole message, so an upgrade that splits or parses messages differently fails `make release-please-check`.
- `git` on `PATH`, for the history read and the pull request's commits.
- Node.js 22 or later (release-please's floor); the workflow uses the current LTS. pnpm at the version `packageManager` pins, which pnpm does not let run dependencies' install scripts.
- GitHub's REST and GraphQL APIs. One run costs a few hundred REST calls, most of them release-please fetching each branch commit's files.

## Interfaces

- `first-parent.mjs`: `fetchBranchHead(repoDir, remote, branch)` fetches the branch and returns its commit. `readHistory(repoDir, ref)` refuses a shallow clone and returns `{mergeOf, messageOf, branchCommits, revertedWithin}`: a `Map` from each commit a first-parent merge brought in to that merge, each such merge's whole message, each such non-merge commit's message and changed files (a rename's old and new paths both), and the commits a revert on the same merge's branch undid, with the reverts. `typeRanker(sections)` ranks commit types by `changelog-sections`. `firstParentCommits(commits, history, {headerOf, rank, complete, branches, warn})` filters one path's release-please `ConventionalCommit`s as above; `complete` holds the merges whose branch is complete in that path, and `branches` maps a merge to its branch commits' types, whether one breaks, and those release-please cannot read whole (`unreadable`, each `{sha, subject, problems}`), which it passes to `warn`. `revertsWithin(messageOf, groupOf)` returns the commits a revert among them undid, with the reverts. `commitOverride(body)` returns a pull request description's override as release-please reads it, or `''`. None of them imports release-please.
- `commit-messages.mjs`: `entries(message)`, the texts release-please parses one by one, each `{text, nested}`; `messageProblems(message)`, what release-please loses of a message, each a phrase to follow `<the message>: `; `pullRequestCommits(repoDir, base, head)`, which refuses a shallow clone and returns the non-merge commits `head` brings onto `base`, oldest first, as `{sha, message}`, without those a revert among them undid or those reverts; `checkPullRequest(commits, body)`, one `{source, problems, replaced}` per commit or override with problems, `replaced` when the description's override stands in for the commit.
- `check-commits.mjs`: the pull-request check. It prints each problem (as an annotation under Actions) and a summary line, and exits 0 when nothing fails, 1 when a commit or the override fails, and 2 when the check itself fails (a bad revision, a shallow clone).
- `plugin.mjs`: `FirstParentPlugin`, which parses each merge's message and each branch commit's with release-please's parser, records what `messageProblems` finds in each branch commit, maps branch commits to the paths their files reach, identifies each `processCommits` call's path from `preconfigure`, refuses differing `changelog-sections` and logs each warning once a run, and `registerFirstParent(history)`, which makes the config's `"first-parent"` build it.
- `outputs.mjs`: `Output`, `outputReleases`, `outputPullRequests`: step outputs encoded as `@actions/core` encodes them (an absent value is an empty string).
- `run.mjs`: the workflow step. Outputs, named as `release-please-action` v5 names them: `releases_created`, `paths_released`, and per released path `<path>--release_created`, `<path>--tag_name`, `<path>--version`, `<path>--body`, `<path>--html_url` and the release's other fields; `prs_created`, `pr`, `prs`. The `publish` job reads `parse--tag_name`.

## Build, run and test

```bash
make release-please-check          # pnpm install --frozen-lockfile, then node --test
GITHUB_TOKEN="$(gh auth token)" node tools/release-please/run.mjs --repo ashtonian/pkg --dry-run
gh pr view 361 -R ashtonian/pkg --json body -q .body > /tmp/description.md
node tools/release-please/check-commits.mjs --base origin/main --body-file /tmp/description.md
```

The last two run the pull-request check on the current branch, with a pull request's description.

The workflow's jobs install through `.github/actions/release-please-tool` (Node.js LTS, pnpm, `pnpm install --frozen-lockfile`), which CI's modules job uses for `make release-please-check` too.

The dry run reads the live repository and prints each release PR's title and body, so a change to the plugin or config can be compared with the open release PR before it merges.

## Operations

Two jobs run on every push to main. `release-please` creates the releases a merged release PR describes, in each push's own run, so `publish` attests a release from the run of its release commit. `release-pr` follows it and opens or updates the release PR, one run at a time (`concurrency: release-please-pr`): a newer push's pending run replaces an older one's, so the last push writes the release PR last. `release-pr`'s log starts with `Running release-please version:` and `first-parent: history at <sha> (origin/main), N branch commits mapped to their merges`; then release-please logs each path it considers and the plugins it runs (`running plugin: FirstParentPlugin`). The history is read at main as GitHub has it when the run starts; a merge that lands while the run is building keeps its branch commits listed until the next run, which that merge's own push starts.

- **`release-please failed: Error: GITHUB_TOKEN is not set`**: the secret and the job token are both missing; check the job's `permissions` and `RELEASE_PLEASE_TOKEN`.
- **`… has no entry with its header; its branch commits stay listed`**: release-please listed the merge, but none of its entries carries the header the plugin parsed from the merge's message, so the plugin kept the branch commits rather than risk dropping their change. Look at how release-please read that merge's message in the dry run.
- **`first-parent: release paths … set different changelog-sections`**: give every path the same `changelog-sections`, or none.
- **A module released for a change its pull request made elsewhere** (a sub-module bumped, or listed as breaking, for the core's change): in that module's path the merge subject stayed a floor. Either a branch commit touching the module is older than its last release or unparseable, or the subject claims a type or `!` that no branch commit carries. Run the dry run and check the merge's branch commits with `git log --format='%h %s' <merge>^1..<merge>^2 -- <path>`; mark the breaking branch commit itself (`feat(…)!:` or a `BREAKING CHANGE:` footer), or edit the release PR.
- **`first-parent: no release path has these commits; each merge counts at least as its subject`**: release-please handed `processCommits` commits that match no path from `preconfigure`, so the release-please upgrade changed how the manifest calls plugins. Versions are safe but can be too high; fix the path identification in `plugin.mjs` and its tests.
- **`… counts as feat, from … ; its subject's type is lower than its change's`**: a merge subject understates its branch (`fix(…)` over a `feat` commit). The release is right; the release PR lists the entry under the raised type with the merge's subject. Word future merge subjects by the change's highest type.
- **`… names no first-parent plugin; adding it`**: the target branch's config doesn't list `first-parent`. The notes are still first-parent; restore the entry so the stock action keeps refusing the config.
- **A change listed twice in the release PR**: release-please can't parse the merge commit's message, so its branch commits stayed listed (by design, rather than dropping the change). Edit the release PR, and land future pull requests with a conventional merge subject.
- **A change missing from the release PR**: run the dry run and look for the change's merge commit in its output. A merge whose message release-please can't parse lists its branch commits instead; one whose diff touches no release path counts toward no release.
- **`… is a shallow clone; first-parent needs the whole history`**: the `release-pr` job's checkout must keep `fetch-depth: 0`, and a local dry run needs a full clone.
- **`… is listed, but no entry of it is its header; its branch commits stay listed`**: release-please parsed entries from the merge's message, but none from its first line, and more than one besides trailers, so the plugin can't tell which entry stands for the change. Give future merges a conventional subject.
- **`git fetch` fails**: the runner fetches the target branch without credentials (`persist-credentials: false`), which works for this public repository. A private one needs the checkout's credentials.
- **`Unknown plugin type: first-parent`**: something ran the stock release-please against this config. Run it through `run.mjs`.
- **A release PR proposing versions that were just released**: a `release-pr` run of an earlier push was still building when a later push's `release-please` job tagged the release. Close that PR; the next push's run builds the right one.
- **GitHub rate limits (403 `API rate limit exceeded`)**: release-please makes a few hundred REST calls a run, on the token's account. Rerun the job once the limit resets.

The `release notes` workflow's `commits` job (`commits release-please can read`) runs on each pull request event `opened`, `synchronize`, `reopened` and `edited`, apart from `ci.yml`: it checks commits, not the tree, so no push runs it, and a description edit reruns only it. Its log ends with `N commits in origin/main..<sha> [and the commit override] read as release-please reads them;` and either `nothing is lost` or `fix the errors above`, each error above it naming a commit (short sha and subject) or `the commit override`.

- **`release-please cannot parse it (unexpected token '(' at L:C, valid tokens [)])`**: line L of that commit's message opens with a word that runs straight into `(`, and column C is a second `(` (or the line ends) before its `)`. Reword the line or start it with another word, and amend or rewrite the commit; or give the pull request a commit override, after which the commit only warns.
- **`… cannot parse its entry "<header>" (…)`**: one entry of a message with several, such as a nested commit of the override; L counts that entry's lines.
- **`it has N BREAKING CHANGE footers, and release-please keeps only the last`**: merge the footers into one `BREAKING CHANGE:` paragraph.
- **`release-please reads "<header>" as part of the entry before it`**: an override (or commit) has a further `type(scope)!: ` entry; wrap it in `BEGIN_NESTED_COMMIT` … `END_NESTED_COMMIT`. **`… as part of the nested commit before it`**: a nested block holds two entries; give each its own block.
- **`… release-please reads the commit override in its place`** (a warning): the commit's problem never reaches the release notes, as long as the description keeps its override.
- **`check-commits failed: … is a shallow clone`** or **`unknown revision`**: the checkout lacks the history or the base branch; the job's checkout keeps `fetch-depth: 0`, and locally `git fetch origin main`.
- **`<merge> (<subject>) is missing what release-please cannot read of its branch commit <sha> (<subject>): …`** in `release-pr`'s log: that merge's entry in the release PR lacks the commit's type, `BREAKING CHANGE` notes or references. Before the release PR merges, add to the merged pull request's description a commit override that states its whole change, then rerun `release-pr` (or push to main); the plugin then reads the override and the warning goes.

## Security

The job holds `contents: write` and `pull-requests: write`. `run.mjs` passes the token to release-please only; it never logs it. The checkout keeps no credentials (`persist-credentials: false`), and the history read runs `git` against that checkout only. Dependencies install from the lockfile alone (`--frozen-lockfile`), without install scripts. The `release notes` job holds `contents: read` alone and keeps no checkout credentials; the pull request's description reaches the check through an environment variable and a file, never the script's text.

## Code map

| File | Responsibility |
|---|---|
| `first-parent.mjs` | `fetchBranchHead`, `readHistory`, `typeRanker`, `firstParentCommits`, `revertsWithin`, `commitOverride`: which commits count, the type, notes and references a merge takes over, and the warning about a branch commit release-please cannot read |
| `plugin.mjs` | `FirstParentPlugin`, `registerFirstParent`: the plugin release-please builds from the config |
| `outputs.mjs` | The action's step outputs |
| `run.mjs` | The workflow steps: releases, the release PR, step outputs, `--dry-run` |
| `commit-messages.mjs` | `entries`, `messageProblems`, `pullRequestCommits`, `checkPullRequest`: what release-please loses of a commit message, a pull request's commits and its override |
| `check-commits.mjs` | The pull-request check the `release notes` workflow runs |
| `../../.github/workflows/release-notes.yml` | The pull-request check's workflow: every pull request event, including description edits |
| `../../.github/actions/release-please-tool` | Node.js, pnpm and this tool's dependencies, for the workflow's jobs and CI |
| `first-parent.test.mjs` | Counting, the fallbacks, trailers, overrides (the merge's own, a stacked pull request's, and one nothing carries), paths a merge doesn't reach, type, breaking, Release-As and reference folding; per-path typing where the branch is complete, and the subject as a floor where its branch doesn't back it; the warning naming a merge with a branch commit release-please cannot read, and none under the merge's own override; `readHistory` on scratch repositories (a sync merge, a branch cut from another, a wrapped subject, branch commits' files); `fetchBranchHead` against a bare remote |
| `outputs.test.mjs` | Output names and encoding |
| `commit-messages.test.mjs` | Messages that parse, the #289 body line, two `BREAKING CHANGE` footers, an override's swallowed `!` header, nested commits that are fine and two entries in one; the split held to `parseConventionalCommits`; an override replacing its commits; a pull request's commits read from a scratch repository without merges or reverted pairs; `check-commits.mjs`'s annotations and exit status |
| `plugin.test.mjs` | The config's `first-parent` built through release-please's factory; release-please's own parse of a merge with trailers, a respaced subject, a subject with trailing space and a body, and a body that breaks the parser; unparseable merges; one warning a run; `changelog-sections` that differ refused, the same written differently accepted; per-path entries and their notes as release-please writes them, a branch commit cut by the release or unparseable, `exclude-paths`, paths sharing commits, and no `preconfigure`; the #289 branch commit named with its merge, and not once reverted on its branch |
