import assert from 'node:assert/strict';
import test from 'node:test';
import { Output, outputPullRequests, outputReleases } from './outputs.mjs';

// collect parses what Output appends back into key → value.
function collect() {
  const values = {};
  const output = new Output(text => {
    const [, key, delimiter, value] = text.match(/^(.+?)<<(ghadelimiter_[0-9a-f-]+)\n([\s\S]*)\n\2\n$/);
    assert.ok(delimiter);
    values[key] = value;
  });
  return { output, values };
}

test("a created release's fields are written under the action's names", () => {
  const { output, values } = collect();
  outputReleases(output, [
    undefined,
    { path: 'parse', tagName: 'parse/v0.15.0', version: '0.15.0', notes: 'notes', url: 'https://example.com/r', uploadUrl: 'u', name: undefined, draft: false },
  ]);

  assert.equal(values.releases_created, 'true');
  assert.equal(values.paths_released, '["parse"]');
  assert.equal(values['parse--release_created'], 'true');
  assert.equal(values['parse--tag_name'], 'parse/v0.15.0');
  assert.equal(values['parse--body'], 'notes');
  assert.equal(values['parse--html_url'], 'https://example.com/r');
  assert.equal(values['parse--name'], '', 'an absent field is empty, as @actions/core writes it');
  assert.equal(values['parse--draft'], 'false');
});

test('no release and no pull request', () => {
  const { output, values } = collect();
  outputReleases(output, []);
  outputPullRequests(output, [undefined]);
  assert.deepEqual(values, { releases_created: 'false', paths_released: '[]', prs_created: 'false' });
});

test('a multiline value survives the delimiter', () => {
  const { output, values } = collect();
  output.set('body', 'line one\nline two');
  assert.equal(values.body, 'line one\nline two');
});
