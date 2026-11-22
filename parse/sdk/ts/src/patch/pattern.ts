// Path patterns as the Go patch schema resolves them (parse/patch/pattern.go):
// a schema key is a JSON Pointer whose tokens are each a literal, `*`
// (exactly one token) or `**` (zero or more). A request path takes its exact
// key, else the most specific pattern that matches it.

import type { PatchPathSchema } from "./types.js";

const ONE = "*";
const ANY = "**";

interface Pattern {
  readonly key: string;
  readonly tokens: readonly string[];
}

// Tokens stay escaped, as paths compare them: "" has none, "/" one empty
// token, "/a//b" three.
function pointerTokens(s: string): string[] {
  return s === "" ? [] : (s.startsWith("/") ? s.slice(1) : s).split("/");
}

const isWildcard = (t: string): boolean => t === ONE || t === ANY;

function literalPrefix(p: Pattern): number {
  const i = p.tokens.findIndex(isWildcard);
  return i < 0 ? p.tokens.length : i;
}

const count = (p: Pattern, wildcard: string): number => p.tokens.filter((t) => t === wildcard).length;

// Most specific first: more leading literals, then fewer `**`, fewer `*`,
// more tokens, then by key, so selection never depends on key order.
function compareSpecificity(a: Pattern, b: Pattern): number {
  return (literalPrefix(b) - literalPrefix(a))
    || (count(a, ANY) - count(b, ANY))
    || (count(a, ONE) - count(b, ONE))
    || (b.tokens.length - a.tokens.length)
    || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0);
}

// The pattern run as an automaton over the path's tokens.
function matches(p: Pattern, path: readonly string[]): boolean {
  const close = (reached: boolean[]): void => {
    p.tokens.forEach((t, s) => {
      if (reached[s] && t === ANY) reached[s + 1] = true;
    });
  };
  let reached = new Array<boolean>(p.tokens.length + 1).fill(false);
  reached[0] = true;
  close(reached);
  for (const tok of path) {
    const next = new Array<boolean>(reached.length).fill(false);
    let alive = false;
    for (let s = 0; s < p.tokens.length; s++) {
      if (!reached[s]) continue;
      const want = p.tokens[s];
      if (want === ANY) {
        next[s] = true;
        alive = true;
      } else if (want === ONE || want === tok) {
        next[s + 1] = true;
        alive = true;
      }
    }
    if (!alive) return false;
    close(next);
    reached = next;
  }
  return reached[p.tokens.length] === true;
}

const patternsBySchema = new WeakMap<Record<string, PatchPathSchema>, readonly Pattern[]>();

function patternsOf(paths: Record<string, PatchPathSchema>): readonly Pattern[] {
  let patterns = patternsBySchema.get(paths);
  if (!patterns) {
    patterns = Object.keys(paths)
      .map((key) => ({ key, tokens: pointerTokens(key) }))
      .filter((p) => p.tokens.some(isWildcard))
      .sort(compareSpecificity);
    patternsBySchema.set(paths, patterns);
  }
  return patterns;
}

/** The path schema for a request path: its exact key, else the most specific matching pattern. */
export function resolvePath(paths: Record<string, PatchPathSchema>, path: string): PatchPathSchema | undefined {
  if (Object.hasOwn(paths, path)) return paths[path];
  const tokens = pointerTokens(path);
  const pattern = patternsOf(paths).find((p) => matches(p, tokens));
  return pattern ? paths[pattern.key] : undefined;
}
