import assert from 'node:assert/strict';
import fs from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';

export async function registryHasPackage(packed, {
  installMetadata = false,
  signal,
  fetchImpl = fetch,
} = {}) {
  const url = `https://registry.npmjs.org/${encodeURIComponent(packed.name)}`
    + (installMetadata ? '' : `/${encodeURIComponent(packed.version)}`);
  const requestDeadline = AbortSignal.timeout(30_000);
  const response = await fetchImpl(url, {
    headers: { Accept: installMetadata ? 'application/vnd.npm.install-v1+json' : 'application/json' },
    signal: signal ? AbortSignal.any([signal, requestDeadline]) : requestDeadline,
  });
  if (response.status === 404) {
    await response.body?.cancel();
    return false;
  }
  assert.ok(response.ok, `registry returned HTTP ${response.status} for ${url}`);
  let published = await response.json();
  if (installMetadata) {
    assert.ok(published.versions && typeof published.versions === 'object'
      && !Array.isArray(published.versions), 'registry install metadata has no version map');
    if (!Object.hasOwn(published.versions, packed.version)) return false;
    published = published.versions[packed.version];
  }
  assert.equal(published.name, packed.name, 'registry package name differs');
  assert.equal(published.version, packed.version, 'registry package version differs');
  assert.equal(published.dist?.integrity, packed.integrity, 'published version has different bytes');
  return true;
}

export async function waitForPackage(packed, {
  signal = AbortSignal.timeout(15 * 60_000),
  pollMs = 10_000,
  fetchImpl = fetch,
  onPending = console.log,
} = {}) {
  try {
    for (;;) {
      signal.throwIfAborted();
      // npm installation metadata can lag the individual version endpoint.
      if (await registryHasPackage(packed, { signal, fetchImpl })
        && await registryHasPackage(packed, { signal, fetchImpl, installMetadata: true })) {
        return;
      }
      onPending(`${packed.name}@${packed.version}: waiting for npm processing and registry availability`);
      await delay(pollMs, undefined, { signal });
    }
  } catch (error) {
    if (signal.aborted) {
      throw new Error(`${packed.name}@${packed.version}: registry availability deadline reached; `
        + 'inspect npm processing/review status before retrying acceptance', { cause: error });
    }
    throw error;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    const [file, mode, ...extra] = process.argv.slice(2);
    assert.ok(file && (mode === undefined || mode === '--wait') && extra.length === 0,
      'usage: node tools/check-parse-sdk-registry.mjs pack.json [--wait]');
    // `pnpm pack --json` output for the one package.
    const packed = JSON.parse(fs.readFileSync(file, 'utf8'));
    assert.equal(packed?.name, '@ashtonian/parse');
    assert.match(packed.version, /^\d+\.\d+\.\d+$/);
    assert.match(packed.integrity, /^sha512-[A-Za-z0-9+/]+={0,2}$/);
    if (mode === '--wait') {
      await waitForPackage(packed);
      console.log(`${packed.name}@${packed.version}: registry metadata matches the retained package`);
    } else {
      console.log(`publish=${!await registryHasPackage(packed)}`);
    }
  } catch (error) {
    console.error(error);
    process.exitCode = 1;
  }
}
