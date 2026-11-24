import assert from 'node:assert/strict';
import { execFileSync, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import test, { after } from 'node:test';
import { fileURLToPath } from 'node:url';
import {
  boundMs, installFromRegistry, pnpmRunner, registryHasPackage, RETRY_SECONDS, WAIT_SECONDS, waitForPackage,
} from './check-parse-sdk-registry.mjs';

const CHECK = fileURLToPath(new URL('./check-parse-sdk-registry.mjs', import.meta.url));
const packed = { name: '@ashtonian/parse', version: '0.10.1', integrity: 'sha512-test' };
const version = { name: packed.name, version: packed.version, dist: { integrity: packed.integrity } };
const NO_MATCHING_VERSION = 'Error: ERR_PNPM_NO_MATCHING_VERSION\n\n  × adding a new package\n'
  + '  ╰─▶ Failed to resolve dependency tree: No matching version found for @ashtonian/parse@0.10.1\n';
const quiet = { write() {} };

function scratch(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'parse-sdk-registry-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
}

// A clock that only sleeps move.
function fakeClock() {
  let at = 0;
  const sleeps = [];
  return { now: () => at, sleep: async ms => { sleeps.push(ms); at += ms; }, sleeps };
}

function responses(sequence) {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({ url, ...options });
    assert.ok(sequence.length, 'unexpected registry request');
    return sequence.shift();
  };
  return { fetchImpl, calls };
}

// pnpm, answering each attempt with the next outcome: a failure's exit status and
// output, or a lockfile naming integrity.
function scriptedPnpm(outcomes) {
  const calls = [];
  const run = async (args, { cwd }) => {
    calls.push({ args, cwd });
    assert.ok(outcomes.length, 'unexpected pnpm run');
    const outcome = outcomes.shift();
    if (outcome.integrity) {
      fs.writeFileSync(path.join(cwd, 'pnpm-lock.yaml'), `lockfileVersion: '9.0'\n\npackages:\n\n`
        + `  '${packed.name}@${packed.version}':\n    resolution: {integrity: ${outcome.integrity}}\n`);
      return { code: 0, signal: null, output: '' };
    }
    return { code: 1, signal: null, ...outcome };
  };
  return { run, calls };
}

test('the publish decision reads the version document', async () => {
  const { fetchImpl, calls } = responses([new Response(null, { status: 404 }), Response.json(version)]);
  assert.equal(await registryHasPackage(packed, { fetchImpl }), false);
  assert.equal(await registryHasPackage(packed, { fetchImpl }), true);
  assert.equal(calls[0].url, 'https://registry.npmjs.org/%40ashtonian%2Fparse/0.10.1');
  assert.equal(calls[0].headers.Accept, 'application/json');
  assert.ok(calls[0].signal instanceof AbortSignal);
});

test('the version document fails on other bytes, HTTP errors and malformed metadata', async () => {
  const cases = [
    { name: 'other bytes', response: Response.json({ ...version, dist: { integrity: 'sha512-other' } }), want: /different bytes/ },
    ...[401, 403, 429, 500].map(status => ({ name: `HTTP ${status}`, response: new Response(null, { status }), want: new RegExp(`HTTP ${status}`) })),
    { name: 'malformed', response: new Response('{invalid'), want: SyntaxError },
  ];
  for (const tc of cases) {
    const { fetchImpl } = responses([tc.response]);
    await assert.rejects(registryHasPackage(packed, { fetchImpl }), tc.want, tc.name);
  }
});

