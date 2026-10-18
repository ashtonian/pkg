#!/usr/bin/env bash
# Exercise the same standalone module contents a downstream caller downloads.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT
module=github.com/ashtonian/pkg/gowalker
(
  cd "$repo_root/tools/modarchive"
  GOWORK=off go run . -out "$tmp/proxy" \
    -manifest "$repo_root/.release-please-manifest.json" "$repo_root/gowalker"
) > "$tmp/module.tsv"
IFS=$'\t' read -r module version source < "$tmp/module.tsv"

export GOWORK=off GOTOOLCHAIN=local GOFLAGS='-mod=mod -modcacherw'
export GOMODCACHE="$tmp/modcache" GOPROXY="file://$tmp/proxy"
export GOPRIVATE= GONOPROXY= GONOSUMDB="$module"
mkdir "$tmp/consumer"
cd "$tmp/consumer"
go mod init example.com/gowalker-consumer
go get "$module@$version"
go vet "$module/..."
go test -race -count=1 "$module/..."
# The quick start is copied verbatim from the archive, not the working tree.
downloaded=$(go list -m -f '{{.Dir}}' "$module")
awk '/^```go$/{code=1;next} /^```$/{if(code)exit} code{print}' "$downloaded/README.md" > main.go
go run . > output.txt
test "$(cat output.txt)" = 8080
# Every usage example is a complete program paired with its expected output.
awk -v dir="$tmp" '
  /^```go$/ { n++; mode="go"; next }
  /^```text$/ { mode="txt"; next }
  /^```$/ { mode=""; next }
  mode != "" { print > (dir "/usage-" n "." mode) }
' "$downloaded/docs/usage.md"
for program in "$tmp"/usage-*.go; do
  go run "$program" > "$tmp/actual.txt"
  diff -u "${program%.go}.txt" "$tmp/actual.txt"
done
go list -m -f '{{if .Replace}}unexpected replacement{{end}}' "$module" | \
  awk 'NF {exit 1}'
cp -R "$downloaded" "$tmp/tidy"
cd "$tmp/tidy"
go mod tidy -diff
