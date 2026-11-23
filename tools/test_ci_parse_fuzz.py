"""Every parse fuzz target is one the nightly fuzz job fuzzes, and the
corpus the job carries between runs is bounded.

tools/fuzz-parse.sh lists each package's fuzz targets with `go test -list`
under the unit tag. A target in a file that tag does not select would never
be listed, and so never fuzzed, without an error. The generated corpus is
saved to the GitHub cache every night; tools/fuzz_corpus.py prunes it first.
"""
import os
import re
import tempfile
import unittest
from pathlib import Path

from fuzz_corpus import bound
from test_ci_parse_race import BUILD_LINE, parse_test_files, selected

NIGHTLY = Path(__file__).resolve().parent.parent / ".github" / "workflows" / "nightly.yml"

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


class FuzzCorpusBound(unittest.TestCase):
    def corpus(self, root, files):
        """files maps a path under root to (size, mtime)."""
        for name, (size, mtime) in files.items():
            path = Path(root, name)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(b"x" * size)
            os.utime(path, (mtime, mtime))

    def remaining(self, root):
        return sorted(str(p.relative_to(root)) for p in Path(root).rglob("*") if p.is_file())

    def test_keeps_the_newest_inputs_of_each_target(self):
        with tempfile.TemporaryDirectory() as root:
            self.corpus(root, {f"FuzzA/{i}": (1, 1000 + i) for i in range(5)} |
                        {f"filter/FuzzB/{i}": (1, 2000 + i) for i in range(2)})
            self.assertEqual(bound(root, 3, 1 << 20), (5, 5))
            self.assertEqual(self.remaining(root), ["FuzzA/2", "FuzzA/3", "FuzzA/4", "filter/FuzzB/0", "filter/FuzzB/1"])

    def test_caps_the_corpus_size_oldest_first(self):
        with tempfile.TemporaryDirectory() as root:
            self.corpus(root, {"FuzzA/old": (40, 1000), "filter/FuzzB/mid": (40, 2000), "FuzzA/new": (40, 3000)})
            self.assertEqual(bound(root, 100, 100), (2, 80))
            self.assertEqual(self.remaining(root), ["FuzzA/new", "filter/FuzzB/mid"])
            self.assertEqual(bound(root, 100, 50), (1, 40))
            self.assertEqual(self.remaining(root), ["FuzzA/new"])
            self.assertFalse(Path(root, "filter").exists(), "an emptied target directory is removed")

    def test_refuses_no_bound(self):
        with tempfile.TemporaryDirectory() as root:
            for entries, size in ((0, 1), (1, 0)):
                with self.subTest(entries=entries, size=size), self.assertRaises(ValueError):
                    bound(root, entries, size)

    def test_a_missing_corpus_is_empty(self):
        with tempfile.TemporaryDirectory() as root:
            self.assertEqual(bound(Path(root, "absent"), 1, 1), (0, 0))

    def test_nightly_bounds_the_corpus_before_saving_it(self):
        workflow = NIGHTLY.read_text()
        restore = workflow.index("actions/cache/restore@")
        prune = workflow.index("tools/fuzz_corpus.py")
        save = workflow.index("actions/cache/save@")
        self.assertLess(restore, prune)
        self.assertLess(prune, save, "the corpus is saved only once it is bounded")
        for name in ("FUZZ_CORPUS_MAX_ENTRIES", "FUZZ_CORPUS_MAX_BYTES"):
            self.assertIn(name, workflow)


if __name__ == "__main__":
    unittest.main()
