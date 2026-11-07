"""Unknown inputs and executable documentation always retain runtime coverage."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import ci_scope


class Scope(unittest.TestCase):
    def test_known_guides_do_not_repeat_runtime_suites(self):
        self.assertFalse(ci_scope.runtime_required(["httpx/docs/requests.md", "CONTRIBUTING.md"]))

    def test_code_configuration_fixtures_and_readmes_are_not_prose(self):
        for path in ("README.md", "httpx/README.md", "config/kongprovider/README.md",
                     "httpx/docs/example.go", "httpx/docs/testdata/input.md", "docs/new-area.md",
                     ".github/workflows/ci.yml", "Makefile", "go.work", "parse/go.sum",
                     "parse/sdk/ts/README.md", "tools/ci_scope.py", "new-package/CHANGELOG.md"):
            with self.subTest(path=path):
                self.assertTrue(ci_scope.runtime_required(["httpx/docs/requests.md", path]))
        self.assertTrue(ci_scope.runtime_required([]))
        self.assertTrue(ci_scope.runtime_required(None))

    def test_manual_dispatch_always_runs_everything(self):
        self.assertIsNone(ci_scope.changed_paths({"GITHUB_EVENT_NAME": "workflow_dispatch"}))

    def test_scope_uses_all_commits_and_both_sides_of_a_rename(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            def git(*args):
                return subprocess.check_output(["git", "-C", directory, *args], stderr=subprocess.PIPE).decode().strip()
            git("init", "-q")
            (root / "input.go").write_text("package input\n")
            git("add", ".")
            base = git("write-tree")
            (root / "httpx/docs").mkdir(parents=True)
            git("mv", "input.go", "httpx/docs/example.md")
            (root / "CONTRIBUTING.md").write_text("prose\n")
            git("add", ".")
            head = git("write-tree")
            event = root / "event.json"
            event.write_text(json.dumps({"before": base}))
            # Real Git trees exercise the full delta without creating fixture commits.
            def tree_git(*args):
                if args == ("rev-parse", "HEAD"):
                    return head + "\n"
                return subprocess.check_output(["git", "-C", directory, *[head if a == "HEAD" else a for a in args]]).decode()
            with patch.object(ci_scope, "git", side_effect=tree_git):
                paths = ci_scope.changed_paths({"GITHUB_EVENT_NAME": "push", "GITHUB_SHA": head, "GITHUB_EVENT_PATH": str(event)})
            self.assertIn("input.go", paths)
            self.assertIn("httpx/docs/example.md", paths)
            self.assertTrue(ci_scope.runtime_required(paths))

    def test_archive_examples_remain_required_for_prose(self):
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text()
        modules = workflow.split("  modules:\n", 1)[1].split("  lint:\n", 1)[0]
        self.assertIn("needs: scope", modules)
        self.assertNotIn("outputs.runtime", modules)
        self.assertIn("make release-check", modules)
        self.assertIn("make cookbook-check", modules)
        self.assertIn("make tool-check", modules)


if __name__ == "__main__":
    unittest.main()
