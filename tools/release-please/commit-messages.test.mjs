import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';
import { parseConventionalCommits } from 'release-please/build/src/commit.js';
import { checkPullRequest, entries, messageProblems, pullRequestCommits } from './commit-messages.mjs';

// 4ef1715, the commit whose BREAKING CHANGE the parse 0.15.0 notes lost (#289).
const BLOB = `fix(parse)!: read a BLOB as no decimal in parse's SQLite functions

decimals.Value and Float32Value read a []byte as TEXT, so
parse_decimal(CAST('10.50' AS BLOB)) was '10.5', its key equalled the
key of '10.5', and parse_decimal_float32 of a BLOB of '2.50' was '2.5'.

BREAKING CHANGE: parse_decimal, parse_decimal_key and parse_decimal_float32 read a BLOB as NULL.`;

// Dependabot's commit on #362, cut to its first update.
const DEPENDABOT = `chore(deps): bump the go group across 7 directories with 5 updates

Bumps the go group with 1 update in the /config/examples/cookbook directory: [github.com/aws/aws-sdk-go-v2/service/secretsmanager](https://github.com/aws/aws-sdk-go-v2).
Bumps the go group with 1 update in the /tools/modarchive directory: [golang.org/x/mod](https://github.com/golang/mod).


Updates \`github.com/aws/aws-sdk-go-v2/service/secretsmanager\` from 1.50.12 to 1.51.0
- [Release notes](https://github.com/aws/aws-sdk-go-v2/releases)
- [Commits](https://github.com/aws/aws-sdk-go-v2/compare/service/chime/v1.50.12...service/s3/v1.51.0)

---
updated-dependencies:
- dependency-name: github.com/aws/aws-sdk-go-v2/service/secretsmanager
  dependency-version: 1.51.0
  dependency-type: direct:production
  update-type: version-update:semver-minor
  dependency-group: go
...

Signed-off-by: dependabot[bot] <support@github.com>`;

const override = text => `Summary.\n\nBEGIN_COMMIT_OVERRIDE\n${text}\nEND_COMMIT_OVERRIDE\n`;

