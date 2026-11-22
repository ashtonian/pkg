// Step outputs, named and encoded as release-please-action v5 writes them, so
// the workflow reads them unchanged.
import { randomUUID } from 'node:crypto';

// Output writes step outputs in GITHUB_OUTPUT's multiline form through append,
// as @actions/core's setOutput does: a string as is, null or undefined as an
// empty string, anything else as JSON.
export class Output {
  constructor(append) {
    this.append = append;
  }

  set(key, value) {
    const text = encode(value);
    const delimiter = `ghadelimiter_${randomUUID()}`;
    this.append(`${key}<<${delimiter}\n${text}\n${delimiter}\n`);
  }

  setPath(path, key, value) {
    this.set(path === '.' ? key : `${path}--${key}`, value);
  }
}

function encode(value) {
  if (value === null || value === undefined) return '';
  if (typeof value === 'string') return value;
  return JSON.stringify(value);
}

const RELEASE_KEYS = { tagName: 'tag_name', uploadUrl: 'upload_url', notes: 'body', url: 'html_url' };

// outputReleases writes Manifest.createReleases' result.
export function outputReleases(output, releases) {
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

// outputPullRequests writes Manifest.createPullRequests' result.
export function outputPullRequests(output, pullRequests) {
  const opened = pullRequests.filter(Boolean);
  output.set('prs_created', opened.length > 0);
  if (opened.length > 0) {
    output.set('pr', opened[0]);
    output.set('prs', JSON.stringify(opened));
  }
}
