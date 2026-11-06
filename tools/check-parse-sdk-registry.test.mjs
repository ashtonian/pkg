import assert from 'node:assert/strict';
import test from 'node:test';
import { registryHasPackage, waitForPackage } from './check-parse-sdk-registry.mjs';

const packed = { name: '@ashtonian/parse', version: '0.10.1', integrity: 'sha512-test' };
const version = { name: packed.name, version: packed.version, dist: { integrity: packed.integrity } };
const install = { versions: { [packed.version]: version } };

function responses(sequence) {
  const calls = [];
  const fetchImpl = async (url, options) => {
    calls.push({ url, ...options });
    assert.ok(sequence.length, 'unexpected registry request');
    return sequence.shift();
  };
  return { fetchImpl, calls };
}

test('an absent version requires publication; an exact existing version does not', async () => {
  const { fetchImpl, calls } = responses([new Response(null, { status: 404 }), Response.json(version)]);
  assert.equal(await registryHasPackage(packed, { fetchImpl }), false);
  assert.equal(await registryHasPackage(packed, { fetchImpl }), true);
  assert.equal(calls[0].url, 'https://registry.npmjs.org/%40ashtonian%2Fparse/0.10.1');
  assert.equal(calls[0].headers.Accept, 'application/json');
  assert.ok(calls[0].signal instanceof AbortSignal);
});

test('waits through processing and install metadata lag before accepting the package', async () => {
  const { fetchImpl, calls } = responses([
    new Response(null, { status: 404 }),
    Response.json(version), Response.json({ versions: {} }),
    Response.json(version), Response.json(install),
  ]);
  const pending = [];
  await waitForPackage(packed, { fetchImpl, pollMs: 0, onPending: message => pending.push(message) });
  assert.equal(pending.length, 2);
  assert.equal(calls.length, 5);
  assert.equal(calls[2].url, 'https://registry.npmjs.org/%40ashtonian%2Fparse');
  assert.equal(calls[2].headers.Accept, 'application/vnd.npm.install-v1+json');
});

for (const installMetadata of [false, true]) {
  test(`rejects differing integrity in ${installMetadata ? 'install' : 'version'} metadata immediately`, async () => {
    const wrong = { ...version, dist: { integrity: 'sha512-other' } };
    const sequence = installMetadata
      ? [Response.json(version), Response.json({ versions: { [packed.version]: wrong } })]
      : [Response.json(wrong)];
    const { fetchImpl, calls } = responses(sequence);
    await assert.rejects(waitForPackage(packed, { fetchImpl }), /different bytes/);
    assert.equal(calls.length, installMetadata ? 2 : 1);
  });
}

for (const status of [401, 403, 429, 500]) {
  test(`does not hide HTTP ${status} behind availability retries`, async () => {
    const { fetchImpl, calls } = responses([new Response(null, { status })]);
    await assert.rejects(waitForPackage(packed, { fetchImpl }), new RegExp(`HTTP ${status}`));
    assert.equal(calls.length, 1);
  });
}

test('malformed metadata fails without retries', async () => {
  const { fetchImpl } = responses([Response.json(version), Response.json({ versions: null })]);
  await assert.rejects(waitForPackage(packed, { fetchImpl }), /no version map/);
  await assert.rejects(registryHasPackage(packed, {
    fetchImpl: async () => new Response('{invalid'),
  }), SyntaxError);
});

test('the overall deadline interrupts polling and active requests', async () => {
  for (const activeRequest of [false, true]) {
    const controller = new AbortController();
    let calls = 0;
    const fetchImpl = async (_url, { signal }) => {
      calls++;
      controller.abort(new Error('test deadline'));
      if (activeRequest) signal.throwIfAborted();
      return new Response(null, { status: 404 });
    };
    await assert.rejects(waitForPackage(packed, {
      signal: controller.signal, fetchImpl, onPending: () => {},
    }), /registry availability deadline reached/);
    assert.equal(calls, 1);
  }
});