// Each case's problems as patterns, in order; none means release-please
// reads every entry whole.
const cases = [
  { name: 'a subject alone', message: 'fix(parse): close rows', want: [] },
  {
    name: 'a breaking commit with one footer and references',
    message: 'feat(parse)!: drop Reader\n\nReader is gone.\n\nBREAKING CHANGE: use Stream.\nCloses #5\nRefs #6',
    want: [],
  },
  { name: "Dependabot's grouped update", message: DEPENDABOT, want: [] },
  {
    name: "Dependabot's development update in a directory",
    message: DEPENDABOT.replace(/^.*\n/, 'chore(deps-dev): bump typescript from 6.0.2 to 6.1.0 in /parse/sdk/ts\n'),
    want: [],
  },
  { name: 'a body line whose call closes on it', message: 'fix(parse): x\n\nScan(dst) was wrong.', want: [] },
  { name: 'a call later in a body line', message: 'fix(parse): x\n\nso parse_decimal(CAST(x AS BLOB)) was wrong.', want: [] },
  {
    name: 'two entries, a footer each',
    message: 'feat(a)!: one\n\nBREAKING CHANGE: a breaks.\n\nfix(b): two\n\nBREAKING CHANGE: b breaks.',
    want: [],
  },
  {
    name: 'nested commits, a footer each',
    message: override('feat(a)!: one\n\nBREAKING CHANGE: a breaks.\n\nfix(b): two\n\nBEGIN_NESTED_COMMIT\nfeat(c)!: three\n\nBREAKING CHANGE: c breaks.\nEND_NESTED_COMMIT\nBEGIN_NESTED_COMMIT\n\nfeat(d)!: four\n\nBREAKING CHANGE: d breaks.\n\nEND_NESTED_COMMIT'),
    want: [],
  },
  { name: 'the #289 body line', message: BLOB, want: [/^release-please cannot parse it \(unexpected token '\(' at 4:19, valid tokens \[\)\]\)/] },
  { name: 'a call spanning two lines', message: 'fix(a): x\n\nScan(a,\nb) was wrong.', want: [/cannot parse it \(unexpected token '\\n' at 3:8/] },
  { name: 'wip', message: 'wip', want: [/cannot parse it/] },
  {
    name: 'two BREAKING CHANGE footers',
    message: 'feat(a)!: one\n\nBREAKING CHANGE: first.\nBREAKING CHANGE: second.',
    want: [/^it has 2 BREAKING CHANGE footers, and release-please keeps only the last/],
  },
  {
    name: 'a BREAKING CHANGE paragraph and a BREAKING-CHANGE footer',
    message: 'feat(a): one\n\nBREAKING CHANGE: first.\n\nMore.\n\nBREAKING-CHANGE: second.',
    want: [/has 2 BREAKING CHANGE footers/],
  },
  {
    name: "an override's second breaking header, swallowed with its footer",
    message: override("feat(a)!: one\n\nBREAKING CHANGE: a's break.\n\nfeat(b)!: two\n\nBREAKING CHANGE: b's break."),
    want: [/^it has 2 BREAKING CHANGE footers/, /^release-please reads "feat\(b\)!: two" as part of the entry before it, .* put each further breaking entry in its own BEGIN_NESTED_COMMIT … END_NESTED_COMMIT block$/],
  },
  {
    name: "an override's breaking header with a body, after a plain one",
    message: override('fix(a): one\n\nfeat(b)!: two\n\nIts body.'),
    want: [/^release-please reads "feat\(b\)!: two" as part of the entry before it/],
  },
  {
    name: 'a breaking header after a nested commit',
    message: override('fix(a): one\nBEGIN_NESTED_COMMIT\nfeat(b): two\nEND_NESTED_COMMIT\n\nfeat(c)!: three'),
    want: [/^release-please reads "feat\(c\)!: three" as part of the entry before it/],
  },
  {
    name: 'two headers in one nested commit',
    message: override('fix(a): one\nBEGIN_NESTED_COMMIT\nfeat(b): two\n\nfix(c): three\n\nIts body.\nEND_NESTED_COMMIT'),
    want: [/^release-please reads "fix\(c\): three" as part of the nested commit before it/],
  },
  {
    name: 'a swallowed header in a description written with CRLF',
    message: override('fix(a): one\n\nfeat(b)!: two\n\nIts body.').replaceAll('\n', '\r\n'),
    want: [/^release-please reads "feat\(b\)!: two"/],
  },
  {
    name: 'an entry that does not parse beside one that does',
    message: 'fix(a): one\n\nBEGIN_NESTED_COMMIT\nfeat(b): two\n\nScan(x(y)) was wrong.\nEND_NESTED_COMMIT',
    want: [/^release-please cannot parse its entry "feat\(b\): two" \(unexpected token '\(' at 3:7/],
  },
];

test('messageProblems names what release-please loses of a message', () => {
  for (const tc of cases) {
    const text = tc.message.includes('BEGIN_COMMIT_OVERRIDE') ? overrideText(tc.message) : tc.message;
    const problems = messageProblems(text);
    assert.equal(problems.length, tc.want.length, `${tc.name}: ${JSON.stringify(problems)}`);
    tc.want.forEach((pattern, i) => assert.match(problems[i], pattern, tc.name));
  }
});

const overrideText = body => body.split('BEGIN_COMMIT_OVERRIDE')[1].split('END_COMMIT_OVERRIDE')[0].trim();

// release-please parses a nested commit's text as it is, so wrapping each
// entry in one parses that entry alone: the entries must parse, together, to
// what release-please makes of the whole message. An upgrade that splits a
// message differently fails here.
test('entries split a message as release-please does', () => {
  const read = message => {
    const failures = [];
    const logger = { error() {}, warn() {}, info() {}, trace() {}, debug: m => failures.push(m) };
    const parsed = parseConventionalCommits([{ sha: 's', message, files: [] }], logger)
      .map(({ message: header, type, scope, breaking, notes, references }) => ({ header, type, scope, breaking, notes, references }));
    return { parsed, failures: failures.filter(m => m.startsWith('commit could not be parsed')).length };
  };
  for (const tc of cases) {
    const message = tc.message.includes('BEGIN_COMMIT_OVERRIDE') ? overrideText(tc.message) : tc.message;
    const alone = entries(message).map(({ text }) => read(`BEGIN_NESTED_COMMIT${text}END_NESTED_COMMIT`));
    const whole = read(message);
    assert.deepEqual(alone.flatMap(e => e.parsed), whole.parsed, tc.name);
    assert.equal(alone.reduce((sum, e) => sum + e.failures, 0), whole.failures, tc.name);
    // The release-please override path reads the same text.
    const viaOverride = parseConventionalCommits([{ sha: 's', message: 'x', files: [], pullRequest: { body: override(message) } }], { debug() {}, error() {}, warn() {}, info() {}, trace() {} });
    assert.deepEqual(viaOverride.map(c => c.message), whole.parsed.map(c => c.header), tc.name);
  }
});

test('checkPullRequest reads the override in place of the commits', () => {
  const commits = [
    { sha: 'a'.repeat(40), message: 'fix(parse): close rows' },
    { sha: 'b'.repeat(40), message: BLOB },
  ];
  const plain = checkPullRequest(commits, 'No override here.');
  assert.deepEqual(plain.map(f => [f.source, f.replaced]), [[`bbbbbbb ${BLOB.split('\n')[0]}`, false]]);

  const fixed = checkPullRequest(commits, override('fix(parse)!: read a BLOB as no decimal\n\nBREAKING CHANGE: a BLOB is NULL.'));
  assert.deepEqual(fixed.map(f => [f.source, f.replaced]), [[`bbbbbbb ${BLOB.split('\n')[0]}`, true]]);

  const broken = checkPullRequest(commits.slice(0, 1), override('fix(a): one\n\nfeat(b)!: two\n\nIts body.'));
  assert.deepEqual(broken.map(f => [f.source, f.replaced, f.problems.length]), [['the commit override', false, 1]]);

  assert.deepEqual(checkPullRequest(commits.slice(0, 1), override('   ')), [], 'an empty override is none');
});

function scratchRepo(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'commit-messages-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const env = { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' };
  const gitAt = repo => (...args) => execFileSync('git', ['-C', repo, '-c', 'user.name=t', '-c', 'user.email=t@example.com', ...args], { encoding: 'utf8', env }).trim();
  const work = path.join(dir, 'work');
  fs.mkdirSync(work);
  const git = gitAt(work);
  git('init', '-q', '-b', 'main');
  const commitFile = (name, message) => {
    fs.writeFileSync(path.join(work, name), message);
    git('add', name);
    git('commit', '-q', '-m', message);
    return git('rev-parse', 'HEAD');
  };
  return { dir, work, git, gitAt, commitFile };
}

