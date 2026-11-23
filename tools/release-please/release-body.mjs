// How a release PR's body carries each release's notes, read back as the
// Markdown release-please writes rather than as HTML.
//
// release-please reads a merged release PR's body with an HTML parser. Text in
// one release's notes that looks like a tag (`include=<key>`, `Key<T>` in a
// code span) is read as an element that swallows the sections after it, and
// the entities it decodes come back raw when it renders the body again before
// each path reads it: those components are silently left unreleased
// (googleapis/release-please#2952, #2899, #2801, #2884). The body is
// release-please's own rendering, one section a release, so it is read here
// by its lines and each release's notes are kept exactly as written.
import { createRequire } from 'node:module';

const require = createRequire(import.meta.url);
const { PullRequestBody } = require('release-please/build/src/util/pull-request-body.js');
const { Version } = require('release-please/build/src/version.js');

const DELIMITER = '---';
const SECTION = /^<details><summary>(?<summary>.*)<\/summary>$/;
const CLOSE = '</details>';
// release-please's own summary patterns (util/pull-request-body.js).
const SUMMARY = /^(?<component>.*[^:]):? (?<version>\d+\.\d+\.\d+.*)$/;
const COMPONENTLESS = /^(?<version>\d+\.\d+\.\d+.*)$/;

const stockParse = PullRequestBody.parse;
const stockToString = PullRequestBody.prototype.toString;

// releaseSections splits a body as release-please's splitBody does and reads
// each <details> section of its content: a section runs from its header to
// the last </details> line before the next header, so notes holding either
// line as text stay whole. It returns undefined for a body that is not a
// release PR's, and no sections for one release-please wrote without
// components.
export function releaseSections(body) {
  const lines = body.trim().replace(/\r\n/g, '\n').split('\n');
  const first = lines.indexOf(DELIMITER);
  if (first === -1) return undefined;
  let last = lines.lastIndexOf(DELIMITER);
  if (last === first) last = lines.length - 1;
  const header = lines.slice(0, first).join('\n').trim();
  const footer = lines.slice(last + 1).join('\n');
  const content = lines.slice(first + 1, last);

  const starts = [];
  content.forEach((line, i) => {
    if (SECTION.test(line)) starts.push(i);
  });
  const sections = [];
  for (const [n, start] of starts.entries()) {
    const end = n + 1 < starts.length ? starts[n + 1] : content.length;
    const close = content.lastIndexOf(CLOSE, end - 1);
    if (close <= start) {
      throw new Error(`release PR body: section "${content[start]}" has no ${CLOSE} line before the next section`);
    }
    sections.push({
      summary: content[start].match(SECTION).groups.summary,
      notes: content.slice(start + 1, close).join('\n').trim(),
    });
  }
  return { header, footer, sections };
}

// parseReleaseBody is PullRequestBody.parse over releaseSections: the same
// release data, with each release's notes verbatim.
export function parseReleaseBody(body, logger) {
  const parts = releaseSections(body);
  if (parts === undefined || parts.sections.length === 0) return stockParse.call(PullRequestBody, body, logger);
  const data = [];
  for (const { summary, notes } of parts.sections) {
    const named = summary.match(SUMMARY);
    const bare = named ? undefined : summary.match(COMPONENTLESS);
    if (!named && !bare) {
      logger?.warn(`Summary: ${summary} did not match the expected pattern`);
      continue;
    }
    const groups = (named ?? bare).groups;
    data.push({ component: groups.component, version: Version.parse(groups.version), notes });
  }
  return new PullRequestBody(data, { header: parts.header, footer: parts.footer, useComponents: true });
}

// renderReleaseBody is PullRequestBody's toString, refused when the body it
// renders would not read back as the same releases: a release PR whose body
// loses a release would merge without tagging it.
export function renderReleaseBody() {
  const body = stockToString.call(this);
  if (!this.useComponents) return body;
  const read = parseReleaseBody(body).releaseData;
  const key = release => `${release.component ?? ''}@${release.version?.toString()}`;
  const lost = this.releaseData.filter(release => {
    const back = read.find(r => key(r) === key(release));
    return back === undefined || back.notes !== release.notes.trim();
  });
  if (lost.length > 0) {
    throw new Error(`release PR body does not read back: ${lost.map(key).join(', ')} would be lost or changed; see tools/release-please/README.md`);
  }
  return body;
}

// installReleaseBody makes every PullRequestBody release-please builds or
// reads, the merged release PR's included, go through parseReleaseBody and
// renderReleaseBody. It is idempotent.
export function installReleaseBody() {
  PullRequestBody.parse = parseReleaseBody;
  PullRequestBody.prototype.toString = renderReleaseBody;
}

// escapeNotes escapes the BREAKING CHANGE notes of commits as release-please
// escapes their subjects (changelog-notes/default.js): < and > outside code
// spans become entities, so a changelog, a release PR and a release page show
// them instead of reading them as HTML.
export function escapeNotes(commits) {
  return commits.map(commit => ({
    ...commit,
    notes: commit.notes.map(note => (note.title === 'BREAKING CHANGE' ? { ...note, text: htmlEscape(note.text) } : note)),
  }));
}

function htmlEscape(text) {
  return text.replace(/``[^`].*[^`]``|`[^`]*`|<|>/g, match => (match.length > 1 ? match : match === '<' ? '&lt;' : '&gt;'));
}
