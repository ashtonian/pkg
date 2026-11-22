import assert from 'node:assert/strict';
import test from 'node:test';
import { parseConventionalCommits } from 'release-please/build/src/commit.js';
import { buildPlugin } from 'release-please/build/src/factories/plugin-factory.js';
import { FIRST_PARENT, FirstParentPlugin, registerFirstParent } from './plugin.mjs';

const github = { repository: { owner: 'o', repo: 'r', defaultBranch: 'main' } };
const quiet = { debug() {}, info() {}, warn() {}, error() {}, trace() {} };

function plugin(history, repositoryConfig = { parse: {} }) {
  registerFirstParent(history);
  return buildPlugin({ type: FIRST_PARENT, github, targetBranch: 'main', repositoryConfig, manifestPath: '.release-please-manifest.json' });
}

// The commits as release-please hands them to processCommits: raw messages
// through its own parser.
const parsed = raw => parseConventionalCommits(raw.map(([sha, message]) => ({ sha, message, files: [] })), quiet);

// release-please builds the config's plugins through its factory; this is the
// path a release-please upgrade that moves the plugin API would break.
test('a config naming first-parent builds the plugin over the history', () => {
  const built = plugin({ mergeOf: new Map(), messageOf: new Map() });
  assert.ok(built instanceof FirstParentPlugin);
});

test("release-please's own parse of a merge with trailers folds the branch into its header", () => {
  const subject = 'fix(parse): tidy reader (#260)';
  const message = `${subject}\n\nCo-authored-by: Pat <pat@example.com>`;
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', message]]) };
  const commits = parsed([
    ['m1', message],
    ['b1', 'feat(parse)!: add Stream\n\nBREAKING CHANGE: Reader is gone.\n\nCloses #5'],
  ]);

  const kept = plugin(history).processCommits(commits);

  const header = kept.find(c => c.message === subject);
  assert.equal(header.type, 'feat');
  assert.equal(header.breaking, true);
  assert.deepEqual(header.notes.filter(n => n.title === 'BREAKING CHANGE').map(n => n.text), ['Reader is gone.']);
  assert.deepEqual(header.references.map(r => r.issue), ['5']);
  assert.ok(kept.every(c => c.sha === 'm1'), 'the branch commit is gone');
  for (const trailer of kept.filter(c => c !== header)) {
    assert.equal(trailer.breaking, false, `${trailer.message} takes nothing`);
  }
});

// The parser collapses the whitespace in a header, so the merge's entry is
// found by the header it makes of the subject, not by the subject's text.
test("a merge subject the parser respaces still finds its entry", () => {
  const subject = 'fix(parse):  tidy reader (#260)';
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', subject]]) };
  const commits = parsed([['m1', subject], ['b1', 'feat(parse): add Stream']]);

  const kept = plugin(history).processCommits(commits);

  assert.deepEqual(kept.map(c => [c.sha, c.type]), [['m1', 'feat']]);
});

test('a raised type warns once a run, however many paths the merge reaches', () => {
  const warnings = [];
  const logger = { ...quiet, warn: message => warnings.push(message) };
  const subject = 'fix(parse): tidy reader (#260)';
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', subject]]) };
  const built = new FirstParentPlugin(github, 'main', { parse: {}, 'parse/sdk': {} }, history, logger);

  for (let path = 0; path < 2; path++) built.processCommits(parsed([['m1', subject], ['b1', 'feat(parse): add Stream']]));

  assert.equal(warnings.length, 1);
  assert.match(warnings[0], /counts as feat/);
});

// release-please keeps a header's trailing space when a body follows; the
// subject parsed alone loses it.
test('a merge subject with trailing space and a trailer finds its entry', () => {
  const message = 'fix(parse): tidy reader (#260) \n\nCo-authored-by: P <p@example.com>';
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', message]]) };

  const kept = plugin(history).processCommits(parsed([['m1', message], ['b1', 'feat(parse): add Stream']]));

  assert.ok(kept.every(c => c.sha === 'm1'), 'the branch commit is gone');
  assert.equal(kept.find(c => c.message.startsWith('fix(parse)')).type, 'feat');
});

// The subject alone parses, but the whole message, which release-please
// parses, does not: release-please lists no entry for the merge, which must
// not read as its change missing this path.
test("a merge whose whole message release-please can't parse keeps its branch commits", () => {
  const message = 'fix(parse): tidy reader (#260)\n\nCloses #5\nfoo(';
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', message]]) };

  const kept = plugin(history).processCommits(parsed([['m1', message], ['b1', 'feat(parse): add Stream']]));

  assert.deepEqual(kept.map(c => c.sha), ['b1']);
});

test("a merge whose subject release-please can't parse keeps its branch commits", () => {
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', 'Merge pull request #1 from a/b']]) };
  const commits = parsed([['b1', 'feat(parse): accept POST lists']]);

  assert.deepEqual(plugin(history).processCommits(commits).map(c => c.sha), ['b1']);
});

test('release paths with different changelog-sections are refused', () => {
  const history = { mergeOf: new Map(), messageOf: new Map() };
  assert.throws(
    () => plugin(history, { parse: {}, config: { changelogSections: [{ type: 'feat', section: 'Features' }] } }),
    /set different changelog-sections/,
  );
  assert.ok(plugin(history, { parse: {}, config: {} }) instanceof FirstParentPlugin, 'paths that all use the defaults agree');
  const same = [{ type: 'feat', section: 'Features' }];
  const reordered = [{ section: 'Features', type: 'feat', hidden: false }];
  assert.ok(plugin(history, { parse: { changelogSections: same }, config: { changelogSections: reordered } }) instanceof FirstParentPlugin, 'the same sections written differently agree');
});
