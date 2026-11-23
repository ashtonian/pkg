import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const CHECK = fileURLToPath(new URL('./check-pnpm.mjs', import.meta.url));
const LOCK = "lockfileVersion: '9.0'\n\nimporters:\n\n  .: {}\n";
const LEADING = "---\nlockfileVersion: '9.0'\n\nimporters:\n\n  .:\n    packageManagerDependencies: {}\n\n---\n" + LOCK;

// A repository of two pnpm projects, a and b/c, each written from files, and a
// pnpm on PATH that reports version.
function run(t, { files = {}, version = '12.23.0' } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'check-pnpm-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  const repo = path.join(dir, 'repo');
  const project = { 'package.json': '{"packageManager": "pnpm@12.23.0"}', 'pnpm-workspace.yaml': 'pmOnFail: ignore\n', 'pnpm-lock.yaml': LOCK };
  const tree = {};
  for (const name of ['a', 'b/c']) {
    for (const [file, content] of Object.entries(project)) tree[`${name}/${file}`] = content;
  }
  Object.assign(tree, files);
  for (const [file, content] of Object.entries(tree)) {
    fs.mkdirSync(path.dirname(path.join(repo, file)), { recursive: true });
    fs.writeFileSync(path.join(repo, file), content);
  }
  const git = (...args) => execFileSync('git', ['-C', repo, ...args], { env: { ...process.env, GIT_CONFIG_GLOBAL: '/dev/null', GIT_CONFIG_SYSTEM: '/dev/null' } });
  git('init', '-q');
  git('add', '.');
  const bin = path.join(dir, 'bin');
  fs.mkdirSync(bin);
  fs.writeFileSync(path.join(bin, 'pnpm'), `#!/bin/sh\necho ${version}\n`, { mode: 0o755 });
  const env = { ...process.env, PATH: `${bin}${path.delimiter}${process.env.PATH}` };
  return spawnSync(process.execPath, [CHECK, '--root', repo], { encoding: 'utf8', env });
}

test('the pnpm contract holds every pnpm project to one release and a one-document lockfile', t => {
  const cases = [
    { name: 'kept', want: [/^pnpm contract: pnpm 12\.23\.0, pmOnFail: ignore and a one-document pnpm-lock\.yaml in a, b\/c$/m] },
    { name: 'no pmOnFail', files: { 'b/c/pnpm-workspace.yaml': '# nothing\n' }, want: [/b\/c\/pnpm-workspace\.yaml must set pmOnFail: ignore/] },
    { name: 'a leading document', files: { 'a/pnpm-lock.yaml': LEADING }, want: [/a\/pnpm-lock\.yaml has YAML document markers on line\(s\) 1, 9/] },
    {
      name: 'different pins',
      files: { 'a/package.json': '{"packageManager": "pnpm@12.18.0"}' },
      want: [/pin different pnpm releases \(a 12\.18\.0, b\/c 12\.23\.0\)/],
    },
    { name: 'a range for a pin', files: { 'a/package.json': '{"packageManager": "pnpm@^12"}' }, want: [/a\/package\.json: packageManager must pin an exact pnpm release/] },
    {
      name: 'another pnpm on PATH',
      version: '12.18.0',
      want: [/pnpm 12\.18\.0 is on PATH \(\S+\/bin\/pnpm\), but the repository pins pnpm 12\.23\.0; install it into the active Node\.js: npm install --global pnpm@12\.23\.0$/m],
    },
  ];
  for (const tc of cases) {
    const result = run(t, tc);
    assert.equal(result.status, tc.name === 'kept' ? 0 : 1, `${tc.name}: ${result.stderr}`);
    for (const pattern of tc.want) assert.match(result.stdout + result.stderr, pattern, tc.name);
  }
});
