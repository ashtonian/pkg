// First-parent release notes: a pull request merged with a merge commit
// counts once, as its merge, though its branch commits are conventional too.
// README.md describes each rule.
import { execFileSync } from 'node:child_process';

const BREAKING = 'BREAKING CHANGE';
const RELEASE_AS = 'RELEASE AS';

// conventional-changelog-conventionalcommits' sections, which release-please
// uses without changelog-sections.
const DEFAULT_SECTIONS = [
  { type: 'feat' }, { type: 'feature' }, { type: 'fix' }, { type: 'perf' }, { type: 'revert' },
  ...['docs', 'style', 'chore', 'refactor', 'test', 'build', 'ci'].map(type => ({ type, hidden: true })),
];

function gitIn(repoDir) {
  return (...args) => execFileSync('git', ['-C', repoDir, ...args], { encoding: 'utf8', maxBuffer: 256 << 20 });
}

export function fetchBranchHead(repoDir, remote, branch) {
  const git = gitIn(repoDir);
  git('fetch', '--quiet', '--no-tags', remote, `refs/heads/${branch}`);
  return git('rev-parse', '--verify', 'FETCH_HEAD^{commit}').trim();
}

// readHistory returns mergeOf, each commit a merge on ref's first-parent chain
// brought in mapped to that merge; messageOf, those merges' messages; and
// revertedWithin, the commits a revert on the same merge's branch undid, the
// reverts included.
export function readHistory(repoDir, ref) {
  const git = gitIn(repoDir);
  // A shallow clone maps nothing and would list every branch commit again.
  if (git('rev-parse', '--is-shallow-repository').trim() === 'true') {
    throw new Error(`${repoDir} is a shallow clone; first-parent needs the whole history (fetch-depth: 0)`);
  }
  const parents = new Map();
  for (const line of git('rev-list', '--parents', ref).split('\n')) {
    if (!line) continue;
    const [sha, ...shas] = line.split(' ');
    parents.set(sha, shas);
  }
  const messages = new Map();
  for (const record of git('rev-list', '--no-commit-header', '--format=%x00%H%x1f%B', ref).split('\0')) {
    const separator = record.indexOf('\x1f');
    if (separator < 0) continue;
    messages.set(record.slice(0, separator), record.slice(separator + 1).replace(/\n+$/, ''));
  }
  const chain = [];
  for (let sha = git('rev-parse', '--verify', `${ref}^{commit}`).trim(); sha; sha = parents.get(sha)[0]) {
    chain.push(sha);
  }
  const onChain = new Set(chain);
  const mergeOf = new Map();
  // Oldest first: an older chain commit reaches only chain commits and ones
  // already mapped, so each walk claims exactly what its merge brought in.
  for (const merge of chain.reverse()) {
    const pending = parents.get(merge).slice(1);
    while (pending.length > 0) {
      const sha = pending.pop();
      if (onChain.has(sha) || mergeOf.has(sha)) continue;
      mergeOf.set(sha, merge);
      pending.push(...(parents.get(sha) ?? []));
    }
  }
  const messageOf = new Map();
  for (const sha of chain) {
    if (parents.get(sha).length > 1) messageOf.set(sha, messages.get(sha));
  }
  const revertedWithin = new Set();
  for (const [sha, merge] of mergeOf) {
    for (const [, reverted] of messages.get(sha).matchAll(/^This reverts commit ([0-9a-f]{40})/gm)) {
      if (mergeOf.get(reverted) === merge) revertedWithin.add(sha).add(reverted);
    }
  }
  return { mergeOf, messageOf, revertedWithin };
}

// typeRanker ranks a feature over the other types the changelog shows, and
// those over hidden types, which alone cut no release.
export function typeRanker(sections = DEFAULT_SECTIONS) {
  const shown = new Set(sections.filter(section => !section.hidden).map(section => section.type));
  return type => {
    if (type === 'feat' || type === 'feature') return 2;
    if (shown.has(type)) return 1;
    return 0;
  };
}

