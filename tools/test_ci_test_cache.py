"""Unit tests replay passing results from the build cache; every other go test reruns.

go test keys a cached result on the test binary and on the environment
variables, files and directories the test process read inside its module. The
Makefile's unit targets, whose tests read nothing else, take UNIT_TEST_COUNT:
empty replays, -count=1 reruns. Every other go test, in the Makefile, the
release-check scripts or ci.yml, passes -count=1: its tests start processes,
run the installed git, reach servers or databases, or test a module archive.
The nightly run sets UNIT_TEST_COUNT=-count=1, so each main commit gets one
uncached run.
"""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]
MAKEFILE = (ROOT / "Makefile").read_text()
WORKFLOW = (ROOT / ".github/workflows/ci.yml").read_text()
COUNT = "$(UNIT_TEST_COUNT)"
RERUN = "-count=1"
NIGHTLY_COUNT = "UNIT_TEST_COUNT: ${{ inputs.nightly && '-count=1' || '' }}"


def code_lines(text, comment):
    return [line for line in text.splitlines() if not line.lstrip().startswith(comment)]


def rules():
    """Each Makefile rule's recipe, by target."""
    found, target = {}, None
    for line in MAKEFILE.splitlines():
        rule = re.match(r"^([\w.-]+):(?![:=])", line)
        if rule:
            target = rule[1]
            found[target] = ""
        elif line.startswith("\t") and target:
            found[target] += line + "\n"
        elif line.strip() and not line.startswith("#"):
            target = None
    return found


def dry_run(*targets, count=None):
    """The go test commands make would run for targets over every package."""
    args = ["make", "-s", "-n", *targets, "PACKAGES=all"]
    if count is not None:
        args.append(f"UNIT_TEST_COUNT={count}")
    out = subprocess.check_output(args, cwd=ROOT, timeout=60, text=True)
    return [line for line in out.splitlines() if line.startswith("go test")]


class TestCache(unittest.TestCase):
    def test_every_makefile_go_test_states_whether_it_replays(self):
        lines = [line for line in code_lines(MAKEFILE, "#") if "go test" in line]
        self.assertTrue(lines)
        for line in lines:
            with self.subTest(line=line.strip()):
                self.assertNotEqual(COUNT in line, RERUN in line,
                                    f"pass {COUNT} when the tests read only their package's files, else {RERUN}")

    def test_unit_targets_replay_unless_told_to_rerun(self):
        unit = sorted(target for target, recipe in rules().items() if COUNT in recipe)
        self.assertIn("test-parse-unit", unit)
        for target in unit:
            with self.subTest(target=target):
                replayed, rerun = dry_run(target, count=""), dry_run(target, count=RERUN)
                self.assertTrue(replayed)
                self.assertTrue(all("-count" not in line.split() for line in replayed), replayed)
                self.assertTrue(all(RERUN in line.split() for line in rerun), rerun)

    def test_tests_with_outside_state_always_rerun(self):
        recipes = rules()
        # gitx's tests run the installed git, whose version no cache key holds.
        self.assertIn(RERUN, recipes["test-gitx"])
        for target, recipe in recipes.items():
            if re.search(r",integration\)", recipe):
                with self.subTest(target=target):
                    self.assertIn(RERUN, recipe)
                    self.assertNotIn(COUNT, recipe)

    def test_make_test_reruns_everything_at_the_nightly_count(self):
        commands = dry_run("test", count=RERUN)
        self.assertTrue(commands)
        for command in commands:
            with self.subTest(command=command):
                self.assertIn(RERUN, command.split())

    def test_release_checks_and_archive_consumers_rerun(self):
        scripts = sorted((ROOT / "tools").glob("check-*.sh"))
        self.assertTrue(scripts)
        for script in scripts:
            for line in code_lines(script.read_text(), "#"):
                if re.search(r"\bgo test\b", line):
                    with self.subTest(script=script.name, line=line.strip()):
                        self.assertIn(RERUN, line)

    def test_the_nightly_reruns_unit_tests(self):
        top = WORKFLOW.split("\njobs:\n", 1)[0]
        env = top.split("\nenv:\n", 1)[1]
        self.assertIn(f"\n  {NIGHTLY_COUNT}\n", "\n" + env + "\n")
        # Set once for every job: a job-level value would hide the nightly's.
        self.assertEqual(WORKFLOW.count("UNIT_TEST_COUNT"), 1)
        # go clean -testcache expires results by the time the cache reports for
        # each, and the remote build cache reports when it copied the result
        # into the job, so the nightly would replay what it means to rerun.
        cleans = [line for line in code_lines(WORKFLOW, "#") if "go clean -testcache" in line]
        self.assertEqual(cleans, [])

    def test_workflow_go_tests_rerun(self):
        lines = [line for line in code_lines(WORKFLOW, "#") if re.search(r"\bgo test\b", line)]
        for line in lines:
            with self.subTest(line=line.strip()):
                self.assertIn(RERUN, line)


if __name__ == "__main__":
    unittest.main()
