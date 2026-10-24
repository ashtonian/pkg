#!/usr/bin/env bash
# Exercise the packed public SDK outside its source tree and dependency graph.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
sdk="$repo_root/parse/sdk/ts"
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
if [[ ! -f "$sdk/dist/index.js" ]]; then
    echo 'parse-sdk-release-check: run make test-parse-sdk to build the SDK first' >&2
    exit 1
fi
if [[ $# -gt 1 ]]; then
    echo 'usage: check-parse-sdk-release.sh [tarball]' >&2
    exit 2
fi
if [[ $# == 1 ]]; then
    tarball=$(cd -- "$(dirname -- "$1")" && pwd)/$(basename -- "$1")
else
    (cd "$sdk" && npm pack --json --pack-destination "$scratch") > "$scratch/pack.json"
    tarball="$scratch/$(node --input-type=module -e 'import fs from "node:fs"; console.log(JSON.parse(fs.readFileSync(process.argv[1], "utf8"))[0].filename)' "$scratch/pack.json")"
fi
mkdir "$scratch/consumer"
cd "$scratch/consumer"
cat > package.json <<'JSON'
{"private":true,"type":"module"}
JSON
npm install --ignore-scripts --no-audit --no-fund "$tarball"
cat > tsconfig.json <<'JSON'
{
  "compilerOptions": {
    "target": "ES2023", "module": "Node16", "moduleResolution": "Node16",
    "strict": true, "noUncheckedIndexedAccess": true,
    "exactOptionalPropertyTypes": true, "types": [],
    "lib": ["ES2023", "DOM"], "outDir": "dist"
  },
  "include": ["index.ts"]
}
JSON
cat > index.ts <<'TS'
import { filter, sort, SCHEMA_VERSION } from "@ashtonian/parse";
import { ApiError } from "@ashtonian/parse/core";
import type { ListResult } from "@ashtonian/parse/filter";
import { validatePatch } from "@ashtonian/parse/patch";

type Product = { id: string; price: number };
const query = filter<Product>().where("price", { $lt: 20 }).build();
if (JSON.stringify(query) !== '{"price":{"$lt":20}}') throw new Error("filter wire contract");
if (sort<Product>().desc("price").toString() !== "-price") throw new Error("sort wire contract");
const page: ListResult<Product> = {
  items: [], page_info: { has_next_page: false, has_prev_page: false },
};
if (page.items.length || SCHEMA_VERSION !== 1) throw new Error("schema/page contract");
if (typeof ApiError !== "function" || typeof validatePatch !== "function") throw new Error("public subpath exports");
TS
node "$sdk/node_modules/typescript/bin/tsc" -p tsconfig.json --noEmit
node "$sdk/node_modules/@typescript/native/bin/tsc" -p tsconfig.json
node dist/index.js
# Every map resolves within the installed package, including declaration maps.
node --input-type=module <<'JS'
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
const root = path.resolve('node_modules/@ashtonian/parse');
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
assert.equal(pkg.name, '@ashtonian/parse');
assert.equal(Object.keys(pkg.dependencies ?? {}).length, 0);
for (const entry of fs.readdirSync(root, { recursive: true })) {
  assert.ok(!entry.endsWith('.test.ts'), `test source in package: ${entry}`);
}
for (const entry of fs.readdirSync(path.join(root, 'dist'), { recursive: true })) {
  if (!entry.endsWith('.map')) continue;
  const mapPath = path.join(root, 'dist', entry);
  const map = JSON.parse(fs.readFileSync(mapPath, 'utf8'));
  for (let i = 0; i < map.sources.length; i++) {
    if (map.sourcesContent?.[i] != null) continue;
    const source = path.resolve(path.dirname(mapPath), map.sourceRoot ?? '', map.sources[i]);
    assert.ok(source.startsWith(root + path.sep) && fs.existsSync(source), `missing packaged source in ${entry}`);
  }
}
console.log('Packed SDK: native and TypeScript 6 consumers, runtime subpaths and sourcemaps passed.');
JS
