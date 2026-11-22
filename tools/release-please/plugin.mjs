// The first-parent plugin, named in release-please-config.json's plugins.
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

  // processCommits runs once per release path, before versions and notes.
  processCommits(commits) {
    return firstParentCommits(commits, this.history, {
      headerOf: this.headerOf,
      rank: this.rank,
      warn: message => this.warnOnce(message),
    });
  }

  // The same merge reaches several release paths.
  warnOnce(message) {
    if (this.warned.has(message)) return;
    this.warned.add(message);
    this.logger.warn(message);
  }
}

// processCommits is not told which path it handles, so types rank by one set
// of changelog-sections, which every path must share.
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

// Equal sections compare equal however their JSON is written.
function sectionsKey(sections) {
  if (sections === undefined) return 'defaults';
  return JSON.stringify(sections.map(s => [s.type, s.section ?? '', Boolean(s.hidden)]));
}

// parsedHeaders parses each merge's whole message as release-please does: its
// subject alone can parse where the message does not, and its entries carry
// the header as the parser respaces it. A merge whose first line gives no
// entry has no header.
function parsedHeaders(messageOf) {
  // release-please logs the same parse failures itself.
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

const collapse = text => text.replace(/\s+/g, ' ').trim();

export function registerFirstParent(history) {
  registerPlugin(FIRST_PARENT, options =>
    new FirstParentPlugin(options.github, options.targetBranch, options.repositoryConfig, history, options.logger));
}
