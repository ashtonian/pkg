// First-parent release notes. Pull requests land as merge commits whose
// subjects are conventional, and their branch commits are conventional too.
// release-please reads the whole history, so each change would count once
// per branch commit and again as its merge. These functions keep the merge.
import { execFileSync } from 'node:child_process';

const BREAKING = 'BREAKING CHANGE';

// readHistory maps every commit a merge on ref's first-parent chain brought in
// to that merge. A commit on the chain itself, or one newer than ref, has no
// entry.
export function readHistory(repoDir, ref) {
  const git = (...args) =>
    execFileSync('git', ['-C', repoDir, ...args], { encoding: 'utf8', maxBuffer: 64 << 20 });
  const mergeOf = new Map();
  for (const line of git('rev-list', '--first-parent', '--merges', '--parents', ref).split('\n')) {
    if (!line) continue;
    const [merge, mainline, ...branches] = line.split(' ');
    for (const sha of git('rev-list', ...branches, '--not', mainline).split('\n')) {
      if (sha) mergeOf.set(sha, merge);
    }
  }
  return { mergeOf };
}

// firstParentCommits drops each conventional commit a merge brought in when
// that merge is itself a conventional commit of the same list, so a pull
// request counts once, as its merge. The dropped commit's breaking change and
// issue references move to the merge, so the version bump and the closed
// issues stay. A commit whose merge is not in the list is kept: a merge with
// a subject release-please cannot parse would otherwise drop its change.
export function firstParentCommits(commits, history) {
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
    foldBreaking(record, commit);
    foldReferences(record, commit);
  }
  return kept;
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

function foldReferences(record, dropped) {
  const key = ref => [ref.action, ref.owner, ref.repository, ref.prefix, ref.issue].join('|');
  const seen = new Set(record.references.map(key));
  for (const ref of dropped.references) {
    if (seen.has(key(ref))) continue;
    seen.add(key(ref));
    record.references.push({ ...ref });
  }
}
