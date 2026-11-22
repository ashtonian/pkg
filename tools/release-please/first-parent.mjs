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

// readHistory reads ref's history. mergeOf maps every commit a merge on ref's
// first-parent chain brought in to that merge; a commit on the chain itself,
// or one newer than ref, has no entry. subjectOf holds each of those merges'
// subject: the first line of its message, the header release-please parses.
export function readHistory(repoDir, ref) {
  const git = gitIn(repoDir);
  const parents = new Map();
  for (const line of git('rev-list', '--parents', ref).split('\n')) {
    if (!line) continue;
    const [sha, ...shas] = line.split(' ');
    parents.set(sha, shas);
  }
  const subjectOf = new Map();
  for (const record of git('rev-list', '--first-parent', '--merges', '--no-commit-header', '--format=%x00%H%x1f%B', ref).split('\0')) {
    if (!record) continue;
    const [sha, message] = record.split('\x1f');
    subjectOf.set(sha, message.split('\n')[0].trim());
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
  return { mergeOf, subjectOf };
}

// typeRanker orders commit types by what they call for in a release: a
// feature, then any other type the changelog shows, then hidden types, which
// alone cut no release. sections are changelog-sections; undefined means
// release-please's defaults.
export function typeRanker(sections = DEFAULT_SECTIONS) {
  const shown = new Set(sections.filter(section => !section.hidden).map(section => section.type));
  return type => {
    if (type === 'feat' || type === 'feature') return 2;
    if (shown.has(type)) return 1;
    return 0;
  };
}

// firstParentCommits drops each conventional commit a merge brought in when
// the merge has a header: headerOf maps each merge whose subject
// release-please parses to the header its parser makes of it. So a pull
// request counts once, as its merge:
//
//   - In a release path the merge's changes reach, the merge's entry with that
//     header takes over what the dropped commit calls for: a higher-ranked
//     type, so a fix merged as refactor(…) still cuts a release; its breaking
//     change and Release-As; its issue references.
//   - In a path they don't reach, the branch's change there did not land, so
//     nothing takes it over.
//
// A commit release-please gave a pull request's BEGIN_COMMIT_OVERRIDE message
// is not its own, so it is dropped without taking anything over; the merge's
// copy of the override stands as the author wrote it.
//
// A commit whose merge has no header is kept, so its change is listed rather
// than lost; so is one whose merge is listed here without that header.
export function firstParentCommits(commits, history, { headerOf, rank = typeRanker(), warn = () => {} }) {
  const listed = new Set(commits.map(commit => commit.sha));
  const headers = new Map();
  for (const commit of commits) {
    if (!headers.has(commit.sha) && headerOf.get(commit.sha) === commit.message) headers.set(commit.sha, commit);
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
    if (hasOverride(commit)) continue;
    if (!headerOf.has(merge)) {
      kept.push(commit);
      continue;
    }
    const header = headers.get(merge);
    if (header === undefined) {
      if (!listed.has(merge)) continue;
      if (!unmatched.has(merge)) {
        unmatched.add(merge);
        warn(`${merge.slice(0, 7)} (${headerOf.get(merge)}) has no entry with its header; its branch commits stay listed`);
      }
      kept.push(commit);
      continue;
    }
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

// hasOverride reports a commit whose pull request's body sets
// BEGIN_COMMIT_OVERRIDE, read as release-please reads it: release-please
// gave the commit that message in place of its own.
function hasOverride(commit) {
  const override = (commit.pullRequest?.body ?? '').split('BEGIN_COMMIT_OVERRIDE')[1] ?? '';
  return override.split('END_COMMIT_OVERRIDE')[0].trim() !== '';
}

const isBreaking = note => note.title === BREAKING;

// foldBreaking marks header breaking when dropped was. A description in
// dropped's BREAKING CHANGE footer replaces the note header's own "!" gave it,
// which only repeats its subject.
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

// foldReferences adds dropped's issue references; one issue is one reference,
// whether a footer says Closes or Fixes.
function foldReferences(header, dropped) {
  const key = ref => [ref.owner, ref.repository, ref.prefix, ref.issue].join('|');
  const seen = new Set(header.references.map(key));
  for (const ref of dropped.references) {
    if (seen.has(key(ref))) continue;
    seen.add(key(ref));
    header.references.push({ ...ref });
  }
}
