#!/usr/bin/env bash
# Validate parse and its provider modules, which release together at one
# version, through fresh consumers with no workspace replacements.
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
scratch=$(mktemp -d)
trap 'chmod -R u+w "$scratch" 2>/dev/null; rm -rf "$scratch"' EXIT
modules=(parse parse/parsepgx parse/parsesql parse/parsememory)
parse_module=github.com/ashtonian/pkg/parse
(
  cd "$repo_root/tools/modarchive"
  GOWORK=off go run . -out "$scratch/proxy" \
    -manifest "$repo_root/.release-please-manifest.json" "${modules[@]/#/$repo_root/}"
) > "$scratch/modules.tsv"

# -trimpath keeps the scratch module cache's path out of build cache keys,
# so the consumer's dependencies hit the shared build cache.
export GOWORK=off GOTOOLCHAIN=local GOFLAGS='-mod=mod -modcacherw -trimpath'
export GOMODCACHE="$scratch/modcache" GOPROXY="file://$scratch/proxy,https://proxy.golang.org"
# GOENV=off keeps the user's go env file out: an empty variable does not
# override it, and a GOPRIVATE there adds a cache-only lookup pass that
# answers a nested module's path with its cached parent. The pattern covers
# the provider modules beneath parse's path.
export GOENV=off GOPRIVATE='' GONOPROXY='' GONOSUMDB="$parse_module"
# Real PostgreSQL/PostGIS parity is a separate, explicitly provisioned matrix;
# here every PostgreSQL test skips.
export PARSE_POSTGRES_DSN='' PARSE_POSTGIS_DSN='' PARSE_REQUIRE_PG=''

# The archives describe the working tree, so their sums differ from a
# published release's; a provider module's go.sum carries none for parse,
# whose release it ships with.
without_parse_sums() {
  awk -v prefix="$parse_module" '$1 != prefix && index($1, prefix "/") != 1 { print }' "$1"
}

version=
while IFS=$'\t' read -r module module_version source; do
  if [[ -n $version && $module_version != "$version" ]]; then
    echo "parse-release-check: $module is at $module_version, parse's other modules at $version: they release at one version" >&2
    exit 1
  fi
  version=$module_version
  name=${module//\//-}
  echo "parse-release-check: $module@$version"
  (
    mkdir "$scratch/consumer-$name"
    cd "$scratch/consumer-$name"
    go mod init example.com/consumer
    go get "$module@$version"
    go vet -tags=unit "$module/..."
    go test -tags=unit -count=1 "$module/..."
    go test -tags=integration -count=1 -timeout=15m "$module/..."
    # Dependencies resolve through the public proxy alone, so a private
    # module cannot download; within the owner's namespace only this
    # repository's modules are public, and parse may depend on no other.
    go list -deps -f '{{with .Module}}{{.Path}}{{end}}' "$module/..." | sort -u |
      awk '/^github\.com\/ashtonian\// && !/^github\.com\/ashtonian\/pkg(\/|$)/ {print "dependency outside this repository: " $0 > "/dev/stderr"; bad=1} END {exit bad}'
    go list -m -f '{{if .Replace}}unexpected replacement{{end}}' all | awk 'NF {exit 1}'
    # No module or test links a cgo package: a cgo driver one test needs is
    # a requirement in every consumer's module graph and vulnerability scan.
    go list -deps -test -tags=unit,integration -f '{{if and (not .Standard) .CgoFiles}}{{.ImportPath}}{{end}}' "$module/..." | sort -u |
      awk -v module="$module" 'NF {print "cgo package in " module " or its tests: " $0 > "/dev/stderr"; bad=1} END {exit bad}'
    case $module in
    "$parse_module" | "$parse_module/parsememory")
      # parse requires no database driver, nor does the memory provider,
      # which runs no SQL: a consumer's module graph holds one only through
      # the SQL provider module it requires.
      go list -m all | awk -v module="$module" '/^(github\.com\/jackc\/|modernc\.org\/)/ {print module " requires a database driver: " $0 > "/dev/stderr"; bad=1} END {exit bad}'
      ;;
    *)
      # A SQL provider links no package of the memory provider.
      go list -deps "$module/..." | awk -v memory="$parse_module/parsememory" '$0 == memory || index($0, memory "/") == 1 {print "a SQL provider links the memory provider: " $0 > "/dev/stderr"; bad=1} END {exit bad}'
      ;;
    esac
    if [[ $module != "$parse_module" ]]; then
      # A provider built against another release of parse fails to compile,
      # naming both releases: parse's next minor release here.
      other="$scratch/parse-other-$name"
      cp -R "$(go list -m -f '{{.Dir}}' "$parse_module")" "$other"
      chmod -R u+w "$other"
      release="$other/internal/backend/release.go"
      minor=$(awk '/x-release-please-minor/ { print $4 }' "$release")
      sed -i.orig "s/^const ReleaseMinor = $minor /const ReleaseMinor = $((minor + 1)) /" "$release"
      cp go.mod go.mod.orig
      go mod edit -replace "$parse_module=$other"
      if go build "$module" 2> guard.txt; then
        echo "parse-release-check: $module built against parse at another release" >&2
        exit 1
      fi
      if ! grep -q "contract.go:.*\[[0-9]*\]\[$minor\]\[[0-9]*\]struct{}) as \[[0-9]*\]\[$((minor + 1))\]\[[0-9]*\]struct{} value" guard.txt; then
        echo "parse-release-check: $module's build against another release of parse failed without naming both releases:" >&2
        cat guard.txt >&2
        exit 1
      fi
      mv go.mod.orig go.mod
    fi

    downloaded=$(go list -m -f '{{.Dir}}' "$module")
    cp -R "$downloaded" "$scratch/tidy-$name"
    chmod -R u+w "$scratch/tidy-$name"
    cd "$scratch/tidy-$name"
    if [[ $module == "$parse_module" ]]; then
      go mod tidy -diff
    else
      without_parse_sums go.sum > go.sum.tmp
      mv go.sum.tmp go.sum
      go mod tidy
      cmp go.mod "$source/go.mod"
      diff -u <(without_parse_sums "$source/go.sum") <(without_parse_sums go.sum)
    fi
  )
done < "$scratch/modules.tsv"

# parse's README quick start runs on SQLite through parsesql.
mkdir "$scratch/quickstart"
cd "$scratch/quickstart"
go mod init example.com/parse-quickstart
go get "$parse_module@$version" "$parse_module/parsesql@$version"
readme="$(go list -m -f '{{.Dir}}' "$parse_module")/README.md"
awk '/^```go$/{code=1;next} /^```$/{if(code)exit} code{print}' "$readme" > main.go
go run . > actual.txt
awk '/^```text$/{output=1;next} /^```$/{if(output)exit} output{print}' "$readme" > expected.txt
diff -u expected.txt actual.txt
