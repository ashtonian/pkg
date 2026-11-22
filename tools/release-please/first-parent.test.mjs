import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fetchBranchHead, firstParentCommits, readHistory, typeRanker } from './first-parent.mjs';

// commit builds a release-please ConventionalCommit; a "!" in the header gives
// it the note release-please derives from the subject.
function commit(sha, header, { footer, releaseAs, references = [], pullRequest } = {}) {
  const [, type, scope, bang, subject] = header.match(/^([\w-]+)(?:\((.+)\))?(!)?: (.+)$/);
  const notes = [];
  if (bang || footer) notes.push({ title: 'BREAKING CHANGE', text: footer ?? subject });
  const breaking = notes.length > 0;
  if (releaseAs) notes.push({ title: 'RELEASE AS', text: releaseAs });
  return { sha, message: header, type, scope, bareMessage: subject, notes, references, breaking, files: [], pullRequest };
}

// landed describes merges: each {merge: [subject, ...branch shas]} is a
// first-parent merge that brought those commits in, and whose subject is its
// parsed header unless it is unparsed.
function landed(merges, { unparsed = [] } = {}) {
  const mergeOf = new Map();
  const messageOf = new Map();
  const headerOf = new Map();
  for (const [merge, [subject, ...branch]] of Object.entries(merges)) {
    messageOf.set(merge, subject);
    if (!unparsed.includes(merge)) headerOf.set(merge, subject);
    for (const sha of branch) mergeOf.set(sha, merge);
  }
  return { history: { mergeOf, messageOf }, options: { headerOf } };
}

const closes = issue => ({ prefix: '#', action: 'Closes', issue });
const fixes = issue => ({ prefix: '#', action: 'Fixes', issue });

function keep(commits, merges, extra = {}) {
  const { history, options } = landed(merges, extra);
  return firstParentCommits(commits, history, { ...options, ...extra.options });
}

test('a pull request counts once, as its merge', () => {
  const merge = commit('m1', 'feat(parse): report schema errors (#255)');
  const branch = [commit('b1', 'feat(parse): report schema errors'), commit('b2', 'docs(parse): describe the hook')];
  const direct = commit('d1', 'fix(parse): a commit pushed to main');

  const kept = keep([merge, ...branch, direct], { m1: ['feat(parse): report schema errors (#255)', 'b1', 'b2'] });

  assert.deepEqual(kept.map(c => c.sha), ['m1', 'd1']);
});

test('a branch commit stays when release-please cannot parse its merge', () => {
  // A message of only GitHub's default first line parses to no entry.
  const branch = commit('b1', 'feat(parse): accept POST lists');

  const kept = keep([branch], { m1: ['Merge pull request #1 from a/b', 'b1'] }, { unparsed: ['m1'] });

  assert.deepEqual(kept.map(c => c.sha), ['b1']);
});

// The pull request's net change misses this path: its branch touched it and
// took the change back, so the merge counts nowhere here and nor do they.
test("a branch commit goes when its merge's change does not reach this path", () => {
  const reverted = commit('b1', 'fix(httpx): temporary hack');

  const kept = keep([reverted], { m1: ['feat(parse): x (#270)', 'b1'] });

  assert.deepEqual(kept, []);
});

test('a commit newer than the history stays', () => {
  const unknown = commit('n1', 'fix(parse): merged after the history read');
  assert.deepEqual(keep([unknown], {}).map(c => c.sha), ['n1']);
});

// release-please parses each footer trailer of a merge's message into a
// commit of its own, with the merge's sha, ahead of the header.
test("the merge's header takes the branch over, not a trailer release-please split off", () => {
  const trailer = commit('m1', 'Co-authored-by: Pat <pat@example.com>');
  const header = commit('m1', 'fix(parse): tidy reader (#260)');
  const branch = commit('b1', 'feat(parse)!: add Stream', { footer: 'Reader is gone.', references: [closes('5')] });

  const kept = keep([trailer, header, branch], { m1: ['fix(parse): tidy reader (#260)', 'b1'] });

  assert.deepEqual(kept, [trailer, header]);
  assert.equal(trailer.type, 'Co-authored-by');
  assert.deepEqual(trailer.notes, []);
  assert.equal(header.type, 'feat');
  assert.equal(header.breaking, true);
  assert.deepEqual(header.notes, [{ title: 'BREAKING CHANGE', text: 'Reader is gone.' }]);
  assert.deepEqual(header.references.map(r => r.issue), ['5']);
});

