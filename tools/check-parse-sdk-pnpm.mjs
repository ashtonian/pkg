// The SDK's pnpm contract, checked before an install.
//
// pnpm-workspace.yaml sets pmOnFail: ignore so that pnpm-lock.yaml stays one YAML
// document, the only form GitHub's dependency graph, SBOM and Dependabot read. The cost
// is that pnpm no longer enforces package.json's packageManager pin, and a leading
// document written once under another setting survives every later install. So this
// check covers the setting, the single document and the pnpm on PATH.
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const sdk = process.env.PARSE_SDK_DIR
  ?? path.join(path.dirname(fileURLToPath(import.meta.url)), '..', 'parse', 'sdk', 'ts');
const read = (name) => fs.readFileSync(path.join(sdk, name), 'utf8');
const problems = [];

if (!/^pmOnFail:\s*ignore\s*(#.*)?$/m.test(read('pnpm-workspace.yaml'))) {
  problems.push(`${sdk}/pnpm-workspace.yaml must set pmOnFail: ignore, or pnpm writes a second document into pnpm-lock.yaml`);
}

// pnpm writes no document markers in a single-document lockfile.
const markers = read('pnpm-lock.yaml').split('\n')
  .flatMap((line, i) => (/^(---|\.\.\.)(\s|$)/.test(line) ? [i + 1] : []));
if (markers.length) {
  problems.push(`${sdk}/pnpm-lock.yaml has YAML document markers on line(s) ${markers.join(', ')}, so GitHub's `
    + 'dependency graph ignores it. Delete the leading packageManagerDependencies document (from the first --- '
    + 'through the second) and keep pmOnFail: ignore; pnpm keeps one document from then on.');
}

const { packageManager = '' } = JSON.parse(read('package.json'));
const pinned = /^pnpm@(\d+\.\d+\.\d+)(\+\S+)?$/.exec(packageManager)?.[1];
if (!pinned) {
  problems.push(`${sdk}/package.json: packageManager must pin an exact pnpm release (pnpm@X.Y.Z), not "${packageManager}"`);
} else {
  // Outside the project, pnpm reports its own version: it neither switches to the pin
  // nor rewrites the lockfile.
  let running;
  try {
    running = execFileSync('pnpm', ['--version'], { cwd: os.tmpdir(), encoding: 'utf8', timeout: 30_000 }).trim();
  } catch (error) {
    problems.push(`pnpm --version failed (${error.code ?? error.message}): install pnpm ${pinned}`);
  }
  if (running !== undefined && running !== pinned) {
    problems.push(`pnpm ${running} is on PATH, but the SDK pins pnpm ${pinned}: run pnpm self-update ${pinned}`);
  }
}

if (problems.length) {
  for (const problem of problems) console.error(`parse-sdk-pnpm: ${problem}`);
  process.exit(1);
}
console.log(`parse-sdk-pnpm: pnpm ${pinned}, pmOnFail: ignore, one-document pnpm-lock.yaml`);
