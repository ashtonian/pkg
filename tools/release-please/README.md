# tools/release-please

[Contributing](../../CONTRIBUTING.md#releases)

## Overview

The `release-please` workflow runs release-please from here instead of through `release-please-action`, so it can load the `first-parent` plugin. A pull request lands as a merge commit whose subject is conventional (`fix(parse)!: … (#251)`), and the commits on its branch are conventional too. release-please reads the target branch's whole history, so it would list each change once per branch commit and again as its merge. The plugin keeps the merge: release notes and version bumps come from main's first-parent history, one entry per pull request.

For each release path, the plugin's `processCommits` hook, which runs before versions and notes are computed, takes the conventional commits release-please gathered and:

- keeps commits on main's first-parent chain: merge commits, release commits, anything pushed to main directly;
- drops each commit a merge brought in when the merge has a header: the plugin parses the merge's whole message with release-please's own parser, as release-please does, and takes the entry its first line gave (the parser can respace it), not a trailer such as `Co-authored-by:` that it parses into an entry of its own. In a path the merge's changes reach, that entry takes over what the dropped commit calls for:
  - its type, when that ranks higher (`feat` over any other type the changelog shows, over hidden types such as `chore`, `refactor` and `test`), so a feature merged as `fix(…)` still bumps the minor version and a fix merged as `refactor(…)` still cuts a release and is listed. The job warns when this happens;
  - its `BREAKING CHANGE` notes. A breaking branch commit still raises the version, and its description replaces the note the merge's `!` gives it, which only repeats the subject;
  - its `Release-As` footer;
  - its issue references (`Closes #215`), one per issue.

  In a path the merge's net changes don't reach, a branch commit there was taken back before the merge, so nothing takes it over;
- drops every branch commit of a merge whose own pull request sets `BEGIN_COMMIT_OVERRIDE`: release-please lists that merge as the override, which its author wrote to stand for the whole change (as #60 and #62 did). A commit carrying the override of a stacked pull request, one merged into this one's branch, folds into the merge it landed through like any other branch commit;
- keeps a branch commit whose merge has no header, so a change is duplicated rather than lost: a merge whose message release-please can't parse (GitHub's default `Merge pull request #1 from …`, or a body that breaks the parser even though the subject parses), and a merge listed without an entry carrying its header (with a warning);
- keeps a commit newer than the history it read, which it cannot place.

The plugin sees only the commits release-please gathers, which it cuts at each path's last release in history (date) order. A branch commit older than that release, on a pull request merged after it, never reaches the plugin, so that pull request's merge subject alone decides its entry and bump, as it would without the plugin. That is one more reason the merge subject must summarize the whole change.

`run.mjs` does what the action does: it creates the GitHub releases a merged release PR describes, then opens or updates the release PR, and writes the action's step outputs. `release-please-config.json` names the plugin under `plugins`, so the stock action, which cannot load it, refuses the config (`Unknown plugin type: first-parent`) instead of producing duplicate notes. When the target branch's config doesn't name it, the runner adds it anyway and logs a warning. Either way the runner puts it ahead of the config's other plugins, so a plugin that rewrites messages (`sentence-case`) runs after it has found each merge's entry.

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
| `--dry-run` | flag | off | Print the release PRs release-please would open, and write nothing to GitHub. It still fetches the target branch into the clone, which moves `FETCH_HEAD` and the remote-tracking branch, and it shows no release a merged release PR is waiting on |

release-please's own settings (release type, components, `exclude-paths`, `last-release-sha`) stay in `release-please-config.json`. The plugin ranks types by `changelog-sections`, which every path must then share: `processCommits` is not told which path it is handling, so a config whose paths set different sections is refused.

## External dependencies

- `release-please` 17.11.2, pinned in `package.json` and `pnpm-lock.yaml`. `plugin.mjs` imports `ManifestPlugin` from `release-please/build/src/plugin.js` and `parseConventionalCommits` from `release-please/build/src/commit.js`, paths outside its documented exports. `plugin.test.mjs` builds the plugin through release-please's plugin factory and feeds it that parser's output, so an upgrade that moves either, or changes how the parser splits a merge's trailers, fails `make release-please-check`.
- `git` on `PATH`, for the history read.
- Node.js 22 or later (release-please's floor); the workflow uses the current LTS. pnpm at the version `packageManager` pins, which pnpm does not let run dependencies' install scripts.
- GitHub's REST and GraphQL APIs. One run costs a few hundred REST calls, most of them release-please fetching each branch commit's files.

## Interfaces

- `first-parent.mjs`: `fetchBranchHead(repoDir, remote, branch)` fetches the branch and returns its commit. `readHistory(repoDir, ref)` returns `{mergeOf, messageOf}`: a `Map` from each commit a first-parent merge brought in to that merge, and each such merge's whole message. `typeRanker(sections)` ranks commit types by `changelog-sections`. `firstParentCommits(commits, history, {headerOf, rank, warn})` filters release-please `ConventionalCommit`s as above. None of them imports release-please.
- `plugin.mjs`: `FirstParentPlugin`, which parses each merge's message with release-please's parser, refuses differing `changelog-sections` and logs each warning once a run, and `registerFirstParent(history)`, which makes the config's `"first-parent"` build it.
- `outputs.mjs`: `Output`, `outputReleases`, `outputPullRequests`: step outputs encoded as `@actions/core` encodes them (an absent value is an empty string).
- `run.mjs`: the workflow step. Outputs, named as `release-please-action` v5 names them: `releases_created`, `paths_released`, and per released path `<path>--release_created`, `<path>--tag_name`, `<path>--version`, `<path>--body`, `<path>--html_url` and the release's other fields; `prs_created`, `pr`, `prs`. The `publish` job reads `parse--tag_name`.

## Build, run and test

```bash
make release-please-check          # pnpm install --frozen-lockfile, then node --test
GITHUB_TOKEN="$(gh auth token)" node tools/release-please/run.mjs --repo ashtonian/pkg --dry-run
```

The dry run reads the live repository and prints each release PR's title and body, so a change to the plugin or config can be compared with the open release PR before it merges.

## Operations

The job runs on every push to main, one run at a time (`concurrency: release-please`), so the last push's run writes the release PR last. Its log starts with `Running release-please version:` and `first-parent: history at <sha> (origin/main), N branch commits mapped to their merges`; then release-please logs each path it considers and the plugins it runs (`running plugin: FirstParentPlugin`). The history is read at main as GitHub has it when the run starts; a merge that lands while the run is building keeps its branch commits listed until the next run, which that merge's own push starts.

- **`release-please failed: Error: GITHUB_TOKEN is not set`**: the secret and the job token are both missing; check the job's `permissions` and `RELEASE_PLEASE_TOKEN`.
- **`… has no entry with its header; its branch commits stay listed`**: release-please listed the merge, but none of its entries carries the header the plugin parsed from the merge's message, so the plugin kept the branch commits rather than risk dropping their change. Look at how release-please read that merge's message in the dry run.
- **`first-parent: release paths … set different changelog-sections`**: give every path the same `changelog-sections`, or none.
- **`… counts as feat, from … ; its subject's type is lower than its change's`**: a merge subject understates its branch (`fix(…)` over a `feat` commit). The release is right; the release PR lists the entry under the raised type with the merge's subject. Word future merge subjects by the change's highest type.
- **`… names no first-parent plugin; adding it`**: the target branch's config doesn't list `first-parent`. The notes are still first-parent; restore the entry so the stock action keeps refusing the config.
- **A change listed twice in the release PR**: release-please can't parse the merge commit's message, so its branch commits stayed listed (by design, rather than dropping the change). Edit the release PR, and land future pull requests with a conventional merge subject.
- **A change missing from the release PR**: run the dry run and look for the change's merge commit in its output. A merge whose message release-please can't parse lists its branch commits instead; one whose diff touches no release path counts toward no release.
- **`first-parent: 0 branch commits mapped`** after merges have landed: the checkout is shallow. The workflow's checkout must keep `fetch-depth: 0`.
- **`git fetch` fails**: the runner fetches the target branch without credentials (`persist-credentials: false`), which works for this public repository. A private one needs the checkout's credentials.
- **`Unknown plugin type: first-parent`**: something ran the stock release-please against this config. Run it through `run.mjs`.
- **GitHub rate limits (403 `API rate limit exceeded`)**: release-please makes a few hundred REST calls a run, on the token's account. Rerun the job once the limit resets.

## Security

The job holds `contents: write` and `pull-requests: write`. `run.mjs` passes the token to release-please only; it never logs it. The checkout keeps no credentials (`persist-credentials: false`), and the history read runs `git` against that checkout only. Dependencies install from the lockfile alone (`--frozen-lockfile`), without install scripts.

## Code map

| File | Responsibility |
|---|---|
| `first-parent.mjs` | `fetchBranchHead`, `readHistory`, `typeRanker`, `firstParentCommits`: which commits count, and the type, notes and references a merge takes over |
| `plugin.mjs` | `FirstParentPlugin`, `registerFirstParent`: the plugin release-please builds from the config |
| `outputs.mjs` | The action's step outputs |
| `run.mjs` | The workflow step: releases, the release PR, step outputs, `--dry-run` |
| `first-parent.test.mjs` | Counting, the fallbacks, trailers, overrides (the merge's own, a stacked pull request's, and one nothing carries), paths a merge doesn't reach, type, breaking, Release-As and reference folding; `readHistory` on scratch repositories (a sync merge, a branch cut from another, a wrapped subject); `fetchBranchHead` against a bare remote |
| `outputs.test.mjs` | Output names and encoding |
| `plugin.test.mjs` | The config's `first-parent` built through release-please's factory; release-please's own parse of a merge with trailers, a respaced subject, a subject with trailing space and a body, and a body that breaks the parser; unparseable merges; one warning a run; `changelog-sections` that differ refused, the same written differently accepted |
