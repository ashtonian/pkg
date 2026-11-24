import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';

const PUBLIC_REGISTRY = 'https://registry.npmjs.org/';
const PACKAGE = '@ashtonian/parse';
const REGISTRY_SPEC = /^(@ashtonian\/parse)@(\d+\.\d+\.\d+)$/;
const USAGE = 'usage: node tools/check-parse-sdk-registry.mjs needs-publish|wait pack.json\n'
  + '       node tools/check-parse-sdk-registry.mjs install <scratch dir> <tarball.tgz | @ashtonian/parse@X.Y.Z>';

// How long the readiness wait gives npm to process a publication, and how long a
// consumer retries a version the registry has not shown it yet.
export const WAIT_SECONDS = { name: 'PARSE_SDK_REGISTRY_WAIT_SECONDS', fallback: 900 };
export const RETRY_SECONDS = { name: 'PARSE_SDK_REGISTRY_RETRY_SECONDS', fallback: 300 };
const FIRST_RETRY_MS = 5_000;
const MAX_RETRY_MS = 30_000;
// pnpm's fetch timeout and retry end a slow request; this ends a pnpm that hangs.
const ATTEMPT_TIMEOUT_MS = 5 * 60_000;
const OUTPUT_TAIL = 64 * 1024;

class NotInstallableError extends Error {}

export function boundMs(env, { name, fallback }) {
  const value = env[name] ?? '';
  if (value === '') return fallback * 1000;
  assert.match(value, /^\d+$/, `${name} must be a whole number of seconds, not ${JSON.stringify(value)}`);
  return Number(value) * 1000;
}

// npm's version document: the registry's own record of one version's bytes.
export async function registryHasPackage(packed, { registry = PUBLIC_REGISTRY, fetchImpl = fetch } = {}) {
  const url = new URL(`${encodeURIComponent(packed.name)}/${encodeURIComponent(packed.version)}`, registry).href;
  const response = await fetchImpl(url, {
    headers: { Accept: 'application/json' },
    signal: AbortSignal.timeout(30_000),
  });
  if (response.status === 404) {
    await response.body?.cancel();
    return false;
  }
  assert.ok(response.ok, `registry returned HTTP ${response.status} for ${url}`);
  const published = await response.json();
  assert.equal(published.name, packed.name, 'registry package name differs');
  assert.equal(published.version, packed.version, 'registry package version differs');
  assert.equal(published.dist?.integrity, packed.integrity, 'published version has different bytes');
  return true;
}

// Runs pnpm, copying its output to log and keeping the end of it to classify a failure.
export function pnpmRunner({ log = process.stderr } = {}) {
  return (args, { cwd }) => new Promise((resolve, reject) => {
    const child = spawn('pnpm', args, { cwd, stdio: ['ignore', 'pipe', 'pipe'], timeout: ATTEMPT_TIMEOUT_MS });
    let output = '';
    for (const stream of [child.stdout, child.stderr]) {
      stream.setEncoding('utf8');
      stream.on('data', chunk => {
        log.write(chunk);
        output = (output + chunk).slice(-OUTPUT_TAIL);
      });
    }
    child.on('error', reject);
    child.on('close', (code, signal) => resolve({ code, signal, output }));
  });
}

// pnpm's report that the registry view it read lacks the version: package metadata
// without it (npm's ETARGET), or the metadata or tarball not found (npm's E404).
function notYetPublished(output) {
  return /\bERR_PNPM_NO_MATCHING_VERSION\b/.test(output)
    || (/\bERR_PNPM_\w+/.test(output) && /\(404 Not Found\)|\bHTTP 404\b/.test(output));
}

// A fresh consumer's install: its own project, store and metadata cache, so the
// registry serves every byte. A release age of zero admits the version however
// recently it was published.
async function installAttempt(spec, dir, { registry, run }) {
  const consumer = path.join(dir, 'consumer');
  fs.mkdirSync(consumer, { recursive: true });
  fs.writeFileSync(path.join(consumer, 'package.json'), '{"private":true,"type":"module"}\n');
  const { code, signal, output } = await run([
    'add', '--ignore-scripts', '--store-dir', path.join(dir, 'pnpm-store'),
    `--config.cache-dir=${path.join(dir, 'pnpm-cache')}`, `--registry=${registry}`,
    '--config.fetch-timeout=30000', '--config.fetch-retries=1', '--config.minimum-release-age=0', spec,
  ], { cwd: consumer });
  if (code === 0) return { consumer };
  const pnpmError = output.match(/\bERR_PNPM_\w+/)?.[0];
  const failure = `pnpm add ${signal ? `stopped by ${signal}` : `exited with status ${code}`}`
    + (pnpmError ? ` (${pnpmError})` : '');
  if (notYetPublished(output)) return { pending: failure };
  throw new Error(`installing ${spec} from ${registry}: ${failure}`);
}

