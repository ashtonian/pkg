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

export GOWORK=off GOTOOLCHAIN=local GOFLAGS='-mod=mod -modcacherw'
export GOMODCACHE="$scratch/modcache" GOPROXY="file://$scratch/proxy,https://proxy.golang.org"
export GOPRIVATE= GONOPROXY= GONOSUMDB="$module"
# Real PostgreSQL/PostGIS parity is a separate, explicitly provisioned matrix.
export PARSE_POSTGRES_DSN= PARSE_POSTGIS_DSN= PARSE_INTEG_PG=
mkdir "$scratch/consumer"
cd "$scratch/consumer"
go mod init example.com/parse-consumer
go get "$module@$version"
go vet -tags=unit "$module/..."
go test -tags=unit -race -count=1 "$module/..."
go test -tags=integration -race -count=1 -timeout=15m "$module/..."
go list -deps "$module/..." | awk '/github.com\/ashtonian\/karsto/ {bad=1} END {exit bad}'
go list -m -f '{{if .Replace}}unexpected replacement{{end}}' all | awk 'NF {exit 1}'
# Generated Go SDKs must import the request builder without server dependencies.
go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' "$module/client" |
    awk -v client="$module/client" 'NF && $0 != client {print "unexpected client dependency: " $0 > "/dev/stderr"; bad=1} END {exit bad}'

downloaded=$(go list -m -f '{{.Dir}}' "$module")
awk '/^```go$/{code=1;next} /^```$/{if(code)exit} code{print}' "$downloaded/README.md" > main.go
go run . > actual.txt
awk '/^```text$/{output=1;next} /^```$/{if(output)exit} output{print}' "$downloaded/README.md" > expected.txt
diff -u expected.txt actual.txt
cp -R "$downloaded" "$scratch/tidy"
cd "$scratch/tidy"
go mod tidy -diff
