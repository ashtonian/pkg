#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
scratch=$(mktemp -d)
trap 'chmod -R u+w "$scratch" 2>/dev/null; rm -rf "$scratch"' EXIT
(
  cd "$repo_root/tools/modarchive"
  GOWORK=off go run . -out "$scratch/proxy" -manifest "$repo_root/.release-please-manifest.json" \
    "$repo_root/process" "$repo_root/state" "$repo_root/onepassword"
) > "$scratch/modules.tsv"
export GOWORK=off GOTOOLCHAIN=local GOFLAGS='-mod=mod -modcacherw'
export GOMODCACHE="$scratch/modcache" GOPROXY="file://$scratch/proxy,https://proxy.golang.org"
export GOPRIVATE='' GONOPROXY='' GONOSUMDB=github.com/ashtonian/pkg/process,github.com/ashtonian/pkg/state,github.com/ashtonian/pkg/onepassword
while IFS=$'\t' read -r module version source; do
  name=${module##*/}
  mkdir "$scratch/consumer-$name"
  (
    cd "$scratch/consumer-$name"
    go mod init "example.com/$name-consumer"
    go get "$module@$version"
    go vet -tags=unit,integration "$module/..."
    go test -tags=unit,integration -race -count=1 -timeout=2m "$module/..."
    go list -m -f '{{if .Replace}}unexpected replacement{{end}}' all | awk 'NF {exit 1}'
    go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' "$module" |
      awk -v module="$module" 'NF && $0 != module && !(module == "github.com/ashtonian/pkg/onepassword" && $0 == "github.com/ashtonian/pkg/process") {print "unexpected runtime dependency: " $0 > "/dev/stderr"; bad=1} END {exit bad}'
    downloaded=$(go list -m -f '{{.Dir}}' "$module")
    awk '/^```go$/{code=1;next} /^```$/{if(code)exit} code{print}' "$downloaded/README.md" > main.go
    go run . > actual.txt
    awk '/^```text$/{output=1;next} /^```$/{if(output)exit} output{print}' "$downloaded/README.md" > expected.txt
    diff -u expected.txt actual.txt
    cp -R "$downloaded" "$scratch/tidy-$name"
    cd "$scratch/tidy-$name"
    chmod -R u+w .
    # Only the scratch copy accepts synthetic candidate dependency checksums.
    awk '$1 != "github.com/ashtonian/pkg/process" && $1 != "github.com/ashtonian/pkg/state" && $1 != "github.com/ashtonian/pkg/onepassword" {print}' go.sum > go.sum.tmp
    mv go.sum.tmp go.sum
    go mod tidy
    cmp go.mod "$source/go.mod"
  )
done < "$scratch/modules.tsv"
