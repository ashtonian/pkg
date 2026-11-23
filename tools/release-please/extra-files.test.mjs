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

// A release rewrites each generic extra file with release-please's own
// updater: every version it pins moves to the release, and nothing else in the
// file changes. A pin outside its markers would stay behind; a marker around
// unrelated text would rewrite it.
for (const { dir, path } of generic) {
  test(`${path} pins ${dir}'s released version and a release moves only those pins`, () => {
    const current = manifest[dir];
    const before = read(path);
    const next = new Version(99, 98, 97);
    const after = new Generic({ version: next, versionsMap: new Map() }).updateContent(before);

    const was = before.split('\n');
    const now = after.split('\n');
    assert.equal(now.length, was.length);
    const changed = was.flatMap((line, i) => (line === now[i] ? [] : [[line, now[i]]]));
    assert.ok(changed.length > 0, `${path} has no x-release-please markers around a version`);
    for (const [line, updated] of changed) {
      assert.ok(line.includes(current), `${path}: "${line}" pins no ${current}`);
      assert.equal(updated, line.replace(current, next.toString()));
    }
    const missed = now.filter(line => line.includes(`@v${current}`));
    assert.deepEqual(missed, [], `${path}: pins outside the markers`);
  });
}
