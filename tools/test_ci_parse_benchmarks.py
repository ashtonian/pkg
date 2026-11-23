"""The parse benchmark job records every parse package that has benchmarks.

tools/bench-parse.sh finds those packages when it runs. Which pushes run the
job is go-ci-actions scope's decision, from the job's packages in ci.yml's
scope input. That input is a literal, like every job's, so it reads as written
and these tests parse it. Generating the list in a step before the scope would
gain nothing: this test runs in the same change-scope job on every event, and
fails the change that lets the two lists differ.
"""
import subprocess
import unittest

from test_ci_release_checks import ROOT, SCOPE_JOBS, ignored, job
import ci_release_checks as ci

SCRIPT = "tools/bench-parse.sh"


class ParseBenchmarks(unittest.TestCase):
    def test_the_scope_selects_the_job_for_every_benchmarked_package(self):
        listed = subprocess.check_output([SCRIPT, "--list"], cwd=ROOT, timeout=120).decode().split()
        self.assertIn("./parse/filter", listed)
        self.assertEqual(SCOPE_JOBS["parse-benchmarks"]["go"], listed,
                         f"ci.yml's parse-benchmarks scope lists what {SCRIPT} --list prints")

    def test_the_script_selects_only_the_benchmark_job(self):
        self.assertEqual(SCOPE_JOBS["parse-benchmarks"]["paths"], [SCRIPT])
        self.assertTrue(ignored(SCRIPT))
        self.assertEqual(ci.release_checks([SCRIPT]), [])

    def test_the_job_uploads_what_the_script_records(self):
        benchmarks = job("parse-benchmarks")
        self.assertRegex(benchmarks, rf"(?m)^        run: {SCRIPT} parse-benchmarks$")
        self.assertRegex(benchmarks, r"(?m)^          path: parse-benchmarks$")


if __name__ == "__main__":
    unittest.main()
