"""The release-notes check reads the commits a pull request brings to main, and
the commit override in its description, as release-please will. It checks
commits, not the tree, so it runs on pull requests alone, in a workflow of its
own: a push to main has no pull request commits to check, so ci.yml's scope
never selects it and a merge push neither reuses nor requires it; and an edit
to the description, which can change the override, reruns it without
rerunning or cancelling CI."""
from pathlib import Path
import re
import unittest

import ci_release_checks as ci

ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ROOT / ".github/workflows"
CHECK = "tools/release-please/check-commits.mjs"
NOTES = (WORKFLOWS / "release-notes.yml").read_text()
CI = (WORKFLOWS / "ci.yml").read_text()


def gate(workflow, job):
    return re.search(r"^    if: (.+)$", workflow.split(f"\n  {job}:\n", 1)[1], re.M)[1]


class ReleaseNotesCheck(unittest.TestCase):
    def test_it_runs_on_pull_requests_and_description_edits_only(self):
        triggers = NOTES.split("\non:\n", 1)[1].split("\n\n", 1)[0]
        self.assertEqual(triggers, "  pull_request:\n    types: [opened, synchronize, reopened, edited]")

    def test_it_is_no_ci_job_acceptance_records(self):
        for path in sorted(WORKFLOWS.glob("*.yml")):
            with self.subTest(workflow=path.name):
                self.assertEqual(CHECK in path.read_text(), path.name == "release-notes.yml")
        self.assertNotIn("commits", ci.JOBS)
        self.assertNotIn("release-notes.yml", CI)

    def test_it_checks_the_pull_requests_whole_range_for_the_owner(self):
        self.assertEqual(gate(NOTES, "commits"), gate(CI, "scope"))
        self.assertIn("fetch-depth: 0", NOTES)
        self.assertIn("uses: ./.github/actions/release-please-tool", NOTES)
        self.assertIn('--base "origin/$BASE_REF" --head "$HEAD_SHA" --body-file "$RUNNER_TEMP/description.md"', NOTES)
        for line in ("BASE_REF: ${{ github.base_ref }}", "HEAD_SHA: ${{ github.event.pull_request.head.sha }}",
                     "DESCRIPTION: ${{ github.event.pull_request.body }}"):
            with self.subTest(line=line):
                self.assertIn(line, NOTES)

    def test_its_tests_run_with_the_release_please_check(self):
        self.assertEqual(ci.release_checks(["tools/release-please/commit-messages.mjs"]), ["release-please-check"])
        self.assertEqual(ci.release_checks(["tools/release-please/check-commits.mjs"]), ["release-please-check"])


if __name__ == "__main__":
    unittest.main()