test('pullRequestCommits lists what the branch brings, without merges or what it reverted', t => {
  const { work, git, commitFile } = scratchRepo(t);
  commitFile('a', 'chore: root');
  const onMain = commitFile('m', 'feat: landed earlier');
  git('switch', '-q', '-c', 'feature');
  const first = commitFile('b', 'feat(a): first');
  const added = commitFile('c', 'wip');
  git('revert', '--no-edit', added);
  // A revert of a commit main holds is the branch's own change.
  git('revert', '--no-edit', onMain);
  const revertOfMain = git('rev-parse', 'HEAD');
  git('switch', '-q', 'main');
  commitFile('d', 'fix: on main meanwhile');
  git('switch', '-q', 'feature');
  git('merge', '-q', '--no-edit', 'main');
  const last = commitFile('e', BLOB);

  const commits = pullRequestCommits(work, 'main', 'feature');

  assert.deepEqual(commits.map(c => c.sha), [first, revertOfMain, last]);
  assert.equal(commits[2].message, BLOB);
  const findings = checkPullRequest(commits, '');
  assert.deepEqual(findings.map(f => f.source.slice(0, 7)), [revertOfMain.slice(0, 7), last.slice(0, 7)], "git's revert subject does not parse");
});

test('pullRequestCommits refuses a shallow clone', t => {
  const { dir, work, gitAt, commitFile } = scratchRepo(t);
  commitFile('a', 'chore: root');
  commitFile('b', 'fix: second');
  const shallow = path.join(dir, 'shallow');
  gitAt(dir)('clone', '-q', '--depth', '1', `file://${work}`, shallow);

  assert.throws(() => pullRequestCommits(shallow, 'HEAD~1', 'HEAD'), /shallow clone/);
});

const CHECK = fileURLToPath(new URL('./check-commits.mjs', import.meta.url));

test('check-commits fails naming each commit release-please cannot read', t => {
  const { dir, work, git, commitFile } = scratchRepo(t);
  commitFile('a', 'chore: root');
  git('switch', '-q', '-c', 'feature');
  commitFile('b', 'feat(a): fine');
  const bad = commitFile('c', BLOB);
  const description = path.join(dir, 'description.md');
  const run = body => {
    fs.writeFileSync(description, body);
    const args = [CHECK, '--checkout', work, '--base', 'main', '--head', 'feature', '--body-file', description];
    return spawnSync(process.execPath, args, { encoding: 'utf8', env: { ...process.env, GITHUB_ACTIONS: 'true' } });
  };

  const failed = run('');
  assert.equal(failed.status, 1, failed.stderr);
  assert.match(failed.stdout, new RegExp(`^::error title=Release notes::${bad.slice(0, 7)} fix\\(parse\\)!: read a BLOB .*unexpected token '\\(' at 4:19`, 'm'));
  assert.match(failed.stdout, /^2 commits in main\.\.feature read as release-please reads them; fix the errors above/m);

  const replaced = run(override('fix(parse)!: read a BLOB as no decimal\n\nBREAKING CHANGE: a BLOB is NULL.'));
  assert.equal(replaced.status, 0, replaced.stdout);
  assert.match(replaced.stdout, new RegExp(`^::warning title=Release notes::${bad.slice(0, 7)} .*release-please reads the commit override in its place$`, 'm'));

  const swallowed = run(override('fix(a): one\n\nfeat(b)!: two\n\nIts body.'));
  assert.equal(swallowed.status, 1);
  assert.match(swallowed.stdout, /^::error title=Release notes::the commit override: release-please reads "feat\(b\)!: two"/m);
});
