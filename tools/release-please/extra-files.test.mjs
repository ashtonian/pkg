import assert from 'node:assert/strict';
import fs from 'node:fs';
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