test('a version that appears late installs after backoff, each attempt from a fresh project', async t => {
  const { run, calls } = scriptedPnpm([
    { output: NO_MATCHING_VERSION }, { output: NO_MATCHING_VERSION }, { output: NO_MATCHING_VERSION },
    { integrity: packed.integrity },
  ]);
  const { fetchImpl } = responses([Response.json(version)]);
  const clock = fakeClock();
  const pending = [];
  const dir = scratch(t);
  const installed = await installFromRegistry(packed, {
    scratch: dir, boundMs: 300_000, run, fetchImpl, ...clock, onPending: message => pending.push(message),
  });
  assert.deepEqual(clock.sleeps, [5_000, 10_000, 20_000]);
  assert.equal(installed.integrity, packed.integrity);
  assert.equal(installed.consumer, calls.at(-1).cwd);
  assert.equal(new Set(calls.map(call => call.cwd)).size, calls.length);
  assert.deepEqual(fs.readdirSync(dir), ['attempt-4']);
  assert.match(pending[0], /^@ashtonian\/parse@0\.10\.1: not installable from https:\/\/registry\.npmjs\.org\/ yet \(pnpm add exited with status 1 \(ERR_PNPM_NO_MATCHING_VERSION\)\); retrying in 5 s$/);
  const args = calls[0].args;
  assert.equal(args.at(-1), '@ashtonian/parse@0.10.1');
  for (const flag of ['--ignore-scripts', '--registry=https://registry.npmjs.org/', '--config.minimum-release-age=0']) {
    assert.ok(args.includes(flag), flag);
  }
});

test('a version that never appears fails once the bound passes, starting no attempt after it', async t => {
  const { run, calls } = scriptedPnpm(Array.from({ length: 5 }, () => ({ output: NO_MATCHING_VERSION })));
  const clock = fakeClock();
  await assert.rejects(installFromRegistry(packed, {
    scratch: scratch(t), boundMs: 60_000, run, ...clock, onPending: () => {},
  }), /^Error: @ashtonian\/parse@0\.10\.1: not installable from https:\/\/registry\.npmjs\.org\/ within 60 s \(5 attempts; last: pnpm add exited with status 1 \(ERR_PNPM_NO_MATCHING_VERSION\)\)$/);
  assert.equal(calls.length, 5);
  assert.deepEqual(clock.sleeps, [5_000, 10_000, 20_000, 25_000]);
});

test('unrelated failures end the wait at once', async t => {
  const cases = [
    { name: 'forbidden', output: 'Error: ERR_PNPM_RESOLVING_NPM_RESOLVER_NETWORK_ERROR\n  ╰─▶ Failed to fetch metadata: HTTP status client error (403 Forbidden)\n' },
    { name: 'server error', output: 'Error: ERR_PNPM_RESOLVING_NPM_RESOLVER_NETWORK_ERROR\n  ╰─▶ Failed to fetch metadata: HTTP status server error (500 Internal Server Error)\n' },
    { name: 'no pnpm error code', output: 'ENOSPC: no space left on device\n' },
    { name: 'stopped', code: null, signal: 'SIGTERM', output: '' },
  ];
  for (const tc of cases) {
    const { run, calls } = scriptedPnpm([tc]);
    const clock = fakeClock();
    await assert.rejects(installFromRegistry(packed, { scratch: scratch(t), boundMs: 300_000, run, ...clock }),
      /^Error: installing @ashtonian\/parse@0\.10\.1 from https:\/\/registry\.npmjs\.org\/: pnpm add /, tc.name);
    assert.equal(calls.length, 1, tc.name);
    assert.deepEqual(clock.sleeps, [], tc.name);
  }
});

test('a version document lagging pnpm is retried; one naming other bytes is not', async t => {
  const lagging = scriptedPnpm([{ integrity: packed.integrity }, { integrity: packed.integrity }]);
  const lag = responses([new Response(null, { status: 404 }), Response.json(version)]);
  const clock = fakeClock();
  await installFromRegistry(packed, {
    scratch: scratch(t), boundMs: 300_000, run: lagging.run, fetchImpl: lag.fetchImpl, ...clock, onPending: () => {},
  });
  assert.deepEqual(clock.sleeps, [5_000]);

  const other = scriptedPnpm([{ integrity: 'sha512-other' }]);
  const { fetchImpl } = responses([Response.json(version)]);
  await assert.rejects(installFromRegistry(packed, { scratch: scratch(t), boundMs: 300_000, run: other.run, fetchImpl }),
    /different bytes/);
});

