// The pull-request check: reads each commit a pull request brings to main,
// and its commit override, as release-please will, and fails naming each one
// whose type, BREAKING CHANGE notes or issue references the release notes
// would lose.
//
//   node tools/release-please/check-commits.mjs --base origin/main [--head HEAD] [--body-file description.md]
import fs from 'node:fs';
import { parseArgs } from 'node:util';
import { checkPullRequest, pullRequestCommits } from './commit-messages.mjs';

const RULES = 'CONTRIBUTING.md#releases';

function parseOptions() {
  const { values } = parseArgs({
    options: {
      base: { type: 'string' },
      head: { type: 'string', default: 'HEAD' },
      // The pull request's description, for its BEGIN_COMMIT_OVERRIDE block.
      'body-file': { type: 'string' },
      checkout: { type: 'string', default: '.' },
    },
  });
  if (!values.base) throw new Error('--base is required');
  return values;
}

function main() {
  const args = parseOptions();
  const body = args['body-file'] === undefined ? '' : fs.readFileSync(args['body-file'], 'utf8');
  const commits = pullRequestCommits(args.checkout, args.base, args.head);
  const findings = checkPullRequest(commits, body);
  const annotate = process.env.GITHUB_ACTIONS === 'true';
  let failed = false;
  for (const { source, problems, replaced } of findings) {
    failed ||= !replaced;
    for (const problem of problems) {
      const text = replaced ? `${source}: ${problem}. release-please reads the commit override in its place` : `${source}: ${problem}`;
      console.log(annotate ? `::${replaced ? 'warning' : 'error'} title=Release notes::${escapeData(text)}` : `${replaced ? 'warning' : 'error'}: ${text}`);
    }
  }
  console.log(`${commits.length} ${commits.length === 1 ? 'commit' : 'commits'} in ${args.base}..${args.head}${body.includes('BEGIN_COMMIT_OVERRIDE') ? ' and the commit override' : ''} read as release-please reads them; ${failed ? `fix the errors above (${RULES})` : 'nothing is lost'}`);
  if (failed) process.exitCode = 1;
}

// A workflow command's message ends at its line; GitHub decodes these.
const escapeData = text => text.replaceAll('%', '%25').replaceAll('\r', '%0D').replaceAll('\n', '%0A');

try {
  main();
} catch (err) {
  console.error(`check-commits failed: ${err.stack ?? err}`);
  process.exitCode = 2;
}
