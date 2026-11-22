// The first-parent plugin as release-please loads it: named "first-parent" in
// release-please-config.json's plugins.
import { registerPlugin } from 'release-please';
import { parseConventionalCommits } from 'release-please/build/src/commit.js';
import { ManifestPlugin } from 'release-please/build/src/plugin.js';
import { firstParentCommits, typeRanker } from './first-parent.mjs';

export const FIRST_PARENT = 'first-parent';

export class FirstParentPlugin extends ManifestPlugin {
  constructor(github, targetBranch, repositoryConfig, history, logger) {
    super(github, targetBranch, repositoryConfig, logger);
    this.history = history;
    this.rank = typeRanker(sharedChangelogSections(repositoryConfig));
    this.conventionalMerges = conventionalMerges(history.subjectOf);
  }

  // processCommits runs, once per release path, before release-please
  // computes versions and notes.
  processCommits(commits) {
    return firstParentCommits(commits, this.history, {
      conventionalMerges: this.conventionalMerges,
      rank: this.rank,
      warn: message => this.logger.warn(message),
    });
  }
}

// sharedChangelogSections returns the changelog-sections every release path
// uses. processCommits is not told which path its commits belong to, so the
// plugin ranks types by one set of sections and refuses paths that differ.
function sharedChangelogSections(repositoryConfig) {
  const distinct = new Map();
  for (const [path, config] of Object.entries(repositoryConfig)) {
    distinct.set(JSON.stringify(config.changelogSections ?? null), path);
  }
  if (distinct.size > 1) {
    throw new Error(`${FIRST_PARENT}: release paths ${[...distinct.values()].join(', ')} set different changelog-sections; set one for all of them`);
  }
  return Object.values(repositoryConfig)[0]?.changelogSections;
}

// conventionalMerges returns the merges whose subjects release-please parses
// as a conventional commit, by its own parser: a branch commit is dropped only
// when its merge can carry the change.
function conventionalMerges(subjectOf) {
  // The parser logs each subject it cannot parse; release-please logs the
  // same commits again when it parses them itself.
  const quiet = { error() {}, warn() {}, info() {}, debug() {}, trace() {} };
  const merges = new Set();
  for (const [sha, subject] of subjectOf) {
    if (parseConventionalCommits([{ sha, message: subject, files: [] }], quiet).length > 0) merges.add(sha);
  }
  return merges;
}

// registerFirstParent makes the config's "first-parent" build a plugin over
// history (readHistory's result).
export function registerFirstParent(history) {
  registerPlugin(FIRST_PARENT, options =>
    new FirstParentPlugin(options.github, options.targetBranch, options.repositoryConfig, history, options.logger));
}
