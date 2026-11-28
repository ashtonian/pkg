import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import { posix } from 'node:path';
import test from 'node:test';
import { Generic } from 'release-please/build/src/updaters/generic.js';
import { Version } from 'release-please/build/src/version.js';

const root = new URL('../../', import.meta.url);
const read = path => fs.readFileSync(new URL(path, root), 'utf8');
const config = JSON.parse(read('release-please-config.json'));
const manifest = JSON.parse(read('.release-please-manifest.json'));

const generic = Object.entries(config.packages).flatMap(([dir, pkg]) =>
  (pkg['extra-files'] ?? []).filter(file => file.type === 'generic').map(file => ({ dir, path: `${dir}/${file.path}` })));

// pinned is what a line marked for scope pins of version: the whole version,
// or one of its numbers standing alone (x-release-please-major, -minor,
// -patch, as Go constants hold a release).
const pinned = (line, version) => {
  const scope = line.match(/x-release-please-(major|minor|patch)\b/)?.[1];
  if (scope === undefined) return { text: version.toString(), at: line.indexOf(version.toString()) };
  const number = String(version[scope]);
  const at = line.search(new RegExp(`\\b${number}\\b`));
  return { text: number, at };
};

// A release rewrites each generic extra file with release-please's own
// updater: every version it pins moves to the release, and nothing else in the
// file changes. A pin outside its markers would stay behind; a marker around
// unrelated text would rewrite it.
for (const { dir, path } of generic) {
  test(`${path} pins ${dir}'s released version and a release moves only those pins`, () => {
    const current = Version.parse(manifest[dir]);
    const before = read(path);
    const next = new Version(99, 98, 97);
    const after = new Generic({ version: next, versionsMap: new Map() }).updateContent(before);

    const was = before.split('\n');
    const now = after.split('\n');
    assert.equal(now.length, was.length);
    const changed = was.flatMap((line, i) => (line === now[i] ? [] : [[line, now[i]]]));
    assert.ok(changed.length > 0, `${path} has no x-release-please markers around a version`);
    for (const [line, updated] of changed) {
      const pin = pinned(line, current);
      assert.ok(pin.at >= 0, `${path}: "${line}" pins no ${pin.text} of ${current}`);
      const moved = pinned(line, next).text;
      assert.equal(updated, line.slice(0, pin.at) + moved + line.slice(pin.at + pin.text.length));
    }
    const missed = now.filter(line => line.includes(`@v${current}`));
    assert.deepEqual(missed, [], `${path}: pins outside the markers`);
  });
}

// A linked-versions group releases at one version, which its members' pins
// (the provider modules' requirement of parse, their release guards) hold:
// the manifest gives every member the same one.
for (const plugin of (config.plugins ?? []).filter(p => p.type === 'linked-versions')) {
  test(`the ${plugin.groupName} group's members are at one version`, () => {
    const pathOf = component => Object.keys(config.packages).find(dir => (config.packages[dir].component ?? dir) === component);
    const versions = plugin.components.map(component => [component, manifest[pathOf(component)]]);
    assert.ok(versions.every(([, version]) => version !== undefined), JSON.stringify(versions));
    assert.equal(new Set(versions.map(([, version]) => version)).size, 1, JSON.stringify(versions));
  });
}

// A module in this repository that replaces others with their working trees
// builds against what those trees require, so `go mod tidy` raises its own
// requirement to the highest of them. A release moves the replaced modules'
// pinned requirements; the replacing module's must move with them, through
// markers and an extra-files entry of its own, or tidy fails on the release.
const released = new Map(
  generic.map(({ path }) => [path, new Generic({ version: new Version(99, 98, 97), versionsMap: new Map() }).updateContent(read(path))]));
const afterRelease = path => released.get(path) ?? read(path);

const goMod = text => {
  let module = '';
  const requires = new Map();
  const replaces = new Map();
  let block = null;
  for (const raw of text.split('\n')) {
    const line = raw.replace(/\/\/.*$/, '').trim();
    if (line === '') continue;
    if (block !== null && line === ')') {
      block = null;
      continue;
    }
    let directive = block;
    let rest = line;
    if (block === null) {
      const head = line.match(/^(\w+)\s*(.*)$/);
      if (head === null) continue;
      [, directive, rest] = head;
      if (rest === '(') {
        block = directive;
        continue;
      }
    }
    if (directive === 'module') module = rest;
    if (directive === 'require') {
      const [module, version] = rest.split(/\s+/);
      requires.set(module, version);
    }
    if (directive === 'replace') {
      const local = rest.match(/^(\S+)(?:\s+\S+)?\s+=>\s+(\.{1,2}\/\S*|\.{1,2})$/);
      if (local !== null) replaces.set(local[1], local[2]);
    }
  }
  return { module, requires, replaces };
};

// compareVersions orders vMAJOR.MINOR.PATCH, a pre-release or pseudo-version
// below its release.
const compareVersions = (a, b) => {
  const parts = v => v.replace(/^v/, '').split(/[-+]/)[0].split('.').map(Number);
  const [pa, pb] = [parts(a), parts(b)];
  for (let i = 0; i < 3; i++) if (pa[i] !== pb[i]) return pa[i] - pb[i];
  return (b.includes('-') ? 1 : 0) - (a.includes('-') ? 1 : 0);
};

const goModPaths = execFileSync('git', ['ls-files', '--', 'go.mod', '**/go.mod'], { cwd: root, encoding: 'utf8' })
  .split('\n').filter(path => path !== '');
const inRepo = new Set(goModPaths.map(path => goMod(read(path)).module));

for (const path of goModPaths.filter(path => goMod(read(path)).replaces.size > 0)) {
  test(`${path} requires what the working trees it replaces require, after a release too`, () => {
    for (const [label, readMod] of [['now', read], ['after a release', afterRelease]]) {
      const mod = goMod(readMod(path));
      for (const [module, local] of mod.replaces) {
        const target = posix.join(posix.dirname(path), local, 'go.mod');
        for (const [required, version] of goMod(readMod(target)).requires) {
          if (!inRepo.has(required)) continue;
          const own = mod.requires.get(required);
          assert.ok(own !== undefined && compareVersions(own, version) >= 0,
            `${path} (${label}) requires ${required} ${own ?? '(nothing)'}, below the ${version} that ${target} requires, the working tree it replaces ${module} with: ` +
            'mark the requirement x-release-please-version and list the file in that module\'s extra-files');
        }
      }
    }
  });
}
