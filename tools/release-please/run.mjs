// Runs release-please as release-please-action does, with the first-parent
// plugin registered: creates the GitHub releases a merged release PR
// describes, then opens or updates the release PR, and writes the action's
// outputs. --dry-run prints the release PRs it would open and writes nothing.
//
//   GITHUB_TOKEN=… node tools/release-please/run.mjs [--dry-run]
import { randomUUID } from 'node:crypto';
import fs from 'node:fs';
import { parseArgs } from 'node:util';
import { GitHub, Manifest, VERSION } from 'release-please';
import { readHistory } from './first-parent.mjs';
import { FIRST_PARENT, FirstParentPlugin, registerFirstParent } from './plugin.mjs';

const { values: args } = parseArgs({
  options: {
    repo: { type: 'string', default: process.env.GITHUB_REPOSITORY },
    'config-file': { type: 'string', default: 'release-please-config.json' },
    'manifest-file': { type: 'string', default: '.release-please-manifest.json' },
    // A checkout of the target branch with its full history.
    checkout: { type: 'string', default: '.' },
    'dry-run': { type: 'boolean', default: false },
  },
});

async function main() {
  const token = process.env.GITHUB_TOKEN;
  if (!token) throw new Error('GITHUB_TOKEN is not set');
  const [owner, repo] = (args.repo ?? '').split('/');
  if (!owner || !repo) throw new Error(`--repo must be owner/name, got ${JSON.stringify(args.repo)}`);
  console.log(`Running release-please version: ${VERSION}`);

  const github = await GitHub.create({ owner, repo, token });
  const history = readHistory(args.checkout, 'HEAD');
  console.log(`first-parent: ${history.mergeOf.size} branch commits mapped to their merges`);
  registerFirstParent(history);
  // Each phase reads the repository afresh, as the action does: creating the
  // releases changes what the release PR holds.
  const manifest = async () => {
    const loaded = await Manifest.fromManifest(github, github.repository.defaultBranch, args['config-file'], args['manifest-file']);
    // The config names the plugin so the stock action refuses it. A config
    // that does not (the target branch's, before it does) still gets it, first.
    if (!loaded.plugins.some(plugin => plugin instanceof FirstParentPlugin)) {
      console.warn(`${args['config-file']} on ${github.repository.defaultBranch} names no ${FIRST_PARENT} plugin; adding it`);
      loaded.plugins.unshift(new FirstParentPlugin(github, github.repository.defaultBranch, loaded.repositoryConfig, history));
    }
    return loaded;
  };

  if (args['dry-run']) {
    for (const pr of await (await manifest()).buildPullRequests()) {
      console.log(`\n# ${pr.title.toString()}\n\n${pr.body.toString()}`);
    }
    return;
  }
  const output = new Output(process.env.GITHUB_OUTPUT);
  outputReleases(output, await (await manifest()).createReleases());
  outputPullRequests(output, await (await manifest()).createPullRequests());
}

// Output writes step outputs as @actions/core's setOutput does.
class Output {
  constructor(file) {
    if (!file) throw new Error('GITHUB_OUTPUT is not set; run with --dry-run outside GitHub Actions');
    this.file = file;
  }

  set(key, value) {
    const text = typeof value === 'string' ? value : JSON.stringify(value);
    const delimiter = `ghadelimiter_${randomUUID()}`;
    fs.appendFileSync(this.file, `${key}<<${delimiter}\n${text}\n${delimiter}\n`);
  }

  setPath(path, key, value) {
    this.set(path === '.' ? key : `${path}--${key}`, value);
  }
}

// The action's output names, so the workflow reads them unchanged.
const RELEASE_KEYS = { tagName: 'tag_name', uploadUrl: 'upload_url', notes: 'body', url: 'html_url' };

function outputReleases(output, releases) {
  const created = releases.filter(Boolean);
  const paths = [];
  output.set('releases_created', created.length > 0);
  for (const release of created) {
    const path = release.path || '.';
    paths.push(path);
    output.setPath(path, 'release_created', true);
    for (const [key, value] of Object.entries(release)) {
      output.setPath(path, RELEASE_KEYS[key] ?? key, value);
    }
  }
  output.set('paths_released', JSON.stringify(paths));
}

function outputPullRequests(output, pullRequests) {
  const opened = pullRequests.filter(Boolean);
  output.set('prs_created', opened.length > 0);
  if (opened.length > 0) {
    output.set('pr', opened[0]);
    output.set('prs', JSON.stringify(opened));
  }
}

main().catch(err => {
  console.error(`release-please failed: ${err.stack ?? err}`);
  process.exitCode = 1;
});
