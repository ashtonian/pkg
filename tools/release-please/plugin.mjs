// The first-parent plugin, named in release-please-config.json's plugins.
import { registerPlugin } from 'release-please';
import { parseConventionalCommits } from 'release-please/build/src/commit.js';
import { ROOT_PROJECT_PATH } from 'release-please/build/src/manifest.js';
import { ManifestPlugin } from 'release-please/build/src/plugin.js';
import { CommitExclude } from 'release-please/build/src/util/commit-exclude.js';
import { CommitSplit } from 'release-please/build/src/util/commit-split.js';
import { messageProblems } from './commit-messages.mjs';
import { firstParentCommits, typeRanker } from './first-parent.mjs';
import { escapeNotes } from './release-body.mjs';

export const FIRST_PARENT = 'first-parent';

export class FirstParentPlugin extends ManifestPlugin {
  constructor(github, targetBranch, repositoryConfig, history, logger) {
    super(github, targetBranch, repositoryConfig, logger);
    this.history = history;
    this.rank = typeRanker(sharedChangelogSections(repositoryConfig));
    this.headerOf = parsedHeaders(history.messageOf);
    this.branches = branchSummaries(history);
    this.touching = touchingByPath(history, repositoryConfig);
    this.pathsOf = undefined;
    this.warned = new Set();
  }

  // release-please calls preconfigure with each path's commits, then for each
  // path hands processCommits its own parse of them. processCommits is not
  // told the path, so the parse identifies it.
  async preconfigure(strategiesByPath, commitsByPath) {
    this.pathsOf = new Map();
    for (const [path, commits] of Object.entries(commitsByPath)) {
      const key = listKey(parseConventionalCommits(commits, quiet));
      this.pathsOf.set(key, [...(this.pathsOf.get(key) ?? []), path]);
    }
    return strategiesByPath;
  }

  // processCommits runs once per release path, before versions and notes.
  processCommits(commits) {
    // Paths with the same commits share a parse; a merge must be complete in
    // each of them.
    const paths = this.pathsOf?.get(listKey(commits));
    if (paths === undefined && commits.length > 0) {
      this.warnOnce(`${FIRST_PARENT}: no release path has these commits; each merge counts at least as its subject`);
    }
    const all = [...commits, ...this.unlisted(commits, paths ?? [])];
    const kept = firstParentCommits(all, this.history, {
      headerOf: this.headerOf,
      rank: this.rank,
      complete: this.completeMerges(all, paths ?? []),
      branches: this.branches,
      warn: message => this.warnOnce(message),
    });
    return escapeNotes(kept);
  }

  // unlisted parses, as release-please does, each branch commit of a merge
  // listed here that touches paths but release-please did not list.
  // release-please cuts a path's commits at its last release in history
  // (date) order, so a branch commit older than that release never reaches
  // it, though its merge came after. Paths sharing a parse cannot be told
  // apart, so only a commit touching each of them is read.
  unlisted(commits, paths) {
    if (paths.length === 0) return [];
    const { branchCommits = new Map() } = this.history;
    const listed = new Set(commits.map(commit => commit.sha));
    const read = [];
    for (const merge of listed) {
      for (const sha of this.branches.get(merge)?.shas ?? []) {
        if (listed.has(sha) || !paths.every(path => this.touching.get(path)?.has(sha))) continue;
        const { message, files } = branchCommits.get(sha);
        read.push(...parseConventionalCommits([{ sha, message, files }], quiet));
      }
    }
    return read;
  }

  // completeMerges returns the merges each of whose branch commits touching
  // paths is among commits as a conventional commit: one release-please's
  // parser cannot read ("wip", "fixup! …") gives no entry.
  completeMerges(commits, paths) {
    const complete = new Set();
    if (paths.length === 0) return complete;
    const { mergeOf, revertedWithin = new Set() } = this.history;
    const conventional = new Set(commits.filter(isConventional).map(commit => commit.sha));
    for (const merge of new Set(commits.map(commit => mergeOf.get(commit.sha) ?? commit.sha))) {
      const branch = this.branches.get(merge)?.shas ?? [];
      const reaches = sha => revertedWithin.has(sha) || conventional.has(sha);
      if (paths.every(path => branch.every(sha => !this.touching.get(path)?.has(sha) || reaches(sha)))) complete.add(merge);
    }
    return complete;
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
// the header as the parser respaces it, adding or removing whitespace.
function parsedHeaders(messageOf) {
  const headers = new Map();
  for (const [sha, message] of messageOf) {
    const entries = parseConventionalCommits([{ sha, message, files: [] }], quiet);
    const firstLine = unspaced(message.split('\n')[0]);
    let header = entries.find(entry => unspaced(entry.message) === firstLine);
    if (header === undefined) {
      // GitHub's default merge message, "Merge pull request #9 from a/b"
      // above the pull request's title: that title is the merge's one entry
      // other than its trailers (Co-authored-by and the like).
      const conventional = entries.filter(isConventional);
      if (conventional.length === 1) header = conventional[0];
    }
    if (header !== undefined) headers.set(sha, header.message);
  }
  return headers;
}

const unspaced = text => text.replace(/\s+/g, '');

// release-please logs the same parse failures itself.
const quiet = { error() {}, warn() {}, info() {}, debug() {}, trace() {} };

// Trailers such as Co-authored-by parse as entries whose type is no word.
const isConventional = entry => /^[a-z]+$/.test(entry.type);

const listKey = commits => commits.map(commit => commit.sha).join(' ');

// branchSummaries maps each merge to its non-merge branch commits, reverted
// ones aside: their shas, the types release-please's parser gives them,
// whether one breaks, and what release-please loses of each it cannot read
// whole.
function branchSummaries({ mergeOf, branchCommits = new Map(), revertedWithin = new Set() }) {
  const summaries = new Map();
  for (const [sha, { message }] of branchCommits) {
    const merge = mergeOf.get(sha);
    if (!summaries.has(merge)) summaries.set(merge, { shas: [], types: [], breaking: false, unreadable: [] });
    const summary = summaries.get(merge);
    summary.shas.push(sha);
    if (revertedWithin.has(sha)) continue;
    for (const entry of parseConventionalCommits([{ sha, message, files: [] }], quiet).filter(isConventional)) {
      summary.types.push(entry.type);
      summary.breaking ||= entry.breaking;
    }
    const problems = messageProblems(message);
    if (problems.length > 0) summary.unreadable.push({ sha, subject: message.split('\n')[0], problems });
  }
  return summaries;
}

// touchingByPath maps each release path to the branch commits release-please
// would list there by their files: its own split and exclude-paths rules.
function touchingByPath({ branchCommits = new Map() }, repositoryConfig) {
  const commits = [...branchCommits].map(([sha, { files }]) => ({ sha, message: '', files }));
  const paths = Object.keys(repositoryConfig);
  const split = new CommitSplit({ includeEmpty: true, packagePaths: paths }).split(commits);
  const byPath = {};
  for (const path of paths) byPath[path] = path === ROOT_PROJECT_PATH ? commits : split[path] ?? [];
  const kept = new CommitExclude(repositoryConfig).excludeCommits(byPath);
  return new Map(Object.entries(kept).map(([path, list]) => [path, new Set(list.map(commit => commit.sha))]));
}

export function registerFirstParent(history) {
  registerPlugin(FIRST_PARENT, options =>
    new FirstParentPlugin(options.github, options.targetBranch, options.repositoryConfig, history, options.logger));
}
