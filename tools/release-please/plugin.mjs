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
    this.headerOf = parsedHeaders(history.subjectOf);
    this.warned = new Set();
  }

  // processCommits runs, once per release path, before release-please
  // computes versions and notes.
  processCommits(commits) {
    return firstParentCommits(commits, this.history, {
      headerOf: this.headerOf,
      rank: this.rank,
      warn: message => this.warnOnce(message),
    });
  }

  // warnOnce logs a message once a run: the same merge reaches several
  // release paths.
  warnOnce(message) {
    if (this.warned.has(message)) return;
    this.warned.add(message);
    this.logger.warn(message);
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

// parsedHeaders maps each merge whose subject release-please's parser reads
// as a conventional commit to the header it makes of it, which is the header
// it gives the merge's entry: a branch commit is dropped only when its merge
// can carry the change, and the entry is found by that exact text.
function parsedHeaders(subjectOf) {
  // The parser logs each subject it cannot parse; release-please logs the
  // same commits again when it parses them itself.
  const quiet = { error() {}, warn() {}, info() {}, debug() {}, trace() {} };
  const headers = new Map();
  for (const [sha, subject] of subjectOf) {
    const [header] = parseConventionalCommits([{ sha, message: subject, files: [] }], quiet);
    if (header !== undefined) headers.set(sha, header.message);
  }
  return headers;
}

// registerFirstParent makes the config's "first-parent" build a plugin over
// history (readHistory's result).
export function registerFirstParent(history) {
  registerPlugin(FIRST_PARENT, options =>
    new FirstParentPlugin(options.github, options.targetBranch, options.repositoryConfig, history, options.logger));
}