test('readiness requires the retained package and names npm processing on its deadline', async t => {
  const { run } = scriptedPnpm([{ integrity: 'sha512-other' }]);
  const { fetchImpl } = responses([Response.json({ ...version, dist: { integrity: 'sha512-other' } })]);
  await assert.rejects(waitForPackage(packed, { scratch: scratch(t), boundMs: 900_000, run, fetchImpl }),
    /0\.10\.1: published version has different bytes/);

  const never = scriptedPnpm([{ output: NO_MATCHING_VERSION }]);
  await assert.rejects(waitForPackage(packed, { scratch: scratch(t), boundMs: 0, run: never.run, ...fakeClock() }),
    /within 0 s \(1 attempts; .*\); inspect npm processing\/review status before retrying acceptance$/);
});

test('the bounds are whole seconds from the environment', () => {
  assert.equal(boundMs({}, WAIT_SECONDS), 900_000);
  assert.equal(boundMs({}, RETRY_SECONDS), 300_000);
  assert.equal(boundMs({ PARSE_SDK_REGISTRY_RETRY_SECONDS: '' }, RETRY_SECONDS), 300_000);
  assert.equal(boundMs({ PARSE_SDK_REGISTRY_RETRY_SECONDS: '0' }, RETRY_SECONDS), 0);
  assert.equal(boundMs({ PARSE_SDK_REGISTRY_WAIT_SECONDS: '1200' }, WAIT_SECONDS), 1_200_000);
  for (const value of ['-1', '1.5', '5m', ' 5']) {
    assert.throws(() => boundMs({ PARSE_SDK_REGISTRY_WAIT_SECONDS: value }, WAIT_SECONDS),
      /PARSE_SDK_REGISTRY_WAIT_SECONDS must be a whole number of seconds/, value);
  }
});

// The real pnpm against a registry on loopback: what it requests and how it fails
// are what the retries read.

let fixture;
after(() => fixture && fs.rmSync(fixture.dir, { recursive: true, force: true }));
function packageFixture() {
  if (fixture) return fixture;
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'parse-sdk-fixture-'));
  fs.mkdirSync(path.join(dir, 'package'));
  fs.writeFileSync(path.join(dir, 'package', 'package.json'), JSON.stringify({ name: packed.name, version: packed.version }));
  fs.writeFileSync(path.join(dir, 'package', 'index.js'), 'export {};\n');
  const pack = JSON.parse(execFileSync('pnpm', ['pack', '--json', '--pack-destination', dir], {
    cwd: path.join(dir, 'package'), encoding: 'utf8',
  }));
  fixture = { dir, tarball: fs.readFileSync(pack.filename), integrity: pack.integrity, file: pack.filename };
  return fixture;
}

