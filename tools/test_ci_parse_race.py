"""Every parse test that runs code concurrently runs under race on pull requests.

Parse starts no goroutines of its own, so only tests that start goroutines or
call t.Parallel can fail under the race detector. Pull requests run parse's
unit tests without race, then `make test-parse-race`: race over the files the
concurrency build tag selects. The integration lane always runs under race.
A unit-only file with concurrent tests and no concurrency tag would be raced
only by the nightly run.
"""
from pathlib import Path
import re
import subprocess
import unittest

import make_dry_run

ROOT = Path(__file__).resolve().parents[1]
RACE_LANE_TAG = "concurrency"
# Tags of the lanes pull requests run under race.
RACED_LANES = ({RACE_LANE_TAG}, {"integration"})
CONCURRENT = re.compile(r"\.Parallel\(\)|\.Go\(|\bgo\s+(?:func\b|[A-Za-z_][\w.]*\()")
BUILD_LINE = re.compile(r"^//go:build (.+)$", re.M)
TOKEN = re.compile(r"\s*(\(|\)|&&|\|\||!|[A-Za-z_][\w.]*)")
OPERATORS = {"(": "(", ")": ")", "&&": " and ", "||": " or ", "!": " not "}


def selected(constraint, tags):
    """Whether a //go:build expression holds for tags; other tags are unset."""
    words, rest = [], constraint.strip()
    while rest:
        match = TOKEN.match(rest)
        if not match:
            raise ValueError(f"unparsed build constraint: {constraint!r}")
        token = match.group(1)
        words.append(OPERATORS.get(token) or str(token in tags))
        rest = rest[match.end():].strip()
    # Only True, False, and, or, not and parentheses reach eval.
    return eval("".join(words), {"__builtins__": {}})


def runs_concurrently(source):
    code = (line for line in source.splitlines() if not line.lstrip().startswith("//"))
    return any(CONCURRENT.search(line) for line in code)


def raced_on_pull_requests(source):
    build = BUILD_LINE.search(source)
    return build is None or any(selected(build.group(1), tags) for tags in RACED_LANES)


def parse_test_files():
    out = subprocess.check_output(["git", "-C", str(ROOT), "ls-files", "-z", "--", "parse/*_test.go"], timeout=30)
    return [ROOT / name for name in out.decode().split("\0") if name]


class ParseRaceLane(unittest.TestCase):
    def test_concurrent_unit_tests_carry_the_race_lane_tag(self):
        files = parse_test_files()
        self.assertTrue(files)
        missing = [str(path.relative_to(ROOT)) for path in files
                   if runs_concurrently(path.read_text()) and not raced_on_pull_requests(path.read_text())]
        self.assertEqual(missing, [], f"add `|| {RACE_LANE_TAG}` to their //go:build lines")

    def test_the_race_target_builds_the_tagged_files(self):
        recipe = make_dry_run.dry_run("test-parse-race", timeout=30, PACKAGES="all").split()
        self.assertIn("-race", recipe)
        self.assertIn(f"-tags={RACE_LANE_TAG}", recipe)

    def test_concurrency_is_found_in_code_not_comments(self):
        for source, want in {
            "wg.Go(func() {": True,
            "\tgo func() { done <- run() }()": True,
            "\tgo worker(ctx)": True,
            "\tt.Parallel()": True,
            "\t// wg.Go and t.Parallel() are not used here": False,
            "\tcategory := \"go fish\"": False,
        }.items():
            with self.subTest(source=source):
                self.assertEqual(runs_concurrently(source), want)

    def test_build_constraints(self):
        for constraint, tags, want in (
            ("unit", {"concurrency"}, False),
            ("unit || concurrency", {"concurrency"}, True),
            ("unit || integration", {"integration"}, True),
            ("unit && !race", {"unit"}, True),
            ("(unit || concurrency) && linux", {"concurrency"}, False),
        ):
            with self.subTest(constraint=constraint, tags=tags):
                self.assertEqual(selected(constraint, tags), want)
        self.assertTrue(raced_on_pull_requests("package parse_test\n"))
        self.assertFalse(raced_on_pull_requests("//go:build unit\n\npackage parse_test\n"))
        with self.assertRaises(ValueError):
            selected("unit + concurrency", {"unit"})


if __name__ == "__main__":
    unittest.main()
