#!/usr/bin/env bash
# Validate the files a module consumer downloads, including nested modules.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tmp=$(mktemp -d)
trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT
modules=(httpx httpx/codec/msgpack httpx/codec/proto httpx/compress/brotli)
httpx_module=github.com/ashtonian/pkg/httpx

# The manifest holds each module's version. Before its first public release,
# 0.0.0 is a bootstrap marker and the candidate version is 0.1.0.
(
  cd "$repo_root/tools/modarchive"
  GOWORK=off go run . -out "$tmp/proxy" \
    -manifest "$repo_root/.release-please-manifest.json" \
    "${modules[@]/#/$repo_root/}"
) > "$tmp/modules.tsv"

upstream=$(go env GOPROXY)
export GOWORK=off GOFLAGS='-mod=mod -modcacherw' GOMODCACHE="$tmp/modcache"
export GOPRIVATE= GONOPROXY= GONOSUMDB="$httpx_module"
export GOPROXY="file://$tmp/proxy,$upstream"

# These ZIPs describe the working tree, so their sums can differ from an
# existing tag. Only copied modules lose these entries; tracked sums stay put.
clear_httpx_sums() {
  if [[ -f go.sum ]]; then
    awk -v prefix="$httpx_module" \
      '$1 != prefix && index($1, prefix "/") != 1 { print }' go.sum > go.sum.tmp
    mv go.sum.tmp go.sum
  fi
}

while IFS=$'\t' read -r module version source; do
  name=${module//\//-}
  consumer="$tmp/consumer-$name"
  mkdir -p "$consumer"
  echo "httpx-release-check: $module@$version"
  (
    cd "$consumer"
    go mod init example.com/consumer
    go get "$module@$version"
    go build "$module/..."
    go vet -tags=unit "$module/..."
    go test -tags=unit -race -count=1 "$module/..."
    go vet -tags=integration "$module/..."
    go test -tags=integration -race -count=1 "$module/..."

    # Tidy a copy of the downloaded module. Its go.mod must already be tidy;
    # synthetic working-tree checksums must not be written into the checkout.
    downloaded=$(go list -m -f '{{.Dir}}' "$module")
    cp -R "$downloaded" "$tmp/tidy-$name"
    chmod -R u+w "$tmp/tidy-$name"
    cd "$tmp/tidy-$name"
    clear_httpx_sums
    go mod tidy
    cmp go.mod "$source/go.mod"
  )
done < "$tmp/modules.tsv"

for example in examples/gin internal/bench; do
  name=${example//\//-}
  tags=integration
  if [[ $example == examples/gin ]]; then
    tags=unit,integration
  fi
  cp -R "$repo_root/httpx/$example" "$tmp/$name"
  echo "httpx-release-check: $example"
  (
    cd "$tmp/$name"
    # Benchmarks normally measure the local tree. This check exercises their
    # code against the same archives as a downstream consumer instead.
    go mod edit -dropreplace "$httpx_module"
    clear_httpx_sums
    go mod tidy
    go build -tags="$tags" ./...
    go vet -tags="$tags" ./...
    if [[ $example == internal/bench ]]; then
      # Execute each benchmark once to catch broken fixtures. These timings
      # are smoke checks, not comparable performance measurements.
      go test -tags=integration -race -count=1 -run '^$' -bench . -benchtime=1x ./...
    else
      go test -tags="$tags" -race -count=1 ./...
    fi
  )
done
