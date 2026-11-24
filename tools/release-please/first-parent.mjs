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
// brought in mapped to that merge; messageOf, those merges' messages;
// branchCommits, the message and changed files of each such commit that is
// not itself a merge; revertedWithin, the commits a revert on the same
// merge's branch undid, the reverts included; positionOf, each chain commit's
// place on the chain, oldest 0; and, given the release-please config's path,
// startOf: each release path mapped to the first chain commit whose config
// names it.
export function readHistory(repoDir, ref, { configFile } = {}) {
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
  chain.reverse();
  const onChain = new Set(chain);
  const positionOf = new Map(chain.map((sha, position) => [sha, position]));
  const mergeOf = new Map();
  // Oldest first: an older chain commit reaches only chain commits and ones
  // already mapped, so each walk claims exactly what its merge brought in.
  for (const merge of chain) {
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
  // --no-renames lists a rename's old path too, so a file moved out of a
  // release path counts as touching it, whatever the reader's diff config.
  const filesOf = new Map();
  for (const record of git('log', '-z', '--no-merges', '--no-renames', '--name-only', '--format=%x01%H', ref).split('\x01')) {
    const [sha, ...files] = record.replace(/^\n/, '').split('\0');
    if (sha) filesOf.set(sha.trim(), files.map(file => file.replace(/^\n/, '')).filter(Boolean));
  }
  const branchCommits = new Map();
  for (const sha of mergeOf.keys()) {
    if (parents.get(sha).length === 1) branchCommits.set(sha, { message: messages.get(sha), files: filesOf.get(sha) ?? [] });
  }
  const brought = new Map([...mergeOf.keys()].map(sha => [sha, messages.get(sha)]));
  const revertedWithin = revertsWithin(brought, sha => mergeOf.get(sha));
  const startOf = configFile === undefined ? new Map() : releasePathStarts(repoDir, chain, configFile);
  return { mergeOf, messageOf, branchCommits, revertedWithin, positionOf, startOf };
}

// releasePathStarts maps each package of the release-please config to the
// first commit of chain (oldest first) whose config names it: where it became
// a release path. Two cat-file processes read every chain commit's config
// blob and then each distinct version once.
function releasePathStarts(repoDir, chain, configFile) {
  const catFile = (args, input) => execFileSync('git', ['-C', repoDir, 'cat-file', ...args], { input, maxBuffer: 256 << 20 });
  const isObject = line => /^[0-9a-f]+$/.test(line);
  const blobs = catFile(['--batch-check=%(objectname)'], chain.map(sha => `${sha}:${configFile}\n`).join(''))
    .toString('utf8').split('\n').slice(0, chain.length);
  const versions = [...new Set(blobs.filter(isObject))];
  const out = catFile(['--batch'], versions.map(oid => `${oid}\n`).join(''));
  const textOf = new Map();
  let at = 0;
  for (const oid of versions) {
    // Each object is "<oid> <type> <size>\n<content>\n".
    const eol = out.indexOf(0x0a, at);
    const size = Number(out.toString('utf8', at, eol).split(' ')[2]);
    textOf.set(oid, out.toString('utf8', eol + 1, eol + 1 + size));
    at = eol + 1 + size + 1;
  }
  const startOf = new Map();
  let previous;
  for (const [position, oid] of blobs.entries()) {
    if (!isObject(oid) || oid === previous) continue;
    previous = oid;
    let config;
    try {
      config = JSON.parse(textOf.get(oid));
    } catch (error) {
      throw new Error(`reading ${configFile} at ${chain[position]}: ${error.message}`);
    }
    for (const path of Object.keys(config.packages ?? {})) {
      if (!startOf.has(path)) startOf.set(path, chain[position]);
    }
  }
  return startOf;
}

// revertsWithin returns the commits of messageOf (sha to message) that a
// revert among them undid, with those reverts, pairing them by git's
// "This reverts commit <sha>" line; with groupOf, only within one group.
export function revertsWithin(messageOf, groupOf = () => undefined) {
  const within = new Set();
  for (const [sha, message] of messageOf) {
    for (const [, reverted] of message.matchAll(/^This reverts commit ([0-9a-f]{40})/gm)) {
      if (messageOf.has(reverted) && groupOf(reverted) === groupOf(sha)) within.add(sha).add(reverted);
    }
  }
  return within;
}

// commitOverride returns what release-please reads in place of each commit of
// a pull request whose description is body: its BEGIN_COMMIT_OVERRIDE block,
// trimmed, or '' without one.
export function commitOverride(body) {
  return ((body ?? '').split('BEGIN_COMMIT_OVERRIDE')[1] ?? '').split('END_COMMIT_OVERRIDE')[0].trim();
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
// folds into that header what they call for. commits are one release path's.
// headerOf maps a merge to the header release-please's parse of its message
// gives its first line.
//
// A merge's subject names its whole pull request, whose release paths may
// have changed in different ways. So in a path where complete holds the
// merge (every branch commit touching the path reached it as a conventional
// commit), the merge counts as the highest type among its branch commits
// there, and breaks only if one of them does. That holds for the type when
// some branch commit of the merge, in any path, ranks as high as the
// subject's type, and for breaking when one breaks; branches maps each merge
// to its branch commits' types, whether one breaks, and those release-please
// cannot read whole, which warn names. Otherwise the subject's type and "!"
// are a floor the branch commits can only raise.
export function firstParentCommits(commits, history, { headerOf, rank = typeRanker(), complete = new Set(), branches = new Map(), warn = () => {} }) {
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
  // Each header's branch commits in this path, reverted ones aside.
  const brought = new Map();
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
    if (!brought.has(header)) brought.set(header, []);
    brought.get(header).push(commit);
  }
  for (const [header, branch] of brought) {
    const subject = header.message;
    const subjectRank = rank(header.type);
    const top = branch.reduce((best, commit) => (rank(commit.type) > rank(best.type) ? commit : best));
    const whole = complete.has(header.sha) ? branches.get(header.sha) : undefined;
    const typeHere = whole !== undefined && Math.max(0, ...whole.types.map(rank)) >= subjectRank;
    // An equal rank keeps the subject's type, and with it its changelog section.
    if (rank(top.type) > subjectRank || (typeHere && rank(top.type) < subjectRank)) header.type = top.type;
    if (rank(header.type) > subjectRank) {
      warn(`${header.sha.slice(0, 7)} (${subject}) counts as ${header.type}, from ${top.sha.slice(0, 7)} (${top.message}); its subject's type is lower than its change's`);
    }
    if (whole !== undefined && (whole.breaking || !header.breaking)) {
      header.breaking = false;
      header.notes = header.notes.filter(note => !isBreaking(note));
    }
    for (const commit of branch) {
      foldBreaking(header, commit);
      foldReleaseAs(header, commit);
      foldReferences(header, commit);
    }
    // The changelog writer reads a "!" in an entry's header as a breaking
    // change when the entry has no breaking note.
    if (!header.breaking) header.message = header.message.replace(BANG, '$1:');
  }
  warnUnreadable(commits, history, branches, warn);
  return kept;
}

// warnUnreadable names each merge here missing what release-please cannot
// read of one of its branch commits (a branch summary's unreadable), unless
// the merge's own pull request sets an override, which release-please reads
// in place of each of its commits.
function warnUnreadable(commits, history, branches, warn) {
  const overridden = new Map();
  for (const commit of commits) {
    const merge = history.mergeOf.get(commit.sha) ?? commit.sha;
    overridden.set(merge, overridden.get(merge) || ownOverride(commit, merge));
  }
  for (const [merge, isOverridden] of overridden) {
    if (isOverridden) continue;
    const subject = history.messageOf.get(merge)?.split('\n')[0];
    for (const branch of branches.get(merge)?.unreadable ?? []) {
      for (const problem of branch.problems) {
        warn(`${merge.slice(0, 7)} (${subject}) is missing what release-please cannot read of its branch commit ${branch.sha.slice(0, 7)} (${branch.subject}): ${problem}`);
      }
    }
  }
}

// The changelog writer's breakingHeaderPattern, up to the "!".
const BANG = /^(\w*(?:\(.*\))?)!:(?= )/;

// ownOverride reports a commit release-please gave the override of the pull
// request merged as merge, read as release-please reads an override.
function ownOverride(commit, merge) {
  const pr = commit.pullRequest;
  if (pr === undefined || pr.mergeCommitOid !== merge) return false;
  return commitOverride(pr.body) !== '';
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
