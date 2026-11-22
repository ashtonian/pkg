// Runs release-please as release-please-action does, with the first-parent
// plugin registered: creates the GitHub releases a merged release PR
// describes, then opens or updates the release PR, and writes the action's
// outputs. --dry-run prints the release PRs it would open and writes nothing
// to GitHub; it still fetches the target branch into the clone.
//
//   GITHUB_TOKEN=… node tools/release-please/run.mjs --repo owner/name [--dry-run]
import fs from 'node:fs';
import { parseArgs } from 'node:util';
import { GitHub, Manifest, VERSION } from 'release-please';
import { fetchBranchHead, readHistory } from './first-parent.mjs';
import { Output, outputPullRequests, outputReleases } from './outputs.mjs';
import { FIRST_PARENT, FirstParentPlugin, registerFirstParent } from './plugin.mjs';

function parseOptions() {
  return parseArgs({
    options: {
      repo: { type: 'string', default: process.env.GITHUB_REPOSITORY },
      'config-file': { type: 'string', default: 'release-please-config.json' },
      'manifest-file': { type: 'string', default: '.release-please-manifest.json' },
      // A clone of the repository with its full history, and its remote for it.
      checkout: { type: 'string', default: '.' },
      remote: { type: 'string', default: 'origin' },
      // The action's inputs: the workflow creates releases and updates the
      // release PR in separate jobs.
      'skip-github-release': { type: 'boolean', default: false },
      'skip-github-pull-request': { type: 'boolean', default: false },
      'dry-run': { type: 'boolean', default: false },
    },
  }).values;
}

async function main() {
  const args = parseOptions();
  const token = process.env.GITHUB_TOKEN;
  if (!token) throw new Error('GITHUB_TOKEN is not set');
  const [owner, repo] = (args.repo ?? '').split('/');
  if (!owner || !repo) throw new Error(`--repo must be owner/name, got ${JSON.stringify(args.repo)}`);
  const output = args['dry-run'] ? undefined : stepOutput();
  console.log(`Running release-please version: ${VERSION}`);

  const github = await GitHub.create({ owner, repo, token });
  const branch = github.repository.defaultBranch;
  const buildsPullRequests = args['dry-run'] || !args['skip-github-pull-request'];
  // Creating releases reads the merged release PR, not commits, so only
  // building one needs the history.
  let history = { mergeOf: new Map(), messageOf: new Map() };
  if (buildsPullRequests) {
    // release-please reads the branch as GitHub has it now, which can be
    // ahead of the commit this job checked out.
    const head = fetchBranchHead(args.checkout, args.remote, branch);
    history = readHistory(args.checkout, head);
    console.log(`first-parent: history at ${head} (${args.remote}/${branch}), ${history.mergeOf.size} branch commits mapped to their merges`);
  }
  registerFirstParent(history);
  // Each phase reads the repository afresh, as the action does: creating the
  // releases changes what the release PR holds.
  let warned = false;
  const manifest = async () => {
    const loaded = await Manifest.fromManifest(github, branch, args['config-file'], args['manifest-file']);
    // The config names the plugin so the stock action refuses it; a config
    // that does not still gets it. It runs first, before any plugin that
    // rewrites the messages it matches headers by.
    let plugin = loaded.plugins.find(p => p instanceof FirstParentPlugin);
    if (plugin === undefined) {
      if (!warned) console.warn(`${args['config-file']} on ${branch} names no ${FIRST_PARENT} plugin; adding it`);
      warned = true;
      plugin = new FirstParentPlugin(github, branch, loaded.repositoryConfig, history);
    }
    loaded.plugins = [plugin, ...loaded.plugins.filter(p => p !== plugin)];
    return loaded;
  };

  if (args['dry-run']) {
    for (const pr of await (await manifest()).buildPullRequests()) {
      console.log(`\n# ${pr.title.toString()}\n\n${pr.body.toString()}`);
    }
    return;
  }
  if (!args['skip-github-release']) outputReleases(output, await (await manifest()).createReleases());
  if (!args['skip-github-pull-request']) outputPullRequests(output, await (await manifest()).createPullRequests());
}

function stepOutput() {
  const file = process.env.GITHUB_OUTPUT;
  if (!file) throw new Error('GITHUB_OUTPUT is not set; run with --dry-run outside GitHub Actions');
  return new Output(text => fs.appendFileSync(file, text));
}

main().catch(err => {
  console.error(`release-please failed: ${err.stack ?? err}`);
  process.exitCode = 1;
});
