#!/usr/bin/env bash
# Validate the module archive through a fresh consumer with no workspace replacements.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
scratch=$(mktemp -d)
trap 'chmod -R u+w "$scratch" 2>/dev/null; rm -rf "$scratch"' EXIT
module=github.com/ashtonian/pkg/parse
(
  cd "$repo_root/tools/modarchive"
  GOWORK=off go run . -out "$scratch/proxy" \
    -manifest "$repo_root/.release-please-manifest.json" "$repo_root/parse"
) > "$scratch/module.tsv"
IFS=$'\t' read -r module version source < "$scratch/module.tsv"

# -trimpath keeps the scratch module cache's path out of build cache keys,
# so the consumer's dependencies hit the shared build cache.
export GOWORK=off GOTOOLCHAIN=local GOFLAGS='-mod=mod -modcacherw -trimpath'
export GOMODCACHE="$scratch/modcache" GOPROXY="file://$scratch/proxy,https://proxy.golang.org"
# GOENV=off keeps the user's go env file out: an empty variable does not
# override it, and a GOPRIVATE there adds a cache-only lookup pass that
# answers a nested module's path with its cached parent.
export GOENV=off GOPRIVATE= GONOPROXY= GONOSUMDB="$module"
# Real PostgreSQL/PostGIS parity is a separate, explicitly provisioned matrix;
# here every PostgreSQL test skips.
export PARSE_POSTGRES_DSN= PARSE_POSTGIS_DSN= PARSE_REQUIRE_PG=
mkdir "$scratch/consumer"
cd "$scratch/consumer"
go mod init example.com/parse-consumer
go get "$module@$version"
go vet -tags=unit "$module/..."
go test -tags=unit -count=1 "$module/..."
go test -tags=integration -count=1 -timeout=15m "$module/..."
# Dependencies resolve through the public proxy alone, so a private module
# cannot download; within the owner's namespace only this repository's
# modules are public, and parse may depend on no other.
go list -deps -f '{{with .Module}}{{.Path}}{{end}}' "$module/..." | sort -u |
    awk '/^github\.com\/ashtonian\// && !/^github\.com\/ashtonian\/pkg(\/|$)/ {print "dependency outside this repository: " $0 > "/dev/stderr"; bad=1} END {exit bad}'
go list -m -f '{{if .Replace}}unexpected replacement{{end}}' all | awk 'NF {exit 1}'

downloaded=$(go list -m -f '{{.Dir}}' "$module")
awk '/^```go$/{code=1;next} /^```$/{if(code)exit} code{print}' "$downloaded/README.md" > main.go
go run . > actual.txt
awk '/^```text$/{output=1;next} /^```$/{if(output)exit} output{print}' "$downloaded/README.md" > expected.txt
diff -u expected.txt actual.txt
cp -R "$downloaded" "$scratch/tidy"
cd "$scratch/tidy"
go mod tidy -diff
