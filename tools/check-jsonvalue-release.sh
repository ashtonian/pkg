#!/usr/bin/env bash
# Exercise the installed module and its documented API outside the workspace.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
scratch=$(mktemp -d)
trap 'chmod -R u+w "$scratch" 2>/dev/null; rm -rf "$scratch"' EXIT
module=github.com/ashtonian/pkg/jsonvalue
(
  cd "$repo_root/tools/modarchive"
  GOWORK=off go run . -out "$scratch/proxy" \
    -manifest "$repo_root/.release-please-manifest.json" "$repo_root/jsonvalue"
) > "$scratch/module.tsv"
IFS=$'\t' read -r module version source < "$scratch/module.tsv"

export GOWORK=off GOTOOLCHAIN=local GOFLAGS='-mod=mod -modcacherw'
export GOMODCACHE="$scratch/modcache" GOPROXY="file://$scratch/proxy,https://proxy.golang.org"
export GOPRIVATE= GONOPROXY= GONOSUMDB="$module"
mkdir "$scratch/consumer"
cd "$scratch/consumer"
go mod init example.com/jsonvalue-consumer
go get "$module@$version"
go vet "$module/..."
go test -race -count=1 -timeout=2m "$module/..."
go list -m -f '{{if .Replace}}unexpected replacement{{end}}' all | awk 'NF {exit 1}'
go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' "$module" |
  awk -v module="$module" 'NF && $0 != module {print "unexpected runtime dependency: " $0 > "/dev/stderr"; bad=1} END {exit bad}'

downloaded=$(go list -m -f '{{.Dir}}' "$module")
awk '/^```go$/{code=1;next} /^```$/{if(code)exit} code{print}' "$downloaded/README.md" > main.go
go run . > actual.txt
awk '/^```text$/{output=1;next} /^```$/{if(output)exit} output{print}' "$downloaded/README.md" > expected.txt
diff -u expected.txt actual.txt
cp -R "$downloaded" "$scratch/tidy"
cd "$scratch/tidy"
go mod tidy -diff
