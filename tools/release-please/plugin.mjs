// The first-parent plugin as release-please loads it: named "first-parent" in
// release-please-config.json's plugins.
import { registerPlugin } from 'release-please';
import { ManifestPlugin } from 'release-please/build/src/plugin.js';
import { firstParentCommits, typeRanker } from './first-parent.mjs';

export const FIRST_PARENT = 'first-parent';

export class FirstParentPlugin extends ManifestPlugin {
  constructor(github, targetBranch, repositoryConfig, history, logger) {
    super(github, targetBranch, repositoryConfig, logger);
    this.history = history;
    this.rank = typeRanker(Object.values(repositoryConfig).map(config => config.changelogSections));
  }

  // processCommits runs before release-please computes versions and notes.
  processCommits(commits) {
    return firstParentCommits(commits, this.history, { rank: this.rank, warn: message => this.logger.warn(message) });
  }
}

// registerFirstParent makes the config's "first-parent" build a plugin over
// history (readHistory's result).
export function registerFirstParent(history) {
  registerPlugin(FIRST_PARENT, options =>
    new FirstParentPlugin(options.github, options.targetBranch, options.repositoryConfig, history, options.logger));
}
