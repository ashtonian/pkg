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
    this.headerOf = parsedHeaders(history.messageOf);
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
    distinct.set(sectionsKey(config.changelogSections), path);
  }
  if (distinct.size > 1) {
    throw new Error(`${FIRST_PARENT}: release paths ${[...distinct.values()].join(', ')} set different changelog-sections; set one for all of them`);
  }
  return Object.values(repositoryConfig)[0]?.changelogSections;
}

// sectionsKey identifies a changelog-sections list by what it means, not by
// how its JSON is written.
function sectionsKey(sections) {
  if (sections === undefined) return 'defaults';
  return JSON.stringify(sections.map(s => [s.type, s.section ?? '', Boolean(s.hidden)]));
}

// parsedHeaders parses each merge's whole message as release-please does and
// maps the merge to the header of the entry its first line gave, exactly as
// release-please's entry carries it. A merge whose message gives no such
// entry (it parses to nothing, or only to trailers) has no header, so its
// branch commits stay listed.
function parsedHeaders(messageOf) {
  // The parser logs each message it cannot parse; release-please logs the
  // same commits again when it parses them itself.
  const quiet = { error() {}, warn() {}, info() {}, debug() {}, trace() {} };
  const headers = new Map();
  for (const [sha, message] of messageOf) {
    const firstLine = collapse(message.split('\n')[0]);
    const entries = parseConventionalCommits([{ sha, message, files: [] }], quiet);
    const header = entries.find(entry => collapse(entry.message) === firstLine);
    if (header !== undefined) headers.set(sha, header.message);
  }
  return headers;
}

// collapse compares headers as release-please's parser rebuilds them, which
// can respace them.
const collapse = text => text.replace(/\s+/g, ' ').trim();

// registerFirstParent makes the config's "first-parent" build a plugin over
// history (readHistory's result).
export function registerFirstParent(history) {
  registerPlugin(FIRST_PARENT, options =>
    new FirstParentPlugin(options.github, options.targetBranch, options.repositoryConfig, history, options.logger));
}