// state.versions lists the versions the package metadata shows (none: the package is
// unknown); state.metadataStatus and state.tarballStatus override those answers.
async function fakeRegistry(t) {
  const { tarball, integrity } = packageFixture();
  const state = { versions: [], metadataStatus: 0, tarballStatus: 0 };
  const requests = [];
  const server = http.createServer((req, res) => {
    const pathname = decodeURIComponent(new URL(req.url, 'http://registry').pathname);
    requests.push({ pathname, accept: req.headers.accept });
    const manifest = v => ({ name: packed.name, version: v, dist: { integrity, tarball: `${registry}${packed.name}/-/parse-${v}.tgz` } });
    const reply = (status, body, type = 'application/json') => {
      res.writeHead(status, { 'content-type': type });
      res.end(body);
    };
    if (pathname === `/${packed.name}`) {
      if (state.metadataStatus) return reply(state.metadataStatus, '{}');
      if (!state.versions.length) return reply(404, '{"error":"Not found"}');
      const versions = Object.fromEntries(state.versions.map(v => [v, manifest(v)]));
      return reply(200, JSON.stringify({ name: packed.name, 'dist-tags': { latest: state.versions.at(-1) }, versions, modified: '2026-11-09T06:45:53.888Z' }),
        'application/vnd.npm.install-v1+json');
    }
    if (pathname === `/${packed.name}/-/parse-${packed.version}.tgz`) {
      if (state.tarballStatus) return reply(state.tarballStatus, '{}');
      return reply(200, tarball, 'application/octet-stream');
    }
    const document = pathname.match(/^\/@ashtonian\/parse\/(\d+\.\d+\.\d+)$/);
    if (document && state.versions.includes(document[1])) return reply(200, JSON.stringify(manifest(document[1])));
    return reply(404, '{"error":"Not found"}');
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => server.close());
  const registry = `http://127.0.0.1:${server.address().port}/`;
  return { registry, state, requests, integrity };
}

const pnpm = pnpmRunner({ log: quiet });

test('pnpm: a version the registry shows late, as npm does after publication, installs', async t => {
  const { registry, state, requests, integrity } = await fakeRegistry(t);
  state.versions = ['0.10.0'];
  const clock = fakeClock();
  const sleep = async ms => {
    await clock.sleep(ms);
    if (clock.sleeps.length === 2) state.versions = ['0.10.0', packed.version];
  };
  const installed = await installFromRegistry(packed, {
    scratch: scratch(t), boundMs: 300_000, registry, run: pnpm, now: clock.now, sleep, onPending: () => {},
  });
  assert.deepEqual(clock.sleeps, [5_000, 10_000]);
  assert.equal(installed.integrity, integrity);
  const manifest = JSON.parse(fs.readFileSync(path.join(installed.consumer, 'node_modules', packed.name, 'package.json'), 'utf8'));
  assert.equal(manifest.version, packed.version);
  // The consumer reads the abbreviated package metadata, which is what the wait must see.
  const metadata = requests.filter(r => r.pathname === `/${packed.name}`);
  assert.ok(metadata.length >= 3);
  for (const r of metadata) assert.match(r.accept, /^application\/vnd\.npm\.install-v1\+json/);
});

test('pnpm: an unknown package and a tarball not yet served are retried', async t => {
  const { registry, state } = await fakeRegistry(t);
  state.tarballStatus = 404;
  const clock = fakeClock();
  const sleep = async ms => {
    await clock.sleep(ms);
    if (clock.sleeps.length === 1) state.versions = [packed.version];
    if (clock.sleeps.length === 2) state.tarballStatus = 0;
  };
  const pending = [];
  await installFromRegistry(packed, {
    scratch: scratch(t), boundMs: 300_000, registry, run: pnpm, now: clock.now, sleep, onPending: m => pending.push(m),
  });
  assert.equal(pending.length, 2);
  assert.match(pending[0], /ERR_PNPM_\w+/);
  assert.match(pending[1], /ERR_PNPM_\w+/);
});

test('pnpm: a version the registry never shows fails within the bound', async t => {
  const { registry, state } = await fakeRegistry(t);
  state.versions = ['0.10.0'];
  const clock = fakeClock();
  await assert.rejects(installFromRegistry(packed, {
    scratch: scratch(t), boundMs: 15_000, registry, run: pnpm, ...clock, onPending: () => {},
  }), /within 15 s \(3 attempts; last: pnpm add exited with status 1 \(ERR_PNPM_NO_MATCHING_VERSION\)\)$/);
  assert.deepEqual(clock.sleeps, [5_000, 10_000]);
});

test('pnpm: a registry refusing the request fails at once', async t => {
  const { registry, state } = await fakeRegistry(t);
  state.metadataStatus = 403;
  const clock = fakeClock();
  await assert.rejects(installFromRegistry(packed, {
    scratch: scratch(t), boundMs: 300_000, registry, run: pnpm, ...clock,
  }), /^Error: installing @ashtonian\/parse@0\.10\.1 from http:\/\/127\.0\.0\.1:\d+\/: pnpm add exited with status 1 \(ERR_PNPM_\w+\)$/);
  assert.deepEqual(clock.sleeps, []);
});

test('install prints only the consumer directory on stdout', t => {
  const dir = scratch(t);
  const result = spawnSync(process.execPath, [CHECK, 'install', dir, packageFixture().file], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, `${path.join(dir, 'consumer')}\n`);
  assert.ok(fs.existsSync(path.join(dir, 'consumer', 'node_modules', packed.name, 'package.json')));
});