test("a pull request's BEGIN_COMMIT_OVERRIDE entries stand as the author wrote them", () => {
  const pullRequest = { mergeCommitOid: 'm1', body: 'BEGIN_COMMIT_OVERRIDE\nfix(parse): only this\nEND_COMMIT_OVERRIDE' };
  // release-please gives every commit of the pull request the override.
  const merge = commit('m1', 'fix(parse): only this', { pullRequest });
  const copy = commit('b1', 'fix(parse): only this', { pullRequest });

  const kept = keep([merge, copy], { m1: ['Merge pull request #9 from a/b', 'b1'] }, { unparsed: ['m1'] });

  assert.deepEqual(kept, [merge]);
  assert.equal(merge.breaking, false);
});

// A pull request merged into another's branch, stacked, lands through the
// outer merge; release-please gives its commits its override's text.
test("a stacked pull request's override folds into the merge it landed through", () => {
  const subject = 'fix(parse): tidy (#12)';
  const stacked = { mergeCommitOid: 'mi', body: 'BEGIN_COMMIT_OVERRIDE\nfeat(parse)!: drop Reader\nEND_COMMIT_OVERRIDE' };
  const header = commit('m2', subject);
  const inner = commit('mi', 'feat(parse)!: drop Reader', { pullRequest: stacked });

  const kept = keep([header, inner], { m2: [subject, 'mi'] });

  assert.deepEqual(kept, [header]);
  assert.equal(header.type, 'feat');
  assert.equal(header.breaking, true);
});

// The merge's own override stands for the whole change, so a branch commit
// GitHub associates with some other pull request goes too, without a warning.
test("a merge listed as its own pull request's override drops every branch commit", () => {
  const warnings = [];
  const own = { mergeCommitOid: 'm1', body: 'BEGIN_COMMIT_OVERRIDE\nfix(parse): one line\nEND_COMMIT_OVERRIDE' };
  const entry = commit('m1', 'fix(parse): one line', { pullRequest: own });
  const elsewhere = commit('b1', 'feat(parse): add X', { pullRequest: { mergeCommitOid: undefined, body: '' } });

  const kept = keep([entry, elsewhere], { m1: ['feat(parse): add X and Y (#20)', 'b1'] }, { options: { warn: m => warnings.push(m) } });

  assert.deepEqual(kept, [entry]);
  assert.equal(entry.type, 'fix');
  assert.deepEqual(warnings, []);
});

test("another pull request's override stays listed when nothing carries its change", () => {
  const other = { mergeCommitOid: 'm9', body: 'BEGIN_COMMIT_OVERRIDE\nfeat(parse)!: the other change\nEND_COMMIT_OVERRIDE' };
  const foreign = commit('b1', 'feat(parse)!: the other change', { pullRequest: other });

  const kept = keep([foreign], { m1: ['Merge pull request #10 from a/b', 'b1'] }, { unparsed: ['m1'] });

  assert.deepEqual(kept, [foreign]);
});

test("a branch commit's breaking description replaces the one the merge's ! repeats", () => {
  const merge = commit('m1', 'fix(parse)!: refuse CockroachDB DSNs (#251)');
  const branch = commit('b1', 'fix(parse)!: refuse CockroachDB DSNs', { footer: 'DialectFromDSN returns ErrUnknownDSN for cockroach://.' });

  const [kept] = keep([merge, branch], { m1: ['fix(parse)!: refuse CockroachDB DSNs (#251)', 'b1'] });

  assert.equal(kept.breaking, true);
  assert.deepEqual(kept.notes, [{ title: 'BREAKING CHANGE', text: 'DialectFromDSN returns ErrUnknownDSN for cockroach://.' }]);
});

test('a merge without ! still bumps for a breaking branch commit', () => {
  const subject = 'feat(parse): canonical value types (#187)';
  const marked = commit('b1', 'feat(parse)!: canonical value types');
  const described = commit('b2', 'feat(parse)!: interval text', { footer: 'An interval reads as ISO 8601 text.' });

  const [onlyMarked] = keep([commit('m1', subject), marked], { m1: [subject, 'b1'] });
  assert.equal(onlyMarked.breaking, true);
  assert.deepEqual(onlyMarked.notes, [{ title: 'BREAKING CHANGE', text: 'canonical value types (#187)' }]);

  const [withText] = keep([commit('m1', subject), marked, described], { m1: [subject, 'b1', 'b2'] });
  assert.deepEqual(withText.notes.map(n => n.text), ['An interval reads as ISO 8601 text.']);
});

