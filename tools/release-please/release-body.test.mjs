import assert from 'node:assert/strict';
import test from 'node:test';
import { parseConventionalCommits } from 'release-please/build/src/commit.js';
import { DefaultChangelogNotes } from 'release-please/build/src/changelog-notes/default.js';
import { PullRequestBody } from 'release-please/build/src/util/pull-request-body.js';
import { Version } from 'release-please/build/src/version.js';
import { escapeNotes, installReleaseBody, parseReleaseBody, releaseSections } from './release-body.mjs';

const quiet = { debug() {}, info() {}, warn() {}, error() {}, trace() {} };

// stockParse is release-please's own reading, taken before installReleaseBody
// replaces it.
const stockParse = PullRequestBody.parse;
installReleaseBody();

const release = (component, version, notes) => ({ component, version: Version.parse(version), notes });
const body = releases => new PullRequestBody(releases, { useComponents: true }).toString();
const components = parsed => parsed.releaseData.map(r => `${r.component}@${r.version}`);

// The shape of #354: a word in angle brackets in an earlier release's notes.
const swallowing = [
  release('parse', '0.18.0', '## [0.18.0]\n\n### ⚠ BREAKING CHANGES\n\n* schema documents list the key; include=<key> is E2083.'),
  release('config', '0.14.0', '## [0.14.0]\n\n### ⚠ BREAKING CHANGES\n\n* PrintTemplate writes the keys under a <name>: entry.'),
  release('httpx', '0.5.0', '## [0.5.0]\n\n### Bug Fixes\n\n* decode nulls'),
];

test("release-please's own reading drops a release after a tag-like word", () => {
  // Pins googleapis/release-please#2952: when this fails, the upstream parse
  // reads every release and release-body.mjs can go.
  assert.deepEqual(components(stockParse.call(PullRequestBody, body(swallowing), quiet)), ['parse@0.18.0', 'httpx@0.5.0']);
});

test('every release reads back, its notes as written', () => {
  const parsed = parseReleaseBody(body(swallowing), quiet);
  assert.deepEqual(components(parsed), ['parse@0.18.0', 'config@0.14.0', 'httpx@0.5.0']);
  assert.deepEqual(parsed.releaseData.map(r => r.notes), swallowing.map(r => r.notes));
});

// findMergedReleasePullRequests parses the merged body and renders it again;
// each path then parses that rendering (googleapis/release-please#2899).
test("the merged release PR survives release-please's parse, render and parse", () => {
  const notes = [
    release('parse', '0.18.0', 'include=&lt;key&gt; and `Key<T>` and a raw <details> token'),
    release('config', '0.14.0', 'under a <name>: entry'),
  ];
  const twice = PullRequestBody.parse(PullRequestBody.parse(body(notes), quiet).toString(), quiet);
  assert.deepEqual(twice.releaseData.map(r => [r.component, r.notes]), notes.map(r => [r.component, r.notes]));
});

test('notes holding section lines as text stay whole', () => {
  const notes = 'a <details><summary>trick</summary> inline\n</details> in prose\nend';
  const parsed = parseReleaseBody(body([release('a', '1.0.0', notes), release('b', '2.0.0', 'b notes')]), quiet);
  assert.deepEqual(parsed.releaseData.map(r => [r.component, r.notes]), [['a', notes], ['b', 'b notes']]);
});

test('a body that would not read back as its releases is refused', () => {
  const forged = 'text\n</details>\n\n<details><summary>evil: 9.9.9</summary>\nmore';
  const pr = new PullRequestBody([release('a', '1.0.0', forged)], { useComponents: true });
  assert.throws(() => pr.toString(), /a@1\.0\.0 would be lost or changed/);
});

test("a body without sections keeps release-please's single-release reading", () => {
  const single = new PullRequestBody([{ version: Version.parse('1.2.3'), notes: '## [1.2.3](https://github.com/o/r/compare/v1.2.2...v1.2.3) (2026-01-01)\n\n* fix' }], { useComponents: false });
  const parsed = parseReleaseBody(single.toString(), quiet);
  assert.equal(parsed.releaseData.length, 1);
  assert.equal(parsed.releaseData[0].version.toString(), '1.2.3');
});

test('a body that is no release PR is not read', () => {
  assert.equal(releaseSections('no delimiter here'), undefined);
});

test('a section with no closing line is refused, not truncated', () => {
  const text = 'header\n---\n\n<details><summary>a: 1.0.0</summary>\n\nnotes\n\n<details><summary>b: 2.0.0</summary>\n\nb\n</details>\n\n---\nfooter';
  assert.throws(() => releaseSections(text), /has no <\/details> line before the next section/);
});

const changelog = commits => new DefaultChangelogNotes().buildNotes(commits, {
  host: 'https://github.com', owner: 'o', repository: 'r', version: '1.0.0', previousTag: 'v0.9.0', currentTag: 'v1.0.0', targetBranch: 'main', commits: [],
});

test('breaking notes are escaped as release-please escapes subjects', async () => {
  const commits = parseConventionalCommits([{
    sha: 'b1',
    message: 'feat(parse)!: schema documents list <the> key\n\nBREAKING CHANGE: include=<key> is E2083; `Key<T>` is unchanged; a > b.',
    files: [],
  }], quiet);
  const escaped = escapeNotes(commits);
  assert.equal(escaped[0].notes[0].text, 'include=&lt;key&gt; is E2083; `Key<T>` is unchanged; a &gt; b.');
  assert.equal(commits[0].notes[0].text.includes('<key>'), true, 'the commits handed in are not changed');
  assert.deepEqual(escapeNotes(escaped)[0].notes, escaped[0].notes, 'escaping twice changes nothing');

  const notes = await changelog(escaped);
  assert.match(notes, /include=&lt;key&gt; is E2083/);
  assert.match(notes, /list &lt;the&gt; key/);
  const parsed = parseReleaseBody(body([release('parse', '1.0.0', notes), release('config', '1.0.0', 'c')]), quiet);
  assert.deepEqual(components(parsed), ['parse@1.0.0', 'config@1.0.0']);
});