function lockedIntegrity(consumer, { name, version }) {
  const lockfile = path.join(consumer, 'pnpm-lock.yaml');
  const key = `${name}@${version}`.replaceAll('.', '\\.');
  const locked = fs.readFileSync(lockfile, 'utf8').match(new RegExp(
    `^  '${key}':\\n    resolution: \\{integrity: (sha512-[A-Za-z0-9+/]+={0,2})\\}$`, 'm'));
  assert.ok(locked, `${lockfile} locks no integrity for ${name}@${version}`);
  return locked[1];
}

// Installs name@version as a fresh consumer, in a new directory under scratch each
// attempt, until pnpm's own resolution finds it and npm's version document names
// the bytes pnpm locked. A version not yet visible is retried with backoff; no
// attempt starts after boundMs, and any other failure ends the wait at once.
export async function installFromRegistry({ name, version }, {
  scratch, boundMs, registry = PUBLIC_REGISTRY, run = pnpmRunner(), fetchImpl = fetch,
  now = Date.now, sleep = delay, onPending = console.error,
}) {
  const spec = `${name}@${version}`;
  const deadline = now() + boundMs;
  let retryMs = FIRST_RETRY_MS;
  for (let attempt = 1; ; attempt++) {
    const dir = path.join(scratch, `attempt-${attempt}`);
    const installed = await installAttempt(spec, dir, { registry, run });
    let pending = installed.pending;
    if (!pending) {
      const integrity = lockedIntegrity(installed.consumer, { name, version });
      // pnpm checked the download against the integrity it locked. npm's own record
      // of the version must name those bytes, whatever registry configuration served them.
      if (await registryHasPackage({ name, version, integrity }, { registry, fetchImpl })) {
        return { consumer: installed.consumer, integrity };
      }
      pending = 'version document not found';
    }
    fs.rmSync(dir, { recursive: true, force: true });
    const remainingMs = deadline - now();
    if (remainingMs <= 0) {
      throw new NotInstallableError(`${spec}: not installable from ${registry} within `
        + `${boundMs / 1000} s (${attempt} attempts; last: ${pending})`);
    }
    const waitMs = Math.min(retryMs, remainingMs);
    onPending(`${spec}: not installable from ${registry} yet (${pending}); retrying in ${Math.ceil(waitMs / 1000)} s`);
    await sleep(waitMs);
    retryMs = Math.min(retryMs * 2, MAX_RETRY_MS);
  }
}

// Readiness: the consumer's own installation sees the version, and it is the
// retained package.
export async function waitForPackage(packed, options) {
  let integrity;
  try {
    ({ integrity } = await installFromRegistry(packed, options));
  } catch (error) {
    if (error instanceof NotInstallableError) {
      throw new Error(`${error.message}; inspect npm processing/review status before retrying acceptance`,
        { cause: error });
    }
    throw error;
  }
  assert.equal(integrity, packed.integrity, `${packed.name}@${packed.version}: published version has different bytes`);
}

function readPacked(file) {
  // `pnpm pack --json` output for the one package.
  const packed = JSON.parse(fs.readFileSync(file, 'utf8'));
  assert.equal(packed?.name, PACKAGE);
  assert.match(packed.version, /^\d+\.\d+\.\d+$/);
  assert.match(packed.integrity, /^sha512-[A-Za-z0-9+/]+={0,2}$/);
  return packed;
}

async function main([command, ...args]) {
  if (command === 'needs-publish' && args.length === 1) {
    console.log(`publish=${!await registryHasPackage(readPacked(args[0]))}`);
  } else if (command === 'wait' && args.length === 1) {
    const packed = readPacked(args[0]);
    const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'parse-sdk-wait-'));
    try {
      await waitForPackage(packed, { scratch, boundMs: boundMs(process.env, WAIT_SECONDS) });
    } finally {
      fs.rmSync(scratch, { recursive: true, force: true });
    }
    console.log(`${packed.name}@${packed.version}: a fresh consumer installs the retained package`);
  } else if (command === 'install' && args.length === 2) {
    // stdout carries the consumer's directory alone; pnpm's output goes to stderr.
    const [scratch, spec] = args;
    const registrySpec = spec.match(REGISTRY_SPEC);
    if (registrySpec) {
      const { consumer } = await installFromRegistry({ name: registrySpec[1], version: registrySpec[2] },
        { scratch, boundMs: boundMs(process.env, RETRY_SECONDS) });
      console.log(consumer);
    } else {
      assert.ok(spec.endsWith('.tgz') && fs.existsSync(spec), USAGE);
      const installed = await installAttempt(spec, scratch, { registry: PUBLIC_REGISTRY, run: pnpmRunner() });
      assert.ok(!installed.pending, `installing ${spec}: ${installed.pending}`);
      console.log(installed.consumer);
    }
  } else {
    throw new Error(USAGE);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await main(process.argv.slice(2));
  } catch (error) {
    console.error(error);
    process.exitCode = 1;
  }
}