test("branch commits' closed issues move to the merge, once each", () => {
  const subject = 'fix(config): keep netip defaults whole (#247)';
  const merge = commit('m1', subject, { references: [closes('186')] });
  const branch = [
    commit('b1', 'fix(config): keep netip values whole', { references: [fixes('186')] }),
    commit('b2', 'fix(config): refuse a second Reloader.Run', { references: [closes('230')] }),
  ];

  const [kept] = keep([merge, ...branch], { m1: [subject, 'b1', 'b2'] });

  assert.deepEqual(kept.references.map(r => r.issue), ['186', '230']);
});

test("a merge takes its branch's highest type, so the release it calls for stays", () => {
  const cases = [
    { name: 'a feature merged as a fix', merge: 'fix(parse): tidy reader (#260)', branch: 'feat(parse): add Stream', want: 'feat' },
    { name: 'a fix merged as a chore', merge: 'chore(parse): sync (#261)', branch: 'fix(parse): close rows', want: 'fix' },
    { name: 'a fix in a feature', merge: 'feat(parse): add Stream (#262)', branch: 'fix(parse): close rows', want: 'feat' },
    { name: 'a test in a fix', merge: 'fix(parse): close rows (#263)', branch: 'test(parse): cover rows', want: 'fix' },
  ];
  for (const tc of cases) {
    const warnings = [];
    const [kept] = keep([commit('m1', tc.merge), commit('b1', tc.branch)], { m1: [tc.merge, 'b1'] }, {
      options: { warn: message => warnings.push(message) },
    });
    assert.equal(kept.type, tc.want, tc.name);
    assert.equal(warnings.length, tc.merge.startsWith(`${tc.want}(`) ? 0 : 1, `${tc.name}: a raised type is warned about`);
  }
});

test('typeRanker follows changelog-sections', () => {
  const stock = typeRanker();
  assert.deepEqual(['feat', 'fix', 'perf', 'docs', 'chore', 'Co-authored-by'].map(stock), [2, 1, 1, 0, 0, 0]);
  const shown = typeRanker([{ type: 'feat' }, { type: 'docs', section: 'Documentation' }]);
  assert.equal(shown('docs'), 1, 'a section the config shows calls for a release');
  assert.equal(shown('fix'), 0, 'a type the config leaves out is hidden');
});

test("a branch commit's Release-As moves to the merge", () => {
  const subject = 'feat(parse): stable API (#270)';
  const branch = commit('b1', 'chore(parse): mark 1.0', { releaseAs: '1.0.0' });

  const [kept] = keep([commit('m1', subject), branch], { m1: [subject, 'b1'] });

  assert.deepEqual(kept.notes, [{ title: 'RELEASE AS', text: '1.0.0' }]);
});

test('a merge split into several conventional commits folds into its header', () => {
  // release-please splits a message whose paragraphs start feat:/fix: into one
  // commit each, all with the merge's sha.
  const subject = 'feat(parse): first (#9)';
  const parts = [commit('m1', subject), commit('m1', 'fix(parse): second')];
  const branch = commit('b1', 'feat(parse): first', { footer: 'Breaks a thing.' });

  const kept = keep([...parts, branch], { m1: [subject, 'b1'] });

  assert.deepEqual(kept.map(c => c.message), [subject, 'fix(parse): second']);
  assert.equal(kept[0].breaking, true);
  assert.equal(kept[1].breaking, false);
});

function scratchRepo(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'first-parent-'));
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
  const merge = (branch, message) => {
    git('merge', '-q', '--no-ff', '-m', message, branch);
    return git('rev-parse', 'HEAD');
  };
  return { dir, work, git, gitAt, commitFile, merge };
}

test('readHistory maps branch commits, sync merges included, to the merge that landed them', t => {
  const { work, git, commitFile, merge } = scratchRepo(t);
  const root = commitFile('a', 'chore: root');
  git('switch', '-q', '-c', 'feature');
  const b1 = commitFile('b', 'feat: on the branch');
  git('switch', '-q', 'main');
  const onMain = commitFile('c', 'fix: on main meanwhile');
  git('switch', '-q', 'feature');
  git('merge', '-q', '--no-edit', 'main');
  const sync = git('rev-parse', 'HEAD');
  const b2 = commitFile('d', 'feat: after the sync');
  git('switch', '-q', 'main');
  const landing = merge('feature', 'feat: the feature (#1)');

  const { mergeOf, messageOf } = readHistory(work, 'HEAD');

  assert.deepEqual(Object.fromEntries(mergeOf), { [b1]: landing, [sync]: landing, [b2]: landing });
  for (const mainline of [root, onMain, landing]) assert.equal(mergeOf.has(mainline), false);
  assert.deepEqual(Object.fromEntries(messageOf), { [landing]: 'feat: the feature (#1)' }, 'only first-parent merges have messages');
});

