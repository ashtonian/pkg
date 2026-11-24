#!/usr/bin/env bash
# Validate the module archive through a fresh consumer with no workspace
# replacements: the module requires nothing, so a program that imports it
# lists it alone beside the standard library.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
scratch=$(mktemp -d)
trap 'chmod -R u+w "$scratch" 2>/dev/null; rm -rf "$scratch"' EXIT
module=github.com/ashtonian/pkg/parse/client
(
  cd "$repo_root/tools/modarchive"
  GOWORK=off go run . -out "$scratch/proxy" \
    -manifest "$repo_root/.release-please-manifest.json" "$repo_root/parse/client"
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
mkdir "$scratch/consumer"
cd "$scratch/consumer"
go mod init example.com/parse-client-consumer
go get "$module@$version"
go vet -tags=unit "$module/..."
# The tests that read parse's recorded wire and corpora skip here: the
# archive does not carry parse's testdata.
go test -tags=unit -count=1 "$module/..."
go list -m -f '{{if .Replace}}unexpected replacement{{end}}' all | awk 'NF {exit 1}'
# Generated Go SDKs import the client without server code or an HTTP stack.
go list -m all | awk -v module="$module" '$1 != "example.com/parse-client-consumer" && $1 != module {print "unexpected module: " $0 > "/dev/stderr"; bad=1} END {exit bad}'
go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' "$module" |
  awk -v module="$module" 'NF && $0 != module {print "unexpected client dependency: " $0 > "/dev/stderr"; bad=1} END {exit bad}'
go list -deps "$module" | awk '$0 == "net/http" {print "the client imports net/http" > "/dev/stderr"; bad=1} END {exit bad}'

downloaded=$(go list -m -f '{{.Dir}}' "$module")
awk '/^```go$/{code=1;next} /^```$/{if(code)exit} code{print}' "$downloaded/README.md" > main.go
go run . > actual.txt
awk '/^```text$/{output=1;next} /^```$/{if(output)exit} output{print}' "$downloaded/README.md" > expected.txt
diff -u expected.txt actual.txt
cp -R "$downloaded" "$scratch/tidy"
cd "$scratch/tidy"
go mod tidy -diff
