"""Every parse fuzz target is one the nightly fuzz job fuzzes.

tools/fuzz-parse.sh lists each package's fuzz targets with `go test -list`
under the unit tag. A target in a file that tag does not select would never
be listed, and so never fuzzed, without an error.
"""
import re
import unittest

from test_ci_parse_race import BUILD_LINE, parse_test_files, selected

FUZZ_TAGS = {"unit"}
FUZZ_TARGET = re.compile(r"^func (Fuzz\w*)\(", re.M)


def fuzzed(source):
    build = BUILD_LINE.search(source)
    return build is None or selected(build.group(1), FUZZ_TAGS)


class ParseFuzzTargets(unittest.TestCase):
    def test_every_fuzz_target_builds_under_the_unit_tag(self):
        targets, unfuzzed = 0, []
        for path in parse_test_files():
            source = path.read_text()
            names = FUZZ_TARGET.findall(source)
            targets += len(names)
            if names and not fuzzed(source):
                unfuzzed.append(f"{path.name}: {', '.join(names)}")
        self.assertGreater(targets, 0)
        self.assertEqual(unfuzzed, [], "the nightly fuzz job lists targets under -tags=unit")

    def test_build_lines(self):
        for source, want in {
            "//go:build unit\n\npackage p\n": True,
            "//go:build unit || concurrency\n\npackage p\n": True,
            "//go:build integration\n\npackage p\n": False,
            "//go:build concurrency\n\npackage p\n": False,
            "package p\n": True,
        }.items():
            with self.subTest(source=source):
                self.assertEqual(fuzzed(source), want)


if __name__ == "__main__":
    unittest.main()