// A branch cut from another unmerged branch: the first merge claims the
// commits both branches hold, the second only its own.
test('readHistory gives a commit to the oldest merge that landed it', t => {
  const { work, git, commitFile, merge } = scratchRepo(t);
  commitFile('a', 'chore: root');
  git('switch', '-q', '-c', 'first');
  const shared = commitFile('b', 'feat: first');
  git('switch', '-q', '-c', 'second');
  const own = commitFile('c', 'feat: second');
  git('switch', '-q', 'main');
  const m1 = merge('first', 'feat: first (#1)');
  const m2 = merge('second', 'feat: second (#2)');

  const { mergeOf } = readHistory(work, 'HEAD');

  assert.deepEqual(Object.fromEntries(mergeOf), { [shared]: m1, [own]: m2 });
});

test('a listed merge with no header keeps its branch commits, and warns once', () => {
  const warnings = [];
  const entry = commit('m1', 'feat(parse): one of two entries');
  const branch = [commit('b1', 'feat(parse): add Stream'), commit('b2', 'fix(parse): close rows')];

  const kept = keep([entry, ...branch], { m1: ['Merge pull request #9 from a/b', 'b1', 'b2'] }, {
    unparsed: ['m1'],
    options: { warn: m => warnings.push(m) },
  });

  assert.deepEqual(kept.map(c => c.sha), ['m1', 'b1', 'b2']);
  assert.equal(warnings.length, 1);
});

test('readHistory refuses a shallow clone, which would map nothing', t => {
  const { dir, work, gitAt, commitFile } = scratchRepo(t);
  commitFile('a', 'chore: root');
  commitFile('b', 'fix: second');
  const shallow = path.join(dir, 'shallow');
  gitAt(dir)('clone', '-q', '--depth', '1', `file://${work}`, shallow);

  assert.throws(() => readHistory(shallow, 'HEAD'), /shallow clone/);
});

test('fetchBranchHead returns the branch as the remote has it now', t => {
  const { dir, work, gitAt, commitFile } = scratchRepo(t);
  commitFile('a', 'chore: root');
  const remote = path.join(dir, 'remote.git');
  gitAt(dir)('clone', '-q', '--bare', work, remote);
  const clone = path.join(dir, 'clone');
  gitAt(dir)('clone', '-q', remote, clone);
  gitAt(work)('remote', 'add', 'origin', remote);
  const pushed = commitFile('b', 'fix: pushed after the clone');
  gitAt(work)('push', '-q', 'origin', 'main');

  assert.equal(fetchBranchHead(clone, 'origin', 'main'), pushed);
  assert.equal(readHistory(clone, pushed).mergeOf.size, 0);
});

// A merge listed here whose entries don't carry its subject (a message
// release-please rewrote some other way) leaves its branch commits listed:
// duplicated rather than lost.
test('a listed merge without its header keeps its branch commits, and warns once', () => {
  const warnings = [];
  const other = commit('m1', 'feat(parse): something else (#9)');
  const branch = [commit('b1', 'feat(parse)!: add Stream'), commit('b2', 'fix(parse): close rows')];

  const kept = keep([other, ...branch], { m1: ['feat(parse): add Stream (#9)', 'b1', 'b2'] }, { options: { warn: m => warnings.push(m) } });

  assert.deepEqual(kept.map(c => c.sha), ['m1', 'b1', 'b2']);
  assert.equal(warnings.length, 1);
});

test("readHistory keeps a merge's whole message", t => {
  const { work, git, commitFile } = scratchRepo(t);
  commitFile('a', 'chore: root');
  git('switch', '-q', '-c', 'feature');
  commitFile('b', 'feat: on the branch');
  git('switch', '-q', 'main');
  const message = 'feat: the feature (#1)\nwrapped onto a second line\n\nCo-authored-by: Pat <pat@example.com>';
  git('merge', '-q', '--no-ff', '-m', message, 'feature');

  const { messageOf } = readHistory(work, 'HEAD');

  assert.deepEqual([...messageOf.values()], [message]);
});
