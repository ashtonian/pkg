# tools/release-please

[Contributing](../../CONTRIBUTING.md#releases)

## Overview

The `release-please` workflow runs release-please from here instead of through `release-please-action`, so it can load the `first-parent` plugin. A pull request lands as a merge commit whose subject is conventional (`fix(parse)!: … (#251)`), and the commits on its branch are conventional too. release-please reads the target branch's whole history, so it would list each change once per branch commit and again as its merge. The plugin keeps the merge: release notes and version bumps come from main's first-parent history, one entry per pull request.

For each conventional commit release-please gathers, the plugin's `processCommits` hook, which runs before versions and notes are computed:

- keeps commits on main's first-parent chain: merge commits, release commits, anything pushed to main directly;
- drops a commit a merge brought in when that merge is a conventional commit of the same release path. The merge takes over what the dropped commit calls for:
  - its type, when that ranks higher (`feat` over any other type the changelog shows, over hidden types such as `chore` and `test`), so a feature merged as `fix(…)` still bumps the minor version and a fix merged as `chore(…)` still cuts a release. The job warns when this happens;
  - its `BREAKING CHANGE` notes. A breaking branch commit still raises the version, and its description replaces the note the merge's `!` gives it, which only repeats the subject;
  - its `Release-As` footer;
  - its issue references (`Closes #215`), one per issue;
- keeps a branch commit whose merge is not a conventional commit, such as GitHub's default `Merge pull request #1 from …`, so no change goes missing from a release;
- keeps a commit newer than the history it read, which it cannot place.

`run.mjs` does what the action does: it creates the GitHub releases a merged release PR describes, then opens or updates the release PR, and writes the action's step outputs. `release-please-config.json` names the plugin under `plugins`, so the stock action, which cannot load it, refuses the config (`Unknown plugin type: first-parent`) instead of producing duplicate notes. When the target branch's config doesn't name it, the runner adds it anyway and logs a warning.

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
| `--dry-run` | flag | off | Print the release PRs release-please would open, and write nothing |

release-please's own settings (release type, components, `exclude-paths`, `last-release-sha`) stay in `release-please-config.json`.

## External dependencies

- `release-please` 17.11.2, pinned in `package.json` and `pnpm-lock.yaml`. `plugin.mjs` imports its `ManifestPlugin` from `release-please/build/src/plugin.js`, a path outside its documented exports; `plugin.test.mjs` builds the plugin through release-please's plugin factory, so an upgrade that moves either fails `make release-please-check`.
- `git` on `PATH`, for the history read.
- Node.js 22 or later (release-please's floor); the workflow uses the current LTS. pnpm at the version `packageManager` pins, which pnpm does not let run dependencies' install scripts.
- GitHub's REST and GraphQL APIs. One run costs a few hundred REST calls, most of them release-please fetching each branch commit's files.

## Interfaces

- `first-parent.mjs`: `fetchBranchHead(repoDir, remote, branch)` fetches the branch and returns its commit. `readHistory(repoDir, ref)` returns `{mergeOf}`, a `Map` from each commit a first-parent merge brought in to that merge, built from one `git rev-list`. `typeRanker(sectionLists)` ranks commit types by the paths' `changelog-sections`. `firstParentCommits(commits, history, {rank, warn})` filters release-please `ConventionalCommit`s as above. None of them imports release-please.
- `plugin.mjs`: `FirstParentPlugin`, and `registerFirstParent(history)`, which makes the config's `"first-parent"` build it.
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
- **`… counts as feat, from … ; its subject's type is lower than its change's`**: a merge subject understates its branch (`fix(…)` over a `feat` commit). The release is right; the release PR lists the entry under the raised type with the merge's subject. Word future merge subjects by the change's highest type.
- **`… names no first-parent plugin; adding it`**: the target branch's config doesn't list `first-parent`. The notes are still first-parent; restore the entry so the stock action keeps refusing the config.
- **A change listed twice in the release PR**: the merge commit's subject is not conventional, so its branch commits stayed listed (by design, rather than dropping the change). Edit the release PR, and land future pull requests with a conventional merge subject.
- **A change missing from the release PR**: run the dry run and look for the change's merge commit in its output. A merge whose subject isn't conventional lists its branch commits instead; one whose diff touches no release path counts toward no release.
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
| `first-parent.test.mjs` | Counting, the fallbacks, type, breaking, Release-As and reference folding; `readHistory` on scratch repositories (a sync merge, a branch cut from another); `fetchBranchHead` against a bare remote |
| `outputs.test.mjs` | Output names and encoding |
| `plugin.test.mjs` | The config's `first-parent` built through release-please's factory |
