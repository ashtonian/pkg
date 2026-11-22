import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fetchBranchHead, firstParentCommits, readHistory, typeRanker } from './first-parent.mjs';

// commit builds a release-please ConventionalCommit; a "!" in the header gives
// it the note release-please derives from the subject.
function commit(sha, header, { footer, releaseAs, references = [] } = {}) {
  const [, type, scope, bang, subject] = header.match(/^(\w+)(?:\((.+)\))?(!)?: (.+)$/);
  const notes = [];
  if (bang || footer) notes.push({ title: 'BREAKING CHANGE', text: footer ?? subject });
  const breaking = notes.length > 0;
  if (releaseAs) notes.push({ title: 'RELEASE AS', text: releaseAs });
  return { sha, message: header, type, scope, bareMessage: subject, notes, references, breaking, files: [] };
}

const closes = issue => ({ prefix: '#', action: 'Closes', issue });
const fixes = issue => ({ prefix: '#', action: 'Fixes', issue });

test('a pull request counts once, as its merge', () => {
  const merge = commit('m1', 'feat(parse): report schema errors (#255)');
  const branch = [commit('b1', 'feat(parse): report schema errors'), commit('b2', 'docs(parse): describe the hook')];
  const direct = commit('d1', 'fix(parse): a commit pushed to main');
  const history = { mergeOf: new Map([['b1', 'm1'], ['b2', 'm1']]) };

  const kept = firstParentCommits([merge, ...branch, direct], history);

  assert.deepEqual(kept.map(c => c.sha), ['m1', 'd1']);
});

test('a branch commit stays when its merge is not a conventional commit', () => {
  // GitHub's default "Merge pull request #1 from …" parses as no commit at all.
  const branch = commit('b1', 'feat(parse): accept POST lists');
  const history = { mergeOf: new Map([['b1', 'm1']]) };

  assert.deepEqual(firstParentCommits([branch], history).map(c => c.sha), ['b1']);
});

test('a commit newer than the checkout stays', () => {
  const unknown = commit('n1', 'fix(parse): merged after the checkout');
  assert.deepEqual(firstParentCommits([unknown], { mergeOf: new Map() }).map(c => c.sha), ['n1']);
});

test("a branch commit's breaking description replaces the one the merge's ! repeats", () => {
  const merge = commit('m1', 'fix(parse)!: refuse CockroachDB DSNs (#251)');
  const branch = commit('b1', 'fix(parse)!: refuse CockroachDB DSNs', { footer: 'DialectFromDSN returns ErrUnknownDSN for cockroach://.' });

  const [kept] = firstParentCommits([merge, branch], { mergeOf: new Map([['b1', 'm1']]) });

  assert.equal(kept.breaking, true);
  assert.deepEqual(kept.notes, [{ title: 'BREAKING CHANGE', text: 'DialectFromDSN returns ErrUnknownDSN for cockroach://.' }]);
});

test('a merge without ! still bumps for a breaking branch commit', () => {
  const merge = commit('m1', 'feat(parse): canonical value types (#187)');
  const marked = commit('b1', 'feat(parse)!: canonical value types');
  const described = commit('b2', 'feat(parse)!: interval text', { footer: 'An interval reads as ISO 8601 text.' });

  const [onlyMarked] = firstParentCommits([structuredClone(merge), marked], { mergeOf: new Map([['b1', 'm1']]) });
  assert.equal(onlyMarked.breaking, true);
  assert.deepEqual(onlyMarked.notes, [{ title: 'BREAKING CHANGE', text: 'canonical value types (#187)' }]);

  const [withText] = firstParentCommits([merge, marked, described], { mergeOf: new Map([['b1', 'm1'], ['b2', 'm1']]) });
  assert.deepEqual(withText.notes.map(n => n.text), ['An interval reads as ISO 8601 text.']);
});

test("branch commits' closed issues move to the merge, once each", () => {
  const merge = commit('m1', 'fix(config): keep netip defaults whole (#247)', { references: [closes('186')] });
  const branch = [
    commit('b1', 'fix(config): keep netip values whole', { references: [fixes('186')] }),
    commit('b2', 'fix(config): refuse a second Reloader.Run', { references: [closes('230')] }),
  ];

  const [kept] = firstParentCommits([merge, ...branch], { mergeOf: new Map([['b1', 'm1'], ['b2', 'm1']]) });

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
    const [kept] = firstParentCommits([commit('m1', tc.merge), commit('b1', tc.branch)], { mergeOf: new Map([['b1', 'm1']]) }, {
      warn: message => warnings.push(message),
    });
    assert.equal(kept.type, tc.want, tc.name);
    assert.equal(warnings.length, tc.merge.startsWith(`${tc.want}(`) ? 0 : 1, `${tc.name}: a raised type is warned about`);
  }
});

test("typeRanker follows the paths' changelog sections", () => {
  const stock = typeRanker();
  assert.deepEqual(['feat', 'fix', 'perf', 'docs', 'chore'].map(stock), [2, 1, 1, 0, 0]);
  const shown = typeRanker([undefined, [{ type: 'feat' }, { type: 'docs', section: 'Documentation' }]]);
  assert.equal(shown('docs'), 1, 'a section a path shows calls for a release');
});

test("a branch commit's Release-As moves to the merge", () => {
  const merge = commit('m1', 'feat(parse): stable API (#270)');
  const branch = commit('b1', 'chore(parse): mark 1.0', { releaseAs: '1.0.0' });

  const [kept] = firstParentCommits([merge, branch], { mergeOf: new Map([['b1', 'm1']]) });

  assert.deepEqual(kept.notes, [{ title: 'RELEASE AS', text: '1.0.0' }]);
});

test('one merge commit split into several conventional commits keeps them all', () => {
  // release-please splits a message whose paragraphs start feat:/fix: into one
  // commit each, all with the merge's sha.
  const parts = [commit('m1', 'feat(parse): first (#9)'), commit('m1', 'fix(parse): second')];
  const branch = commit('b1', 'feat(parse): first', { footer: 'Breaks a thing.' });

  const kept = firstParentCommits([...parts, branch], { mergeOf: new Map([['b1', 'm1']]) });

  assert.deepEqual(kept.map(c => c.message), ['feat(parse): first (#9)', 'fix(parse): second']);
  assert.equal(kept[0].breaking, true, 'the fold goes to the first commit of the merge');
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
  const landed = merge('feature', 'feat: the feature (#1)');

  const { mergeOf } = readHistory(work, 'HEAD');

  assert.deepEqual(Object.fromEntries(mergeOf), { [b1]: landed, [sync]: landed, [b2]: landed });
  for (const mainline of [root, onMain, landed]) assert.equal(mergeOf.has(mainline), false);
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
