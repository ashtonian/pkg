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
        self.assertEqual(ci_scope.release_checks(["parse/docs/queries.md"]), ["parse-release-check"])
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text()
        modules = workflow.split("  modules:\n", 1)[1].split("  lint:\n", 1)[0]
        self.assertIn("needs: scope", modules)
        self.assertNotIn("outputs.runtime", modules)
        self.assertIn("needs.scope.outputs.checks", modules)
        self.assertIn('make "${checks[@]}"', modules)
        makefile = (Path(__file__).resolve().parents[1] / "Makefile").read_text()
        for check in ci_scope.CHECKS:
            with self.subTest(check=check):
                self.assertIn(f"\n{check}:", makefile)
        listed = makefile.split("\nRELEASE_CHECKS := ", 1)[1].split("\n", 1)[0].split()
        self.assertEqual(listed, [c for c in ci_scope.CHECKS if c.endswith("-release-check")])

    def test_release_checks_follow_the_changed_modules(self):
        cases = {
            ("parse/internal/dialect/dialect.go",): ["parse-release-check"],
            ("parse/sdk/ts/src/client.ts", "parse/README.md"): ["parse-release-check"],
            ("httpx/codec/proto/codec.go",): ["httpx-release-check"],
            ("tools/check-httpx-release.sh",): ["httpx-release-check"],
            ("state/store.go", "onepassword/client.go"): ["native-release-check"],
            ("config/watch.go",): ["config-release-check", "cookbook-check"],
            ("config/kongprovider/load.go",): ["kongprovider-release-check", "cookbook-check"],
            ("config/examples/cookbook/main.go",): ["cookbook-check"],
            ("gitx/merge.go", "jsonvalue/value.go"): ["gitx-release-check", "jsonvalue-release-check"],
            ("tools/release-please/first-parent.mjs",): ["release-please-check"],
            ("CONTRIBUTING.md", "README.md"): [],
        }
        for paths, want in cases.items():
            with self.subTest(paths=paths):
                self.assertEqual(ci_scope.release_checks(list(paths)), want)

    def test_shared_unknown_or_unavailable_inputs_run_every_release_check(self):
        every = list(ci_scope.CHECKS)
        for path in ("Makefile", "go.work", ".github/workflows/ci.yml", "tools/ci_scope.py",
                     "tools/modarchive/main.go", ".release-please-manifest.json", "new-module/go.mod",
                     "tools/check-parse-sdk-release.sh"):
            with self.subTest(path=path):
                self.assertEqual(ci_scope.release_checks(["parse/list.go", path]), every)
        self.assertEqual(ci_scope.release_checks([]), every)
        self.assertEqual(ci_scope.release_checks(None), every)


if __name__ == "__main__":
    unittest.main()
