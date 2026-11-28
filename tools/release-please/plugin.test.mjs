import assert from 'node:assert/strict';
import test from 'node:test';
import { parseConventionalCommits } from 'release-please/build/src/commit.js';
import { DefaultChangelogNotes } from 'release-please/build/src/changelog-notes/default.js';
import { buildPlugin } from 'release-please/build/src/factories/plugin-factory.js';
import { buildStrategy } from 'release-please/build/src/factory.js';
import { LinkedVersions } from 'release-please/build/src/plugins/linked-versions.js';
import { TagName } from 'release-please/build/src/util/tag-name.js';
import { Version } from 'release-please/build/src/version.js';
import { FIRST_PARENT, FirstParentLinkedVersions, FirstParentPlugin, registerFirstParent } from './plugin.mjs';

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

test('the commits it keeps carry breaking notes escaped for Markdown', () => {
  const history = { mergeOf: new Map([['b1', 'm1']]), messageOf: new Map([['m1', 'feat(parse)!: keys (#361)']]) };
  const commits = parsed([
    ['m1', 'feat(parse)!: keys (#361)'],
    ['b1', 'feat(parse)!: list keys\n\nBREAKING CHANGE: include=<key> is E2083.'],
  ]);

  const kept = plugin(history).processCommits(commits);

  assert.deepEqual(kept.flatMap(c => c.notes.map(n => n.text)), ['include=&lt;key&gt; is E2083.']);
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

// parse/client was split out of parse: what changed it before the split
// shipped in parse's releases.
test('a release path lists nothing from before it joined the config', async () => {
  const history = {
    mergeOf: new Map([['b0', 'm0'], ['b2', 'm2']]),
    messageOf: new Map([['m0', 'feat(parse)!: wire (#1)'], ['m2', 'feat(parse/client)!: its own module (#3)']]),
    positionOf: new Map([['m0', 0], ['c1', 1], ['m2', 2]]),
    startOf: new Map([['parse', 'm0'], ['parse/client', 'm2']]),
  };
  const built = new FirstParentPlugin(github, 'main', { parse: {}, 'parse/client': {} }, history, quiet);
  const old = [['m0', 'feat(parse)!: wire (#1)'], ['b0', 'feat(parse)!: wire\n\nBREAKING CHANGE: the old wire.'], ['c1', 'fix(parse/client): direct push']];
  const split = [['m2', 'feat(parse/client)!: its own module (#3)'], ['b2', 'feat(parse/client)!: split\n\nBREAKING CHANGE: a module now.']];
  const newer = [['n1', 'fix(parse/client): after the history read']];
  const raw = list => list.map(([sha, message]) => ({ sha, message, files: [] }));
  await built.preconfigure({}, { parse: raw(old), 'parse/client': raw([...newer, ...split, ...old]) }, {});

  const client = built.processCommits(parsed([...newer, ...split, ...old]));
  const parse = built.processCommits(parsed(old));

  assert.deepEqual(client.map(c => c.sha), ['n1', 'm2']);
  assert.deepEqual(client.find(c => c.sha === 'm2').notes.map(n => n.text), ['a module now.']);
  assert.deepEqual(parse.map(c => c.sha), ['m0', 'c1'], 'a path from its start keeps everything since');
});

test('paths sharing a parse keep what the earliest start keeps, and a path with no start keeps all', async () => {
  const history = {
    mergeOf: new Map(),
    messageOf: new Map(),
    positionOf: new Map([['c0', 0], ['c1', 1]]),
    startOf: new Map([['a', 'c0'], ['a/b', 'c1']]),
  };
  const commits = [['c1', 'fix(a): second'], ['c0', 'fix(a): first']];
  const raw = commits.map(([sha, message]) => ({ sha, message, files: [] }));
  const shared = new FirstParentPlugin(github, 'main', { a: {}, 'a/b': {} }, history, quiet);
  await shared.preconfigure({}, { a: raw, 'a/b': raw }, {});
  const unknown = new FirstParentPlugin(github, 'main', { c: {} }, history, quiet);
  await unknown.preconfigure({}, { c: raw }, {});

  assert.deepEqual(shared.processCommits(parsed(commits)).map(c => c.sha), ['c1', 'c0']);
  assert.deepEqual(unknown.processCommits(parsed(commits)).map(c => c.sha), ['c1', 'c0']);
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

// #381 (55aac64): release-please lists nothing for a breaking title under
// GitHub's subject, so its release listed the branch commits and not the title.
test("a breaking title under GitHub's default merge subject is the merge's entry where its change reaches", async () => {
  const message = 'Merge pull request #381 from a/b\n\nfeat(parse)!: parse/client becomes its own module';
  assert.deepEqual(parseConventionalCommits([{ sha: 'm1', message, files: [] }], quiet), [], "release-please's own parse");
  const branch = [
    ['b1', 'feat(parse)!: decode answers', ['parse/client/decode.go']],
    ['b2', 'docs(httpx): link the parse client', ['httpx/README.md']],
  ];
  const history = landedBatch(message, branch);
  const kept = await runPaths(history, {
    parse: [{ sha: 'm1', message, files: ['parse/client/decode.go'] }, { sha: 'b1', message: branch[0][1], files: branch[0][2] }],
    httpx: [{ sha: 'm1', message, files: ['httpx/README.md'] }, { sha: 'b2', message: branch[1][1], files: branch[1][2] }],
    gowalker: [],
  });

  assert.deepEqual(kept.parse.map(c => [c.sha, c.type, c.breaking]), [['m1', 'feat', true]], 'the title, as the one entry');
  assert.match(await notes(kept.parse), /BREAKING CHANGES[\s\S]*parse\/client becomes its own module/);
  assert.deepEqual(kept.httpx.map(c => [c.sha, c.type, c.breaking]), [['m1', 'docs', false]], 'the branch decides a complete path');
  assert.deepEqual(kept.gowalker, [], 'a path the change misses lists nothing');
});

test("GitHub's default merge subject over a body that is no single conventional title keeps the branch commits", async () => {
  const message = 'Merge pull request #9 from a/b\n\nfeat(parse)!: add Stream\nand a second line';
  const branch = [['b1', 'feat(parse): add Stream', ['parse/stream.go']]];
  const kept = await runPaths(landedBatch(message, branch), { parse: raw(message, branch, ['b1']) });

  assert.deepEqual(kept.parse.map(c => c.sha), ['b1']);
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

// #372 merged after httpx 0.5.0, but its breaking commit was older than that
// release, where release-please cuts httpx's commits, so the commit's
// BREAKING CHANGE footer did not reach the notes.
test("a branch commit older than the path's last release is read from the history", async () => {
  const subject = 'fix(httpx)!: preserve independent options across repeated layer configuration (#372)';
  const footer = 'Repeated layer options apply in call order.';
  const branch = [
    ['b0', `fix(httpx)!: preserve independent options across repeated layer configuration\n\nBREAKING CHANGE: ${footer}`, ['httpx/client.go']],
    ['b1', 'test(httpx): close what the tests open', ['httpx/client_test.go']],
    ['b2', 'feat(httpx/oauth2): refresh early', ['httpx/oauth2/token.go']],
  ];
  const config = { httpx: {}, 'httpx/oauth2': {} };

  const kept = await runPaths(landedBatch(subject, branch), { httpx: raw(subject, branch, ['b1']) }, config);

  const [header] = kept.httpx;
  assert.deepEqual([header.type, header.breaking], ['fix', true], "another path's feature does not raise the type");
  assert.deepEqual(header.notes, [{ title: 'BREAKING CHANGE', text: footer }]);
  assert.match(await notes(kept.httpx), /Repeated layer options apply in call order\./);
});

// A branch commit release-please cannot parse gives no entry, so the branch
// commits the plugin sees are not the whole change there and the subject stays
// a floor.
test('a branch commit release-please cannot parse keeps the merge subject a floor in its path', async () => {
  const subject = 'feat(parse)!: stream rows (#9)';
  const cases = [
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

// #289: release-please could not parse a branch commit, so its merge's entry
// lost the commit's BREAKING CHANGE while the job passed.
test('a branch commit release-please cannot parse is named with its merge, unless reverted on the branch', async () => {
  const subject = 'feat(parse)!: refuse repeated cursor members (#287)';
  const blob = "fix(parse)!: read a BLOB as no decimal\n\nso\nparse_decimal(CAST('10.50' AS BLOB)) was '10.5'.\n\nBREAKING CHANGE: a BLOB is NULL.";
  const branch = [['b1', 'feat(parse)!: refuse repeated members', ['parse/cursor.go']], ['b2', blob, ['parse/sqlite.go']]];
  const run = async history => {
    const warnings = [];
    const built = new FirstParentPlugin(github, 'main', { parse: {} }, history, { ...quiet, warn: m => warnings.push(m) });
    const commits = raw(subject, branch, ['b1', 'b2']);
    await built.preconfigure({}, { parse: commits }, {});
    built.processCommits(parseConventionalCommits(commits, quiet));
    return warnings;
  };

  assert.deepEqual(await run(landedBatch(subject, branch)), [
    `m1 (${subject}) is missing what release-please cannot read of its branch commit b2 (fix(parse)!: read a BLOB as no decimal): ` +
      "release-please cannot parse it (unexpected token '(' at 4:19, valid tokens [)]), so the release notes get none of its type, BREAKING CHANGE notes or issue references",
  ]);
  const reverted = { ...landedBatch(subject, branch), revertedWithin: new Set(['b2']) };
  assert.deepEqual(await run(reverted), []);
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
// complete in both, and an unlisted branch commit counts only when it touches
// both: one touching only parse/sdk leaves parse/sdk incomplete.
test('paths with the same commits count a merge as complete only when it is in each', async () => {
  const subject = 'feat(parse)!: stream rows (#9)';
  const cases = [
    { name: 'unparseable', other: ['b0', 'wip', ['parse/sdk/c.go']] },
    { name: 'older than the release', other: ['b0', 'fix(parse/sdk): close streams\n\nBREAKING CHANGE: Close returns an error.', ['parse/sdk/c.go']] },
  ];
  for (const tc of cases) {
    const branch = [['b1', 'test(parse): cover streaming', ['parse/a_test.go', 'parse/sdk/b_test.go']], tc.other, ['b2', 'feat!: x', ['x/y.go']]];
    const listed = raw(subject, branch, ['b1']);

    const kept = await runPaths(landedBatch(subject, branch), { parse: listed, 'parse/sdk': listed });

    for (const path of ['parse', 'parse/sdk']) {
      const [header] = kept[path];
      assert.deepEqual([header.type, header.breaking], ['feat', true], `${tc.name}: ${path}`);
      assert.deepEqual(header.notes.map(n => n.text), ['stream rows (#9)'], `${tc.name}: ${path} takes no note from parse/sdk alone`);
    }
  }
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

// A pull request batching a breaking httpx change with a fix to parse, which
// releases with parse/parsesql at one version; parse/parsesql gained only a
// test, which cuts no release of its own. Returns the version each path's
// release pull request gets, and what the plugins warned.
async function linkedRelease(linkedPlugin) {
  const warned = [];
  const logger = { ...quiet, warn: message => warned.push(message) };
  const subject = 'feat(httpx)!: batch (#400)';
  const branch = [
    ['b1', 'feat(httpx)!: negotiate codecs', ['httpx/codec.go']],
    ['b2', 'fix(parse): close the rows', ['parse/list.go']],
    ['b3', 'test(parse/parsesql): check the README', ['parse/parsesql/readme_test.go']],
  ];
  const history = landedBatch(subject, branch);
  const release = { releaseType: 'go', bumpMinorPreMajor: true, includeComponentInTag: true, tagSeparator: '/' };
  const config = {
    httpx: { ...release, component: 'httpx' },
    parse: { ...release, component: 'parse', excludePaths: ['parse/parsesql'] },
    'parse/parsesql': { ...release, component: 'parse/parsesql' },
  };
  const commitsByPath = {
    httpx: raw(subject, branch, ['b1']),
    parse: raw(subject, branch, ['b2']),
    'parse/parsesql': raw(subject, branch, ['b3']),
  };
  const releasesByPath = Object.fromEntries(Object.keys(config).map(path => [path, {
    tag: new TagName(Version.parse('0.18.0'), config[path].component, '/', true), sha: 'r0', notes: '',
  }]));
  const strategiesByPath = {};
  for (const path of Object.keys(config)) {
    strategiesByPath[path] = await buildStrategy({ ...config[path], github, path, targetBranch: 'main' });
  }
  const firstParent = new FirstParentPlugin(github, 'main', config, history, logger);
  const linked = new LinkedVersions(github, 'main', config, 'parse', ['parse', 'parse/parsesql'], { merge: false, logger: quiet });
  const strategies = await firstParent.preconfigure(strategiesByPath, commitsByPath, releasesByPath);
  const configured = await linkedPlugin(linked, firstParent).preconfigure(strategies, commitsByPath, releasesByPath);
  const versions = {};
  for (const [path, strategy] of Object.entries(configured)) {
    const commits = firstParent.processCommits(parseConventionalCommits(commitsByPath[path], quiet));
    const pr = await strategy.buildReleasePullRequest(commits, releasesByPath[path]);
    versions[path] = pr?.version?.toString();
  }
  return { versions, warned };
}

// first-parent releases httpx as the breaking change and parse as the fix
// its branch holds there, and the linked group follows parse:
// parse/parsesql's test cuts no release, so linked-versions appends the
// commit that releases it at the group's version, which first-parent still
// finds the path's commits beside. linked-versions alone reads the merge's
// "!" among the raw commits: it releases parse as a breaking change, and
// counts parse/parsesql as releasing, so it adds no commit there, and
// first-parent's notes for it are empty: the group splits.
test('a linked group takes the version first-parent gives its members', async () => {
  const followed = await linkedRelease((linked, firstParent) => new FirstParentLinkedVersions(linked, firstParent));
  assert.deepEqual(followed.versions, { httpx: '0.19.0', parse: '0.18.1', 'parse/parsesql': '0.18.1' });
  assert.deepEqual(followed.warned.filter(w => w.includes('no release path')), []);
  const unprocessed = await linkedRelease(linked => linked);
  assert.deepEqual(unprocessed.versions, { httpx: '0.19.0', parse: '0.19.0', 'parse/parsesql': undefined }, 'linked-versions alone reads the raw commits');
});

test('the runner links versions through first-parent', () => {
  const firstParent = new FirstParentPlugin(github, 'main', { parse: {} }, { mergeOf: new Map(), messageOf: new Map() }, quiet);
  const linked = new LinkedVersions(github, 'main', { parse: {} }, 'g', ['parse'], { merge: false, logger: quiet });
  const wrapped = new FirstParentLinkedVersions(linked, firstParent);
  assert.ok(wrapped instanceof LinkedVersions);
  assert.deepEqual([wrapped.groupName, [...wrapped.components], wrapped.merge], ['g', ['parse'], false]);
});
