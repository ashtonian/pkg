// What release-please leaves out of the release notes when it reads a commit
// message: the rules the pull-request check (check-commits.mjs) fails on and
// the release job warns about. README.md describes each rule.
import { execFileSync } from 'node:child_process';
import { createRequire } from 'node:module';
import { commitOverride, revertsWithin } from './first-parent.mjs';

// parseConventionalCommits logs a parse error at debug level and keeps no
// syntax tree, so each entry goes through the parser it calls, resolved from
// release-please itself.
const parser = createRequire(import.meta.resolve('release-please/build/src/commit.js'))('@conventional-commits/parser');

// release-please's splitMessages (build/src/commit.js), which it does not
// export: each BEGIN_NESTED_COMMIT … END_NESTED_COMMIT block is an entry, and
// the rest splits at a blank line followed by a header of one of these types
// without "!".
const TYPES = '(?:feat|fix|docs|style|refactor|perf|test|build|ci|chore|revert)';
const SPLIT = new RegExp(`\\r?\\n\\r?\\n(?=${TYPES}(?:\\(.*?\\))?: )`);
const HEADER_AFTER_BLANK_LINE = new RegExp(`\\r?\\n\\r?\\n(${TYPES}(?:\\(.*?\\))?(!?): [^\\r\\n]*)`, 'g');

// entries returns the texts release-please parses one by one in message, each
// marked whether it was a nested commit.
export function entries(message) {
  const [plain, ...blocks] = message.split('BEGIN_NESTED_COMMIT');
  let rest = plain;
  const nested = [];
  for (const block of blocks) {
    const [inner, ...after] = block.split('END_NESTED_COMMIT');
    nested.push({ text: inner, nested: true });
    rest += after.join('END_NESTED_COMMIT');
  }
  return [...rest.split(SPLIT).filter(Boolean).map(text => ({ text, nested: false })), ...nested];
}

// messageProblems returns what release-please loses of message, each phrased
// to follow "<the message>: ". None means it reads every entry whole.
export function messageProblems(message) {
  const all = entries(message);
  const problems = [];
  for (const { text, nested } of all) {
    const it = all.length === 1 ? 'it' : `its entry "${firstLine(text.trimStart())}"`;
    let tree;
    try {
      tree = parser.parser(text);
    } catch (err) {
      const reason = err.message.replaceAll('\r', '\\r').replaceAll('\n', '\\n');
      problems.push(`release-please cannot parse ${it} (${reason}), so the release notes get none of its type, BREAKING CHANGE notes or issue references`);
      continue;
    }
    const footers = breakingFooters(tree);
    if (footers > 1) {
      problems.push(`${it} has ${footers} BREAKING CHANGE footers, and release-please keeps only the last; state every break in one footer`);
    }
    for (const [, header, bang] of text.trimStart().matchAll(HEADER_AFTER_BLANK_LINE)) {
      if (nested) {
        problems.push(`release-please reads "${header}" as part of the nested commit before it, which is one entry; give it a BEGIN_NESTED_COMMIT … END_NESTED_COMMIT block of its own`);
      } else if (bang) {
        problems.push(`release-please reads "${header}" as part of the entry before it, since it starts an entry only at a header without "!"; put each further breaking entry in its own BEGIN_NESTED_COMMIT … END_NESTED_COMMIT block`);
      }
    }
  }
  return problems;
}

// The parser marks "BREAKING CHANGE" and "BREAKING-CHANGE" in a body or a
// footer, and a header's "!", as breaking-change nodes; release-please keeps
// the text of the last marker of the first two kinds.
function breakingFooters(node) {
  const own = node.type === 'breaking-change' && node.value !== '!' ? 1 : 0;
  return own + (node.children ?? []).reduce((sum, child) => sum + breakingFooters(child), 0);
}

const firstLine = text => text.split(/\r?\n/)[0];

// pullRequestCommits returns the commits head brings onto base, oldest first,
// as {sha, message}. A merge carries no release note of its own, so merges
// are left out; so are commits a revert among them undid, and those reverts,
// which the first-parent plugin reads as nothing.
export function pullRequestCommits(repoDir, base, head) {
  const git = (...args) => execFileSync('git', ['-C', repoDir, ...args], { encoding: 'utf8', maxBuffer: 256 << 20 });
  // Without the merge base, the range would reach back to the shallow boundary.
  if (git('rev-parse', '--is-shallow-repository').trim() === 'true') {
    throw new Error(`${repoDir} is a shallow clone; checking ${base}..${head} needs the whole history (fetch-depth: 0)`);
  }
  const messageOf = new Map();
  const range = git('rev-list', '--no-merges', '--reverse', '--no-commit-header', '--format=%x00%H%x1f%B', `${base}..${head}`);
  for (const record of range.split('\0')) {
    const separator = record.indexOf('\x1f');
    if (separator < 0) continue;
    messageOf.set(record.slice(0, separator), record.slice(separator + 1).replace(/\n+$/, ''));
  }
  const undone = revertsWithin(messageOf);
  return [...messageOf].filter(([sha]) => !undone.has(sha)).map(([sha, message]) => ({ sha, message }));
}

// checkPullRequest returns what release-please would lose of a pull request
// with commits and description body: one {source, problems, replaced} per
// commit or override with problems. release-please reads a pull request's
// commit override in place of each of its commits, so with one, the commits'
// problems are replaced: they never reach the release notes.
export function checkPullRequest(commits, body) {
  const override = commitOverride(body);
  const findings = [];
  for (const { sha, message } of commits) {
    const problems = messageProblems(message);
    if (problems.length > 0) findings.push({ source: `${sha.slice(0, 7)} ${firstLine(message)}`, problems, replaced: override !== '' });
  }
  if (override !== '') {
    const problems = messageProblems(override);
    if (problems.length > 0) findings.push({ source: 'the commit override', problems, replaced: false });
  }
  return findings;
}
