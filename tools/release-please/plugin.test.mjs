import assert from 'node:assert/strict';
import test from 'node:test';
import { parseConventionalCommits } from 'release-please/build/src/commit.js';
import { DefaultChangelogNotes } from 'release-please/build/src/changelog-notes/default.js';
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

test('a raised type warns once a run, however many paths the merge reaches', async () => {
  const warnings = [];
  const logger = { ...quiet, warn: message => warnings.push(message) };
  const subject = 'fix(parse): tidy reader (#260)';
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', subject]]) };
  const built = new FirstParentPlugin(github, 'main', { parse: {}, 'parse/sdk': {} }, history, logger);
  const raw = [{ sha: 'm1', message: subject, files: [] }, { sha: 'b1', message: 'feat(parse): add Stream', files: [] }];
  await built.preconfigure({}, { parse: raw, 'parse/sdk': raw }, {});

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

test('a merge subject the parser adds a space to still finds its entry', () => {
  const subject = 'feat(parse):add Stream (#9)';
  const history = { mergeOf: new Map([['b1', 'm1'], ['b2', 'm1']]), messageOf: new Map([['m1', subject]]) };
  const commits = parsed([['m1', subject], ['b1', 'feat(parse): add Stream'], ['b2', 'fix(parse): tidy']]);

  assert.deepEqual(plugin(history).processCommits(commits).map(c => c.sha), ['m1']);
});

// GitHub's default merge message puts the pull request's title under its
// first line; release-please lists that title as the merge's entry.
test("GitHub's default merge message counts as the pull request's title", () => {
  const message = 'Merge pull request #9 from a/b\n\nfeat(parse): add Stream\n\nCo-authored-by: P <p@example.com>';
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', message]]) };
  const commits = parsed([['m1', message], ['b1', 'fix(parse): add Stream']]);

  const kept = plugin(history).processCommits(commits);

  assert.ok(kept.every(c => c.sha === 'm1'), 'the branch commit is gone');
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

// landedBatch is a history with one merge, m1, of subject over branch: each
// [sha, message, files] a non-merge commit it brought in.
function landedBatch(subject, branch) {
  return {
    mergeOf: new Map(branch.map(([sha]) => [sha, 'm1'])),
    messageOf: new Map([['m1', subject]]),
    branchCommits: new Map(branch.map(([sha, message, files]) => [sha, { message, files }])),
  };
}

// runPaths drives the plugin as release-please's manifest does: preconfigure
// with every path's commits, then processCommits with each path's own parse.
async function runPaths(history, commitsByPath, repositoryConfig) {
  const config = repositoryConfig ?? Object.fromEntries(Object.keys(commitsByPath).map(path => [path, {}]));
  const built = new FirstParentPlugin(github, 'main', config, history, quiet);
  await built.preconfigure({}, commitsByPath, {});
  const kept = {};
  for (const [path, raw] of Object.entries(commitsByPath)) kept[path] = built.processCommits(parseConventionalCommits(raw, quiet));
  return kept;
}

// raw gives release-please's unparsed commits: the merge listed with the
// files its change touches, and the branch commits listed in this path.
const raw = (subject, branch, listed) => [
  { sha: 'm1', message: subject, files: branch.flatMap(([, , files]) => files) },
  ...branch.filter(([sha]) => listed.includes(sha)).map(([sha, message, files]) => ({ sha, message, files })),
];

const notes = commits => new DefaultChangelogNotes().buildNotes(commits, {
  host: 'https://github.com', owner: 'o', repository: 'r', version: '1.0.0', previousTag: 'v0.9.0', currentTag: 'v1.0.0', targetBranch: 'main', commits: [],
});

// A pull request batching a breaking core change with a sub-module that only
// gained a test: the sub-module's notes are empty, so it gets no release.
test('each release path counts what the merge changed there', async () => {
  const subject = 'feat(httpx)!: batch (#355)';
  const branch = [
    ['b1', 'feat(httpx)!: negotiate codecs', ['httpx/codec.go']],
    ['b2', 'test(httpx/oauth2): check the README', ['httpx/oauth2/readme_test.go']],
  ];
  const kept = await runPaths(landedBatch(subject, branch), {
    httpx: raw(subject, branch, ['b1']),
    'httpx/oauth2': raw(subject, branch, ['b2']),
  });

  const [core] = kept.httpx;
  assert.deepEqual([core.type, core.breaking], ['feat', true]);
  assert.match(await notes(kept.httpx), /BREAKING CHANGES/);

  const [oauth2] = kept['httpx/oauth2'];
  assert.deepEqual([oauth2.type, oauth2.breaking], ['test', false]);
  const written = await notes(kept['httpx/oauth2']);
  assert.doesNotMatch(written, /BREAKING|###/, 'no section, so release-please cuts no release');
});

// A branch commit older than the path's last release, or one release-please
// cannot parse, never reaches the plugin: the branch commits it sees are not
// the whole change there, so the subject stays a floor.
test("a branch commit missing from a path's commits keeps the merge subject a floor there", async () => {
  const subject = 'feat(parse)!: stream rows (#9)';
  const cases = [
    { name: 'older than the release', missing: ['b0', 'feat(parse)!: stream rows', ['parse/stream.go']], floor: true },
    { name: 'unparseable', missing: ['b0', 'wip', ['parse/stream.go']], floor: true },
    { name: 'a fixup', missing: ['b0', 'fixup! test(parse): cover streaming', ['parse/stream.go']], floor: true },
    { name: 'unparseable, in another path', missing: ['b0', 'wip', ['httpx/x.go']], floor: false },
  ];
  for (const tc of cases) {
    const branch = [tc.missing, ['b1', 'test(parse): cover streaming', ['parse/stream_test.go']], ['b2', 'feat(httpx)!: x', ['httpx/x.go']]];
    const kept = await runPaths(landedBatch(subject, branch), { parse: raw(subject, branch, ['b1']) });

    const [header] = kept.parse;
    assert.deepEqual([header.type, header.breaking], tc.floor ? ['feat', true] : ['test', false], tc.name);
  }
});

test("a branch commit touching only a path's exclude-paths leaves it complete", async () => {
  const subject = 'feat(parse)!: stream rows (#9)';
  const branch = [
    ['b0', 'wip', ['parse/testdata/rows.json']],
    ['b1', 'test(parse): cover streaming', ['parse/stream_test.go']],
    ['b2', 'feat(httpx)!: x', ['httpx/x.go']],
  ];
  const config = { parse: { excludePaths: ['parse/testdata'] }, httpx: {} };

  const kept = await runPaths(landedBatch(subject, branch), { parse: raw(subject, branch, ['b1']) }, config);

  assert.deepEqual([kept.parse[0].type, kept.parse[0].breaking], ['test', false]);
});

// Two paths with the same commits share a parse, so the merge must be
// complete in both.
test('paths with the same commits count a merge as complete only when it is in each', async () => {
  const subject = 'feat(parse)!: stream rows (#9)';
  const branch = [['b1', 'test(parse): cover streaming', ['parse/a_test.go', 'parse/sdk/b_test.go']], ['b0', 'wip', ['parse/sdk/c.go']], ['b2', 'feat!: x', ['x/y.go']]];
  const listed = raw(subject, branch, ['b1']);

  const kept = await runPaths(landedBatch(subject, branch), { parse: listed, 'parse/sdk': listed });

  for (const path of ['parse', 'parse/sdk']) assert.deepEqual([kept[path][0].type, kept[path][0].breaking], ['feat', true], path);
});

test('without preconfigure, each merge counts at least as its subject, with a warning', () => {
  const warnings = [];
  const subject = 'feat(httpx)!: batch (#355)';
  const branch = [['b1', 'test(httpx/oauth2): check the README', ['httpx/oauth2/readme_test.go']], ['b2', 'feat(httpx)!: x', ['httpx/x.go']]];
  const built = new FirstParentPlugin(github, 'main', { 'httpx/oauth2': {} }, landedBatch(subject, branch), { ...quiet, warn: m => warnings.push(m) });

  const [header] = built.processCommits(parseConventionalCommits(raw(subject, branch, ['b1']), quiet));

  assert.deepEqual([header.type, header.breaking], ['feat', true]);
  assert.match(warnings.join('\n'), /no release path has these commits/);
});
