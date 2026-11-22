// First-parent release notes. Pull requests land as merge commits whose
// subjects are conventional, and their branch commits are conventional too.
// release-please reads the whole history, so each change would count once
// per branch commit and again as its merge. These functions keep the merge.
import { execFileSync } from 'node:child_process';

const BREAKING = 'BREAKING CHANGE';
const RELEASE_AS = 'RELEASE AS';

// The changelog sections release-please uses without changelog-sections
// (conventional-changelog-conventionalcommits' defaults).
const DEFAULT_SECTIONS = [
  { type: 'feat' }, { type: 'feature' }, { type: 'fix' }, { type: 'perf' }, { type: 'revert' },
  ...['docs', 'style', 'chore', 'refactor', 'test', 'build', 'ci'].map(type => ({ type, hidden: true })),
];

function gitIn(repoDir) {
  return (...args) => execFileSync('git', ['-C', repoDir, ...args], { encoding: 'utf8', maxBuffer: 256 << 20 });
}

// fetchBranchHead fetches branch from remote into repoDir and returns the
// commit it names there now.
export function fetchBranchHead(repoDir, remote, branch) {
  const git = gitIn(repoDir);
  git('fetch', '--quiet', '--no-tags', remote, `refs/heads/${branch}`);
  return git('rev-parse', '--verify', 'FETCH_HEAD^{commit}').trim();
}

// readHistory maps every commit a merge on ref's first-parent chain brought in
// to that merge, from one read of the history. A commit on the chain itself,
// or one newer than ref, has no entry.
export function readHistory(repoDir, ref) {
  const git = gitIn(repoDir);
  const parents = new Map();
  for (const line of git('rev-list', '--parents', ref).split('\n')) {
    if (!line) continue;
    const [sha, ...shas] = line.split(' ');
    parents.set(sha, shas);
  }
  const chain = [];
  for (let sha = git('rev-parse', '--verify', `${ref}^{commit}`).trim(); sha; sha = parents.get(sha)[0]) {
    chain.push(sha);
  }
  const onChain = new Set(chain);
  const mergeOf = new Map();
  // Oldest first: everything an older chain commit reaches is on the chain or
  // already mapped, so a walk from a merge's other parents stops there and
  // claims exactly what that merge brought in.
  for (const merge of chain.reverse()) {
    const pending = parents.get(merge).slice(1);
    while (pending.length > 0) {
      const sha = pending.pop();
      if (onChain.has(sha) || mergeOf.has(sha)) continue;
      mergeOf.set(sha, merge);
      pending.push(...(parents.get(sha) ?? []));
    }
  }
  return { mergeOf };
}

// typeRanker orders commit types by what they call for in a release: a
// feature, then any other type the changelog shows, then hidden types, which
// alone cut no release. sectionLists are the release paths' changelog-sections;
// a path without them uses release-please's defaults.
export function typeRanker(sectionLists = []) {
  const visible = new Set();
  for (const sections of sectionLists.length > 0 ? sectionLists : [undefined]) {
    for (const section of sections ?? DEFAULT_SECTIONS) {
      if (!section.hidden) visible.add(section.type);
    }
  }
  return type => (type === 'feat' || type === 'feature' ? 2 : visible.has(type) ? 1 : 0);
}

// firstParentCommits drops each conventional commit a merge brought in when
// that merge is itself a conventional commit of the same list, so a pull
// request counts once, as its merge. The merge takes over what the dropped
// commit calls for: a higher-ranked type (a fix merged as a chore still cuts
// a release), its breaking change and Release-As, and its issue references.
// A commit whose merge is not in the list is kept: a merge with a subject
// release-please cannot parse would otherwise drop its change.
export function firstParentCommits(commits, history, { rank = typeRanker(), warn = () => {} } = {}) {
  const records = new Map();
  for (const commit of commits) {
    if (!records.has(commit.sha)) records.set(commit.sha, commit);
  }
  const kept = [];
  for (const commit of commits) {
    const record = records.get(history.mergeOf.get(commit.sha));
    if (record === undefined) {
      kept.push(commit);
      continue;
    }
    foldType(record, commit, rank, warn);
    foldBreaking(record, commit);
    foldReleaseAs(record, commit);
    foldReferences(record, commit);
  }
  return kept;
}

function foldType(record, dropped, rank, warn) {
  if (rank(dropped.type) <= rank(record.type)) return;
  warn(`${record.sha.slice(0, 7)} (${record.message}) counts as ${dropped.type}, from ${dropped.sha.slice(0, 7)} (${dropped.message}); its subject's type is lower than its change's`);
  record.type = dropped.type;
}

const isBreaking = note => note.title === BREAKING;

// foldBreaking marks record breaking when dropped was. A description in
// dropped's BREAKING CHANGE footer replaces the note record's own "!" gave it,
// which only repeats its subject.
function foldBreaking(record, dropped) {
  if (!dropped.breaking) return;
  record.breaking = true;
  const described = dropped.notes.filter(note => isBreaking(note) && note.text !== dropped.bareMessage);
  if (described.length === 0) {
    if (!record.notes.some(isBreaking)) record.notes.push({ title: BREAKING, text: record.bareMessage });
    return;
  }
  record.notes = record.notes.filter(note => !(isBreaking(note) && note.text === record.bareMessage));
  for (const note of described) {
    if (!record.notes.some(n => isBreaking(n) && n.text === note.text)) record.notes.push({ ...note });
  }
}

function foldReleaseAs(record, dropped) {
  for (const note of dropped.notes) {
    if (note.title !== RELEASE_AS) continue;
    if (!record.notes.some(n => n.title === RELEASE_AS && n.text === note.text)) record.notes.push({ ...note });
  }
}

// foldReferences adds dropped's issue references; one issue is one reference,
// whether a footer says Closes or Fixes.
function foldReferences(record, dropped) {
  const key = ref => [ref.owner, ref.repository, ref.prefix, ref.issue].join('|');
  const seen = new Set(record.references.map(key));
  for (const ref of dropped.references) {
    if (seen.has(key(ref))) continue;
    seen.add(key(ref));
    record.references.push({ ...ref });
  }
}
