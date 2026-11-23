#!/usr/bin/env bash
# Fuzz every fuzz target of a Go module's unit tests under one time budget.
#
#   tools/fuzz-parse.sh [module-dir]        (default: parse)
#
# Targets are found, not listed: each package `go test -list '^Fuzz'` names
# under the unit tag, so a new target is fuzzed without editing this script.
# The budget (FUZZ_BUDGET_SECONDS, default 900) is split evenly between them.
# Every target runs even after one fails. A failing input the fuzzer wrote to
# a package's testdata/fuzz is copied, at its path in the module, under
# FUZZ_CRASHERS (default fuzz-crashers, beside the module) and printed with
# the command that replays it; the script then exits 1.
set -euo pipefail

module=${1:-parse}
budget=${FUZZ_BUDGET_SECONDS:-900}
crashers=${FUZZ_CRASHERS:-fuzz-crashers}
min_per_target=10

case $crashers in
/*) ;;
*) crashers=$PWD/$crashers ;;
esac
cd "$module"

corpus_files() {
	find . -path '*/testdata/fuzz/*' -type f | LC_ALL=C sort
}

packages=$(go list -tags=unit -f '{{.ImportPath}} {{.Dir}}' ./...)
targets=()
while read -r pkg dir; do
	# Only packages with a fuzz function in a test file build a test binary.
	grep -qs '^func Fuzz' "$dir"/*_test.go || continue
	listed=$(go test -tags=unit -list '^Fuzz' "$pkg")
	for name in $(grep '^Fuzz' <<<"$listed" || true); do
		targets+=("$pkg $name")
	done
done <<<"$packages"

if [ ${#targets[@]} -eq 0 ]; then
	echo "::error::no fuzz targets found in $module"
	exit 1
fi
per=$((budget / ${#targets[@]}))
if [ "$per" -lt "$min_per_target" ]; then
	echo "::error::a budget of ${budget}s gives ${#targets[@]} targets ${per}s each, under ${min_per_target}s"
	exit 1
fi
echo "fuzzing ${#targets[@]} targets for ${per}s each"

before=$(corpus_files)
failed=()
for target in "${targets[@]}"; do
	read -r pkg name <<<"$target"
	echo "::group::$name ($pkg)"
	if ! go test -tags=unit -run '^$' -fuzz "^${name}\$" -fuzztime "${per}s" "$pkg"; then
		failed+=("$name ($pkg)")
	fi
	echo "::endgroup::"
done

found=$(comm -13 <(printf '%s\n' "$before") <(corpus_files) | sed '/^$/d')
if [ -n "$found" ]; then
	while read -r file; do
		file=${file#./}
		mkdir -p "$crashers/$(dirname "$file")"
		cp "$file" "$crashers/$file"
		target=$(basename "$(dirname "$file")")
		pkgdir=.
		case $file in
		*/testdata/fuzz/*) pkgdir=./${file%%/testdata/fuzz/*} ;;
		esac
		echo "::error file=$module/$file::$target failed on this input; replay with: (cd $module && go test -tags=unit -run '^$target/$(basename "$file")\$' $pkgdir)"
		echo "--- $module/$file"
		cat "$file"
	done <<<"$found"
fi
if [ ${#failed[@]} -gt 0 ]; then
	printf '::error::fuzzing failed: %s\n' "${failed[@]}"
	exit 1
fi
echo "every fuzz target ran ${per}s without a failure"
