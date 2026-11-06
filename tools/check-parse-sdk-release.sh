#!/usr/bin/env bash
# Exercise the public SDK outside its source tree and dependency graph.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
sdk=$(cd -- "${PARSE_SDK_DIR:-$repo_root/parse/sdk/ts}" && pwd)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
if [[ $# == 2 && $1 == --registry && $2 =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    expected_version=$2
    package="@ashtonian/parse@$expected_version"
    registry=true
elif [[ $# -le 1 ]]; then
    expected_version=$(node -p 'JSON.parse(require("node:fs").readFileSync(process.argv[1], "utf8")).version' "$sdk/package.json")
    registry=false
else
    echo 'usage: check-parse-sdk-release.sh [tarball | --registry version]' >&2
    exit 2
fi
if [[ $registry == false && ! -f "$sdk/dist/index.js" ]]; then
    echo 'parse-sdk-release-check: run make test-parse-sdk to build the SDK first' >&2
    exit 1
fi
if [[ $registry == false && $# == 1 ]]; then
    package=$(cd -- "$(dirname -- "$1")" && pwd)/$(basename -- "$1")
elif [[ $registry == false ]]; then
    (cd "$sdk" && npm pack --json --pack-destination "$scratch") > "$scratch/pack.json"
    package="$scratch/$(node --input-type=module -e 'import fs from "node:fs"; console.log(JSON.parse(fs.readFileSync(process.argv[1], "utf8"))[0].filename)' "$scratch/pack.json")"
fi
mkdir "$scratch/consumer"
cd "$scratch/consumer"
cat > package.json <<'JSON'
{"private":true,"type":"module"}
JSON
npm install --ignore-scripts --no-audit --no-fund --cache "$scratch/npm-cache" \
    --registry=https://registry.npmjs.org --fetch-timeout=30000 --fetch-retries=1 "$package"
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
node --input-type=module - "$expected_version" "$registry" <<'JS'
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
const root = path.resolve('node_modules/@ashtonian/parse');
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
assert.equal(pkg.name, '@ashtonian/parse');
assert.equal(pkg.version, process.argv[2]);
assert.equal(Object.keys(pkg.dependencies ?? {}).length, 0);
if (process.argv[3] === 'true') {
  const lock = JSON.parse(fs.readFileSync('package-lock.json', 'utf8'));
  const installed = lock.packages['node_modules/@ashtonian/parse'];
  assert.equal(new URL(installed.resolved).origin, 'https://registry.npmjs.org');
  assert.equal(installed.version, pkg.version);
  assert.ok(installed.integrity?.startsWith('sha512-'), 'registry package integrity');
}
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
console.log(`SDK ${pkg.version}: native and TypeScript 6 consumers, runtime subpaths and sourcemaps passed (${process.argv[3] === 'true' ? 'public registry' : 'tarball'}).`);
JS
