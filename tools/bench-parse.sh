#!/usr/bin/env bash
# Record the benchmarks and profiles of every parse package that has benchmarks.
#
#   tools/bench-parse.sh --list        the packages, one ./dir per line
#   tools/bench-parse.sh [out-dir]     (default parse-benchmarks)
#
# Packages are found, not listed: every test file go list selects under the
# unit tag is searched for benchmark functions, so a new benchmark is recorded
# without editing this script. CI's change scope keeps a static copy of the
# list, which tools/test_ci_parse_benchmarks.py holds to --list.
#
# go test profiles one package at a time, so each runs on its own and writes
# out-dir/<package dir>/:
#   benchmarks.txt   the -bench -benchmem output, as benchstat reads it
#   cpu.pprof        -cpuprofile
#   allocs.pprof     -memprofile: the allocations the benchmarks sampled
# The profiles carry their symbols; the test binaries are not kept.
# BENCH_COUNT (default 3) and BENCH_TIME (default 1s) set -count and -benchtime.
# Every package runs even after one fails; the script then exits 1.
set -euo pipefail

list=false
out=parse-benchmarks
case ${1:-} in
--list) list=true ;;
"") ;;
*) out=$1 ;;
esac
case $out in
/*) ;;
*) out=$PWD/$out ;;
esac
count=${BENCH_COUNT:-3}
benchtime=${BENCH_TIME:-1s}
cd "$(dirname "$0")/.."

# Every module of the repository is published at this path plus its directory.
repository=github.com/ashtonian/pkg/
# gofmt starts each top-level declaration at the beginning of a line, and a
# benchmark's name continues after Benchmark with anything but a lowercase letter.
benchmark='^func Benchmark([A-Z0-9_][[:alnum:]_]*)?\('

# Every workspace module under parse/ is listed as a pattern of its own: a
# directory pattern stops at a nested module, so ./parse/... misses the
# provider modules.
parse_modules() {
	go list -m -f '{{.Path}}' | sed -n "s|^${repository}\(parse\(/.*\)\{0,1\}\)$|./\1/...|p"
}

benchmarked() {
	# shellcheck disable=SC2046 # one pattern per module
	go list -tags=unit \
		-f '{{range .TestGoFiles}}{{$.ImportPath}} {{$.Dir}}/{{.}}{{"\n"}}{{end}}{{range .XTestGoFiles}}{{$.ImportPath}} {{$.Dir}}/{{.}}{{"\n"}}{{end}}' \
		$(parse_modules) |
		while read -r pkg file; do
			if grep -qE "$benchmark" "$file"; then
				echo "./${pkg#"$repository"}"
			fi
		done |
		LC_ALL=C sort -u
}

packages=$(benchmarked)
if [ -z "$packages" ]; then
	echo "::error::no parse package has benchmarks under the unit tag"
	exit 1
fi
if $list; then
	echo "$packages"
	exit 0
fi

if [ -n "$(ls -A "$out" 2>/dev/null)" ]; then
	echo "::error::$out is not empty; its profiles would mix with this run's"
	exit 1
fi
binaries=$(mktemp -d)
trap 'rm -rf "$binaries"' EXIT

failed=()
for pkg in $packages; do
	dir=$out/${pkg#./}
	mkdir -p "$dir"
	echo "::group::$pkg"
	if ! go test -tags=unit -run '^$' -bench . -benchmem -count="$count" -benchtime="$benchtime" \
		-cpuprofile="$dir/cpu.pprof" -memprofile="$dir/allocs.pprof" -o "$binaries/bench.test" \
		"$pkg" | tee "$dir/benchmarks.txt"; then
		failed+=("$pkg")
	fi
	echo "::endgroup::"
done

if [ ${#failed[@]} -gt 0 ]; then
	printf '::error::benchmarks failed: %s\n' "${failed[@]}"
	exit 1
fi
echo "recorded the benchmarks and profiles of $(echo "$packages" | wc -l | tr -d ' ') packages in $out"
