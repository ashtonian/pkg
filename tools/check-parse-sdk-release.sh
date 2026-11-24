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
    pnpm --dir "$sdk" pack --pack-destination "$scratch" > /dev/null
    package="$scratch/ashtonian-parse-$expected_version.tgz"
fi
# A fresh project, store and metadata cache. A registry version npm has not shown pnpm yet
# is retried for up to PARSE_SDK_REGISTRY_RETRY_SECONDS, and npm's version document must
# name the bytes pnpm locked.
consumer=$(node "$repo_root/tools/check-parse-sdk-registry.mjs" install "$scratch" "$package")
cd "$consumer"
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
import { filter, or, sort, SCHEMA_VERSION } from "@ashtonian/parse";
import { ApiError } from "@ashtonian/parse/core";
import type { ListResult } from "@ashtonian/parse/filter";
import { validatePatch } from "@ashtonian/parse/patch";

type Product = { id: string; price: number };
const query = filter<Product>().where("price", { $lt: 20 }).build();
if (JSON.stringify(query) !== '{"price":{"$lt":20}}') throw new Error("filter wire contract");
if (JSON.stringify(or<Product>(query, { id: "a" })) !== '{"$or":[{"price":{"$lt":20}},{"id":"a"}]}') throw new Error("or wire contract");
if (sort<Product>().desc("price").toString() !== "-price") throw new Error("sort wire contract");
const page: ListResult<Product> = {
  items: [], page_info: { has_next_page: false, has_prev_page: false },
};
if (page.items.length || SCHEMA_VERSION !== 1) throw new Error("schema/page contract");
if (typeof ApiError !== "function" || typeof validatePatch !== "function") throw new Error("public subpath exports");
TS
node "$sdk/node_modules/typescript/bin/tsc6" -p tsconfig.json --noEmit
node "$sdk/node_modules/@typescript/native/bin/tsc" -p tsconfig.json
node dist/index.js
# The exports' default condition serves require, which loads the ES module
# on every Node the package's engines admit.
node -e 'const p = require("@ashtonian/parse"); if (typeof p.or !== "function" || typeof require("@ashtonian/parse/filter").and !== "function") throw new Error("require entry points"); if (require("@ashtonian/parse/package.json").name !== "@ashtonian/parse") throw new Error("package.json export")'
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