// firstParentCommits drops the commits a merge with a header brought in, and
// folds into that header what they call for. headerOf maps a merge to the
// header release-please's parse of its message gives its first line.
export function firstParentCommits(commits, history, { headerOf, rank = typeRanker(), warn = () => {} }) {
  const listed = new Set(commits.map(commit => commit.sha));
  const headers = new Map();
  // A merge whose own pull request sets an override is listed as that
  // override, which its author wrote to stand for the whole change.
  const overridden = new Set();
  for (const commit of commits) {
    if (!headers.has(commit.sha) && headerOf.get(commit.sha) === commit.message) headers.set(commit.sha, commit);
    if (ownOverride(commit, commit.sha)) overridden.add(commit.sha);
  }
  const unmatched = new Set();
  const raised = new Map();
  const kept = [];
  for (const commit of commits) {
    const merge = history.mergeOf.get(commit.sha);
    if (merge === undefined) {
      kept.push(commit);
      continue;
    }
    // The second test catches a copy of the merge's override in a path the
    // merge's own change does not reach.
    if (overridden.has(merge) || ownOverride(commit, merge)) continue;
    // Without a header, the branch commits are the only record of the change.
    if (!headerOf.has(merge)) {
      if (listed.has(merge) && !unmatched.has(merge)) {
        unmatched.add(merge);
        warn(`${merge.slice(0, 7)} is listed, but no entry of it is its header; its branch commits stay listed`);
      }
      kept.push(commit);
      continue;
    }
    const header = headers.get(merge);
    if (header === undefined) {
      // Not listed: the merge's net change misses this path.
      if (!listed.has(merge)) continue;
      if (!unmatched.has(merge)) {
        unmatched.add(merge);
        warn(`${merge.slice(0, 7)} (${headerOf.get(merge)}) has no entry with its header; its branch commits stay listed`);
      }
      kept.push(commit);
      continue;
    }
    // A change the branch undid calls for nothing.
    if (history.revertedWithin?.has(commit.sha)) continue;
    // A commit carrying another pull request's override folds too: that pull
    // request landed through this merge, stacked on its branch.
    if (rank(commit.type) > rank(header.type)) {
      header.type = commit.type;
      raised.set(header, commit);
    }
    foldBreaking(header, commit);
    foldReleaseAs(header, commit);
    foldReferences(header, commit);
  }
  for (const [header, from] of raised) {
    warn(`${header.sha.slice(0, 7)} (${header.message}) counts as ${header.type}, from ${from.sha.slice(0, 7)} (${from.message}); its subject's type is lower than its change's`);
  }
  return kept;
}

// ownOverride reports a commit release-please gave the override of the pull
// request merged as merge, read as release-please reads an override.
function ownOverride(commit, merge) {
  const pr = commit.pullRequest;
  if (pr === undefined || pr.mergeCommitOid !== merge) return false;
  const override = (pr.body ?? '').split('BEGIN_COMMIT_OVERRIDE')[1] ?? '';
  return override.split('END_COMMIT_OVERRIDE')[0].trim() !== '';
}

const isBreaking = note => note.title === BREAKING;

// foldBreaking prefers a footer's description to the note a "!" gives the
// header, which only repeats its subject.
function foldBreaking(header, dropped) {
  if (!dropped.breaking) return;
  header.breaking = true;
  const described = dropped.notes.filter(note => isBreaking(note) && note.text !== dropped.bareMessage);
  if (described.length === 0) {
    if (!header.notes.some(isBreaking)) header.notes.push({ title: BREAKING, text: header.bareMessage });
    return;
  }
  header.notes = header.notes.filter(note => !(isBreaking(note) && note.text === header.bareMessage));
  for (const note of described) {
    if (!header.notes.some(n => isBreaking(n) && n.text === note.text)) header.notes.push({ ...note });
  }
}

function foldReleaseAs(header, dropped) {
  for (const note of dropped.notes) {
    if (note.title !== RELEASE_AS) continue;
    if (!header.notes.some(n => n.title === RELEASE_AS && n.text === note.text)) header.notes.push({ ...note });
  }
}

// One issue is one reference, whether a footer says Closes or Fixes.
function foldReferences(header, dropped) {
  const key = ref => [ref.owner, ref.repository, ref.prefix, ref.issue].join('|');
  const seen = new Set(header.references.map(key));
  for (const ref of dropped.references) {
    if (seen.has(key(ref))) continue;
    seen.add(key(ref));
    header.references.push({ ...ref });
  }
}
