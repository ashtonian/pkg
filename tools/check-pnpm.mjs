// The repository's pnpm contract, checked before a pnpm install: every pnpm
// project here, each directory holding a tracked pnpm-lock.yaml.
//
// Each project's pnpm-workspace.yaml sets pmOnFail: ignore, so pnpm-lock.yaml
// stays one YAML document, the only form GitHub's dependency graph, SBOM and
// Dependabot alerts read. The cost is that pnpm no longer enforces
// package.json's packageManager pin, and a leading document written once
// under another setting survives every later install. So this checks the
// setting and the single document in each project, that every project pins
// the same exact pnpm release, and that the pnpm on PATH is that release.
//
//   node tools/check-pnpm.mjs [--root <repository>]
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseArgs } from 'node:util';

const { values: args } = parseArgs({
  options: { root: { type: 'string', default: path.join(path.dirname(fileURLToPath(import.meta.url)), '..') } },
});
const problems = [];

const tracked = execFileSync('git', ['-C', args.root, 'ls-files', '-z', '--', '*pnpm-lock.yaml'], { encoding: 'utf8' });
const projects = tracked.split('\0').filter(file => path.basename(file) === 'pnpm-lock.yaml').map(file => path.dirname(file));
if (projects.length === 0) problems.push(`${args.root} tracks no pnpm-lock.yaml`);

const read = (project, name) => {
  try {
    return fs.readFileSync(path.join(args.root, project, name), 'utf8');
  } catch (err) {
    problems.push(`${project}/${name}: ${err.code ?? err.message}`);
    return '';
  }
};

const pins = new Map();
for (const project of projects) {
  if (!/^pmOnFail:\s*ignore\s*(#.*)?$/m.test(read(project, 'pnpm-workspace.yaml'))) {
    problems.push(`${project}/pnpm-workspace.yaml must set pmOnFail: ignore, or pnpm writes a second document into pnpm-lock.yaml`);
  }
  // pnpm writes no document markers in a single-document lockfile.
  const markers = read(project, 'pnpm-lock.yaml').split('\n')
    .flatMap((line, i) => (/^(---|\.\.\.)(\s|$)/.test(line) ? [i + 1] : []));
  if (markers.length > 0) {
    problems.push(`${project}/pnpm-lock.yaml has YAML document markers on line(s) ${markers.join(', ')}, so GitHub's `
      + 'dependency graph ignores it. Delete the leading packageManagerDependencies document (from the first --- '
      + 'through the second) and keep pmOnFail: ignore; pnpm keeps one document from then on.');
  }
  let packageManager = '';
  try {
    packageManager = JSON.parse(read(project, 'package.json') || '{}').packageManager ?? '';
  } catch (err) {
    problems.push(`${project}/package.json: ${err.message}`);
  }
  const pinned = /^pnpm@(\d+\.\d+\.\d+)(\+\S+)?$/.exec(packageManager)?.[1];
  if (pinned === undefined) {
    problems.push(`${project}/package.json: packageManager must pin an exact pnpm release (pnpm@X.Y.Z), not "${packageManager}"`);
  } else {
    pins.set(project, pinned);
  }
}

const releases = new Set(pins.values());
if (releases.size > 1) {
  const listed = [...pins].map(([project, pinned]) => `${project} ${pinned}`).join(', ');
  problems.push(`the pnpm projects pin different pnpm releases (${listed}); one host runs one pnpm, so pin the same release in each package.json`);
} else if (releases.size === 1) {
  const [pinned] = releases;
  // Outside the projects, pnpm reports its own version: it neither switches to
  // a pin nor rewrites a lockfile.
  let running;
  try {
    running = execFileSync('pnpm', ['--version'], { cwd: os.tmpdir(), encoding: 'utf8', timeout: 30_000 }).trim();
  } catch (err) {
    problems.push(`pnpm --version failed (${err.code ?? err.message}); install the pinned release into the active Node.js: npm install --global pnpm@${pinned}`);
  }
  if (running !== undefined && running !== pinned) {
    problems.push(`pnpm ${running} is on PATH (${onPath('pnpm') ?? 'unresolved'}), but the repository pins pnpm ${pinned}; `
      + `install it into the active Node.js: npm install --global pnpm@${pinned}`);
  }
}

function onPath(command) {
  for (const dir of (process.env.PATH ?? '').split(path.delimiter)) {
    const candidate = path.join(dir, command);
    try {
      fs.accessSync(candidate, fs.constants.X_OK);
      return candidate;
    } catch {
      // not in this directory
    }
  }
  return undefined;
}

if (problems.length > 0) {
  for (const problem of problems) console.error(`pnpm contract: ${problem}`);
  process.exit(1);
}
console.log(`pnpm contract: pnpm ${[...releases][0]}, pmOnFail: ignore and a one-document pnpm-lock.yaml in ${projects.join(', ')}`);
