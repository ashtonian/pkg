import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { firstParentCommits, readHistory } from './first-parent.mjs';

// commit builds a release-please ConventionalCommit; a "!" in the header gives
// it the note release-please derives from the subject.
function commit(sha, header, { footer, references = [] } = {}) {
  const [, type, scope, bang, subject] = header.match(/^(\w+)(?:\((.+)\))?(!)?: (.+)$/);
  const notes = [];
  if (bang || footer) notes.push({ title: 'BREAKING CHANGE', text: footer ?? subject });
  return { sha, message: header, type, scope, bareMessage: subject, notes, references, breaking: notes.length > 0, files: [] };
}

const closes = issue => ({ prefix: '#', action: 'Closes', issue });

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
    commit('b1', 'fix(config): keep netip values whole', { references: [closes('186')] }),
    commit('b2', 'fix(config): refuse a second Reloader.Run', { references: [closes('230')] }),
  ];

  const [kept] = firstParentCommits([merge, ...branch], { mergeOf: new Map([['b1', 'm1'], ['b2', 'm1']]) });

  assert.deepEqual(kept.references.map(r => r.issue), ['186', '230']);
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

test('readHistory maps branch commits, sync merges included, to the merge that landed them', t => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'first-parent-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const git = (...args) =>
    execFileSync('git', ['-C', dir, ...args], {
      encoding: 'utf8',
      env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' },
    }).trim();
  const commitFile = (name, message) => {
    fs.writeFileSync(path.join(dir, name), message);
    git('add', name);
    git('-c', 'user.name=t', '-c', 'user.email=t@example.com', 'commit', '-q', '-m', message);
    return git('rev-parse', 'HEAD');
  };
  git('init', '-q', '-b', 'main');
  const root = commitFile('a', 'chore: root');
  git('switch', '-q', '-c', 'feature');
  const b1 = commitFile('b', 'feat: on the branch');
  git('switch', '-q', 'main');
  const onMain = commitFile('c', 'fix: on main meanwhile');
  git('switch', '-q', 'feature');
  git('-c', 'user.name=t', '-c', 'user.email=t@example.com', 'merge', '-q', '--no-edit', 'main');
  const sync = git('rev-parse', 'HEAD');
  const b2 = commitFile('d', 'feat: after the sync');
  git('switch', '-q', 'main');
  git('-c', 'user.name=t', '-c', 'user.email=t@example.com', 'merge', '-q', '--no-ff', '-m', 'feat: the feature (#1)', 'feature');
  const merge = git('rev-parse', 'HEAD');

  const { mergeOf } = readHistory(dir, 'HEAD');

  assert.deepEqual(Object.fromEntries(mergeOf), { [b1]: merge, [sync]: merge, [b2]: merge });
  for (const mainline of [root, onMain, merge]) assert.equal(mergeOf.has(mainline), false);
});
