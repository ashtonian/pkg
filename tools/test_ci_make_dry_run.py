"""Tests read the Makefile's recipes as written, whatever the job exports.

ci.yml exports UNIT_TEST_COUNT to every job, -count=1 on the nightly run, and
make lets the environment override any ?= variable. A test that ran make with
the job's environment read the nightly's recipe and failed there alone.
"""
import os
import subprocess
import unittest
from unittest.mock import patch

import make_dry_run

# Values unlike every default, as a nightly job or a developer's shell exports them.
EXPORTED = {
    "UNIT_TEST_COUNT": "-count=1",
    "PACKAGES": "github.com/ashtonian/pkg/gowalker",
    "LINT_ALL": "1",
    "MAKEFLAGS": "PACKAGES=github.com/ashtonian/pkg/gowalker",
}


class MakeDryRun(unittest.TestCase):
    def test_the_overridable_variables_come_from_the_makefile(self):
        names = make_dry_run.defaulted_variables(make_dry_run.MAKEFILE.read_text())
        self.assertLessEqual({"PACKAGES", "UNIT_TEST_COUNT", "LINT_ALL"}, names)
        self.assertNotIn("RACE", names, "a := assignment ignores the environment")

    def test_an_exported_variable_does_not_reach_the_recipe(self):
        clean = make_dry_run.dry_run("test-parse-unit", "lint")
        with patch.dict(os.environ, EXPORTED):
            # Without the helper the exports change what make prints, so the
            # comparison below holds the helper, not an unread variable.
            leaked = subprocess.check_output(["make", "-s", "-n", "test-parse-unit", "lint"],
                                             cwd=make_dry_run.ROOT, timeout=60, text=True)
            self.assertNotEqual(leaked, clean)
            self.assertEqual(make_dry_run.dry_run("test-parse-unit", "lint"), clean)

    def test_a_named_variable_still_applies(self):
        with patch.dict(os.environ, EXPORTED):
            recipe = make_dry_run.dry_run("test-parse-unit", UNIT_TEST_COUNT="-count=2")
        self.assertIn("go test -count=2 -tags=unit ./parse/...", recipe)

    def test_environment_keeps_what_make_does_not_read_as_a_default(self):
        base = {"PATH": "/usr/bin", "HOME": "/home/x", **EXPORTED}
        self.assertEqual(make_dry_run.environment(base), {"PATH": "/usr/bin", "HOME": "/home/x"})


if __name__ == "__main__":
    unittest.main()
