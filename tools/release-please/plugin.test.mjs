import assert from 'node:assert/strict';
import test from 'node:test';
import { buildPlugin } from 'release-please/build/src/factories/plugin-factory.js';
import { FIRST_PARENT, FirstParentPlugin, registerFirstParent } from './plugin.mjs';

// release-please builds the config's plugins through its factory; this is the
// path a release-please upgrade that moves the plugin API would break.
test('a config naming first-parent builds the plugin over the history', () => {
  const history = { mergeOf: new Map([['b1', 'm1']]) };
  registerFirstParent(history);
  const github = { repository: { owner: 'o', repo: 'r', defaultBranch: 'main' } };

  const plugin = buildPlugin({ type: FIRST_PARENT, github, targetBranch: 'main', repositoryConfig: {}, manifestPath: '.release-please-manifest.json' });

  assert.ok(plugin instanceof FirstParentPlugin);
  const merge = { sha: 'm1', bareMessage: 'x (#1)', notes: [], references: [], breaking: false };
  const branch = { sha: 'b1', bareMessage: 'x', notes: [], references: [], breaking: false };
  assert.deepEqual(plugin.processCommits([merge, branch]), [merge]);
});
