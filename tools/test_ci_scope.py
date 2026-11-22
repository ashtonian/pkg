"""CI runs what a change reaches, and everything when the reach is unknown."""
import contextlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import ci_scope

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / ".github/workflows/ci.yml").read_text()
MAKEFILE = (ROOT / "Makefile").read_text()


def job(name):
    """The text of one ci.yml job."""
    return re.split(r"\n  (?=[a-z-]+:\n)", WORKFLOW.split(f"\n  {name}:\n", 1)[1], maxsplit=1)[0]


def make_list(name):
    words = MAKEFILE.split(f"\n{name} := ", 1)[1].split("\n", 1)[0].split()
    return [w for v in words for w in (make_list(v[2:-1]) if v.startswith("$(") else [v])]


def write(root, files):
    for name, text in files.items():
        (root / name).parent.mkdir(parents=True, exist_ok=True)
        (root / name).write_text(text)


def run_git(root, *args):
    env = {**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
           "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com"}
    return subprocess.check_output(["git", "-C", str(root), "-c", "commit.gpgsign=false", *args],
                                   env=env, stderr=subprocess.PIPE).decode().strip()


# A workspace of two modules plus one outside go.work. app's build imports lib,
# its tests import lib/leaf, and only its Darwin build imports lib/darwinonly.
FIXTURE = {
    "go.work": "go 1.22\n\nuse (\n\t./lib\n\t./app\n)\n",
    "lib/go.mod": "module example.com/lib\n\ngo 1.22\n",
    "lib/lib.go": "package lib\n\nfunc Name() string { return \"lib\" }\n",
    "lib/lib_test.go": "package lib\n\nimport \"testing\"\n\nfunc TestName(t *testing.T) { _ = Name() }\n",
    "lib/leaf/leaf.go": "package leaf\n\nconst Leaf = 1\n",
    "lib/darwinonly/darwinonly.go": "package darwinonly\n\nconst On = true\n",
    "lib/tagged/tagged.go": "package tagged\n\nconst Tagged = 1\n",
    "lib/README.md": "lib\n",
    "app/go.mod": "module example.com/app\n\ngo 1.22\n",
    "app/app.go": "package app\n\nimport \"example.com/lib\"\n\nvar Name = lib.Name()\n",
    "app/watch_darwin.go": "package app\n\nimport \"example.com/lib/darwinonly\"\n\nvar _ = darwinonly.On\n",
    "app/app_test.go": "package app\n\nimport (\n\t\"testing\"\n\n\t\"example.com/lib/leaf\"\n)\n\nfunc TestApp(t *testing.T) { _ = leaf.Leaf }\n",
    "app/integration_test.go": "//go:build integration\n\npackage app\n\nimport (\n\t\"testing\"\n\n\t\"example.com/lib/tagged\"\n)\n\nfunc TestTagged(t *testing.T) { _ = tagged.Tagged }\n",
    "app/cmd/tool/main.go": "package main\n\nfunc main() {}\n",
    "app/testdata/case.json": "{}\n",
    "dev/go.mod": "module example.com/dev\n\ngo 1.22\n",
    "dev/dev.go": "package dev\n",
}


class Fixture(unittest.TestCase):
    """The package graph of a real Go workspace."""

    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory()
        cls.root = Path(cls.directory.name)
        write(cls.root, FIXTURE)
        run_git(cls.root, "init", "-q")
        run_git(cls.root, "add", ".")
        cls.workspace = ci_scope.Workspace.load(cls.root)

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def reached(self, *paths):
        return self.workspace.reached(*ci_scope.changes(list(paths), self.workspace)[:2])

    def test_workspace_modules_and_packages(self):
        self.assertEqual(self.workspace.modules, {"lib", "app"})
        self.assertEqual(self.workspace.all_modules, {"lib", "app", "dev"})
        self.assertEqual(self.workspace.packages["lib/leaf"], "lib")
        self.assertEqual(self.workspace.packages["app/cmd/tool"], "app")

    def test_a_package_reaches_its_importers_builds_and_tests(self):
        self.assertEqual(self.reached("lib/lib.go"), {"lib", "app"})
        self.assertEqual(self.reached("lib/leaf/leaf.go"), {"lib/leaf", "app"})
        self.assertEqual(self.reached("app/app.go"), {"app"})

    def test_a_test_file_reaches_only_its_package(self):
        self.assertEqual(self.reached("lib/lib_test.go"), {"lib"})

    def test_every_platform_and_tag_set_links(self):
        self.assertEqual(self.reached("lib/darwinonly/darwinonly.go"), {"lib/darwinonly", "app"})
        self.assertEqual(self.reached("lib/tagged/tagged.go"), {"lib/tagged", "app"})

    def test_other_files_reach_their_whole_module(self):
        self.assertEqual(self.reached("app/testdata/case.json"), {"app", "app/cmd/tool"})
        self.assertEqual(self.reached("lib/README.md"), {"lib", "lib/leaf", "lib/darwinonly", "lib/tagged", "app"})

    def test_modules_outside_the_workspace_and_prose_reach_no_package(self):
        self.assertEqual(self.reached("dev/dev.go", "CONTRIBUTING.md"), set())

    def test_manifests_and_unknown_paths_are_unscoped(self):
        for path in ("lib/go.mod", "app/go.sum", "go.work", "Makefile", "new.txt"):
            with self.subTest(path=path):
                with self.assertRaises(ci_scope.Unscoped):
                    ci_scope.changes([path], self.workspace)

    def test_a_package_whose_imports_do_not_parse_is_unscoped(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write(root, {**FIXTURE, "lib/broken/broken.go": "package broken\n\nimport (\n\t\"fmt\"\n"})
            run_git(root, "init", "-q")
            run_git(root, "add", ".")
            with self.assertRaises(ci_scope.Unscoped):
                ci_scope.Workspace.load(root)

    def test_an_outside_module_requiring_the_workspace_is_unscoped(self):
        # Its packages could import workspace packages behind the listing's back.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            write(root, {**FIXTURE,
                         "go.work": FIXTURE["go.work"] + "\nreplace example.com/ext v1.0.0 => ./ext\n",
                         "app/go.mod": "module example.com/app\n\ngo 1.22\n\nrequire example.com/ext v1.0.0\n",
                         "ext/go.mod": "module example.com/ext\n\ngo 1.22\n\nrequire example.com/lib v1.0.0\n",
                         "ext/ext.go": "package ext\n"})
            run_git(root, "init", "-q")
            run_git(root, "add", ".")
            with self.assertRaisesRegex(ci_scope.Unscoped, "example.com/ext"):
                ci_scope.Workspace.load(root)


class Repository(unittest.TestCase):
    def test_the_repository_workspace_is_scopable(self):
        # Otherwise every run would silently fall back to everything.
        workspace = ci_scope.Workspace.load(ROOT)
        self.assertEqual(workspace.modules, set(make_list("MODULES")))
        for name in ("DARWIN_MODULES", "MINIMUM_GO_MODULES", "POSTGRES_MODULES"):
            with self.subTest(name=name):
                self.assertLessEqual(set(getattr(ci_scope, name)), workspace.modules)
        self.assertIn(ci_scope.BENCHMARKED, workspace.packages)


def workspace(**packages):
    """A Workspace whose packages link only themselves and the listed packages."""
    modules = {"parse", "config", "config/kongprovider", "httpx", "gitx", "process", "gowalker"}
    owners = {name: next(m for m in sorted(modules, key=len, reverse=True) if name == m or name.startswith(m + "/"))
              for name in packages}
    links = {name: {name, *linked} for name, linked in packages.items()}
    return ci_scope.Workspace(modules, modules | {"config/examples/cookbook", "tools/modarchive"}, owners, links)


GRAPH = workspace(**{"parse": ["parse/filter", "gowalker"], "parse/filter": ["gowalker"],
                     "config": ["gowalker"], "config/kongprovider": ["config"], "httpx": [], "gitx": [],
                     "process": [], "gowalker": []})


class Plan(unittest.TestCase):
    def plan(self, *paths):
        return ci_scope.plan(list(paths), lambda: GRAPH)

    def selected(self, *paths):
        return {name for name, on in self.plan(*paths)["jobs"].items() if on}

    def test_jobs_follow_the_modules_a_change_reaches(self):
        cases = {
            ("parse/filter/sql.go",): {"test", "parse-postgres", "modules", "lint", "parse-benchmarks"},
            ("parse/list.go",): {"test", "parse-postgres", "modules", "lint"},
            ("config/watch.go",): {"test", "test-darwin", "modules", "lint"},
            ("process/run.go",): {"test", "test-darwin", "modules", "lint"},
            ("httpx/client.go",): {"test", "minimum-go", "modules", "lint"},
            ("gitx/merge.go",): {"test", "minimum-go", "modules", "lint"},
            ("gowalker/walk.go",): {"test", "test-darwin", "parse-postgres", "modules", "lint", "parse-benchmarks"},
            ("parse/sdk/ts/src/client.ts",): {"parse-sdk", "modules"},
            ("tools/check-parse-sdk-release.sh",): {"parse-sdk"},
            ("parse/testdata/sdk_wire.json",): {"test", "parse-postgres", "parse-sdk", "modules", "lint", "parse-benchmarks"},
            ("config/examples/cookbook/main.go",): {"modules", "lint"},
            ("tools/check-httpx-release.sh",): {"modules"},
            # Every release check runs, and the cookbook lints with its check.
            ("tools/modarchive/main.go",): {"modules", "lint"},
            ("tools/release-please/run.mjs",): {"modules"},
            ("CONTRIBUTING.md", "README.md"): set(),
            ("parse/docs/queries.md",): {"modules"},
        }
        for paths, want in cases.items():
            with self.subTest(paths=paths):
                self.assertEqual(self.selected(*paths), want)

    def test_packages_name_their_module(self):
        self.assertEqual(self.plan("config/watch.go")["packages"], "config:config config/kongprovider:config/kongprovider")
        self.assertEqual(self.plan("parse/sdk/ts/src/client.ts")["packages"], "")

    def test_an_unknown_delta_is_everything(self):
        every = ci_scope.everything()
        self.assertEqual(ci_scope.plan(None, lambda: GRAPH), every)
        self.assertEqual(ci_scope.plan([], lambda: GRAPH), every)
        self.assertEqual(every["packages"], ci_scope.EVERYTHING)
        self.assertTrue(all(every["jobs"].values()))
        self.assertEqual(every["checks"], list(ci_scope.CHECKS))

    def test_shared_inputs_are_unscoped(self):
        for path in ("Makefile", "go.work", "go.work.sum", ".github/workflows/ci.yml", ".github/actions/release-please-tool/action.yml",
                     "tools/ci_scope.py", "parse/go.mod", "httpx/go.sum", ".release-please-manifest.json", "LICENSE", "new-module/go.mod"):
            with self.subTest(path=path):
                with self.assertRaises(ci_scope.Unscoped):
                    self.plan("parse/list.go", path)

    def test_main_falls_back_to_everything(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory, "output")
            env = {"GITHUB_OUTPUT": str(output), "GITHUB_EVENT_NAME": "workflow_dispatch"}
            with patch.dict(os.environ, env, clear=True), contextlib.redirect_stdout(io.StringIO()):
                ci_scope.main()
            values = dict(line.split("=", 1) for line in output.read_text().splitlines())
            self.assertEqual(json.loads(values["jobs"]), dict.fromkeys(ci_scope.JOBS, True))
            self.assertEqual(values["packages"], "all")
            output.write_text("")
            with patch.dict(os.environ, {**env, "GITHUB_EVENT_NAME": "pull_request"}, clear=True), \
                    patch.object(ci_scope, "changed_paths", return_value=["parse/list.go"]), \
                    patch.object(ci_scope.Workspace, "load", side_effect=subprocess.CalledProcessError(1, "go")), \
                    contextlib.redirect_stdout(io.StringIO()):
                ci_scope.main()
            values = dict(line.split("=", 1) for line in output.read_text().splitlines())
            self.assertEqual(values["packages"], "all")


class ReleaseChecks(unittest.TestCase):
    def test_archive_examples_remain_required_for_prose(self):
        self.assertEqual(ci_scope.release_checks(["parse/docs/queries.md"]), ["parse-release-check"])
        modules = job("modules")
        self.assertIn("fromJSON(needs.scope.outputs.jobs).modules", modules)
        self.assertIn("needs.scope.outputs.checks", modules)
        self.assertIn('make "${checks[@]}"', modules)
        for check in ci_scope.CHECKS:
            with self.subTest(check=check):
                self.assertIn(f"\n{check}:", MAKEFILE)
        listed = MAKEFILE.split("\nRELEASE_CHECKS := ", 1)[1].split("\n", 1)[0].split()
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
            ("tools/check-parse-sdk-release.sh",): [],
            ("CONTRIBUTING.md", "README.md"): [],
        }
        for paths, want in cases.items():
            with self.subTest(paths=paths):
                self.assertEqual(ci_scope.release_checks(list(paths)), want)

    def test_shared_unknown_or_unavailable_inputs_run_every_release_check(self):
        every = list(ci_scope.CHECKS)
        for path in ("Makefile", "go.work", ".github/workflows/ci.yml", "tools/ci_scope.py",
                     "tools/modarchive/main.go", ".release-please-manifest.json", "new-module/go.mod"):
            with self.subTest(path=path):
                self.assertEqual(ci_scope.release_checks(["parse/list.go", path]), every)
        self.assertEqual(ci_scope.release_checks([]), every)
        self.assertEqual(ci_scope.release_checks(None), every)

    def test_prose(self):
        for path in ("CONTRIBUTING.md", "README.md", "httpx/docs/requests.md", "parse/docs/queries.md"):
            self.assertTrue(ci_scope.prose(path), path)
        for path in ("httpx/README.md", "config/kongprovider/README.md", "httpx/docs/example.go",
                     "httpx/docs/testdata/input.md", "docs/new-area.md", "new-package/CHANGELOG.md"):
            self.assertFalse(ci_scope.prose(path), path)


class Delta(unittest.TestCase):
    """The base a run compares against."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        run_git(self.root, "init", "-q", "-b", "main")
        write(self.root, {"input.go": "package input\n"})
        run_git(self.root, "add", ".")
        run_git(self.root, "commit", "-q", "-m", "one")
        self.first = run_git(self.root, "rev-parse", "HEAD")
        (self.root / "httpx/docs").mkdir(parents=True)
        run_git(self.root, "mv", "input.go", "httpx/docs/example.md")
        run_git(self.root, "commit", "-q", "-m", "two")
        self.second = run_git(self.root, "rev-parse", "HEAD")
        write(self.root, {"CONTRIBUTING.md": "prose\n"})
        run_git(self.root, "add", ".")
        run_git(self.root, "commit", "-q", "-m", "three")
        self.head = run_git(self.root, "rev-parse", "HEAD")
        cwd = os.getcwd()
        os.chdir(self.root)
        self.addCleanup(os.chdir, cwd)
        self.runs, self.queries = [], []

    def push(self, run_id="99"):
        outer = self

        class API:
            def get(self, path, **query):
                outer.queries.append((path, query))
                return {"workflow_runs": outer.runs}
        env = {"GITHUB_EVENT_NAME": "push", "GITHUB_SHA": self.head, "GITHUB_RUN_ID": run_id}
        return ci_scope.changed_paths(env, API())

    def test_manual_dispatch_always_runs_everything(self):
        self.assertIsNone(ci_scope.changed_paths({"GITHUB_EVENT_NAME": "workflow_dispatch"}))

    def test_push_compares_against_the_last_green_push_with_both_sides_of_a_rename(self):
        self.runs = [{"id": 1, "run_number": 1, "head_sha": self.first}]
        self.assertEqual(sorted(self.push()), ["CONTRIBUTING.md", "httpx/docs/example.md", "input.go"])
        self.assertEqual(self.queries, [("actions/workflows/ci.yml/runs",
                                         {"branch": "main", "event": "push", "status": "success", "per_page": 30})])
        self.runs.append({"id": 2, "run_number": 2, "head_sha": self.second})
        self.assertEqual(self.push(), ["CONTRIBUTING.md"])

    def test_push_skips_runs_off_main_history_and_its_own_attempts(self):
        self.runs = [{"id": 1, "run_number": 1, "head_sha": self.first},
                     {"id": 3, "run_number": 3, "head_sha": "f" * 40},
                     {"id": 99, "run_number": 4, "head_sha": self.head}]
        self.assertEqual(sorted(self.push()), ["CONTRIBUTING.md", "httpx/docs/example.md", "input.go"])

    def test_push_without_a_green_ancestor_runs_everything(self):
        self.runs = [{"id": 3, "run_number": 3, "head_sha": "f" * 40}]
        self.assertIsNone(self.push())
        self.runs = []
        self.assertIsNone(self.push())


class Wiring(unittest.TestCase):
    def test_every_validation_job_follows_the_scope(self):
        for name in ci_scope.VALIDATION_JOBS + ("parse-benchmarks",):
            with self.subTest(job=name):
                text = job(name)
                self.assertTrue(f"fromJSON(needs.scope.outputs.jobs)['{name}']" in text
                                or f"fromJSON(needs.scope.outputs.jobs).{name} " in text)
        accepted = job("accepted")
        self.assertIn("needs: [" + ", ".join(("scope",) + ci_scope.VALIDATION_JOBS) + "]", accepted)

    def test_go_jobs_cover_only_the_packages_reached(self):
        for name in ("test", "test-darwin", "parse-postgres", "lint"):
            with self.subTest(job=name):
                self.assertIn("PACKAGES: ${{ needs.scope.outputs.packages }}", job(name))
        recipes = [line for line in MAKEFILE.splitlines() if line.startswith("\t")]
        self.assertEqual([line for line in recipes if re.match(r"\tgo (test|vet|run)", line)], [])

    def test_job_modules_are_the_makefile_lists(self):
        self.assertEqual(list(ci_scope.DARWIN_MODULES), make_list("NATIVE_MODULES") + make_list("CONFIG_MODULES"))
        self.assertEqual(list(ci_scope.MINIMUM_GO_MODULES), make_list("HTTPX_MODULES") + ["gitx"])
        self.assertEqual(list(ci_scope.POSTGRES_MODULES), make_list("PARSE_MODULES"))
        darwin = re.findall(r"run: make (\S+)", job("test-darwin"))
        self.assertEqual(darwin, ["test-native-unit", "test-native-integration", "test-config"])
        self.assertIn("./parse/filter", job("parse-benchmarks"))
        self.assertEqual(ci_scope.BENCHMARKED, "parse/filter")

    def test_listing_covers_every_tag_set_ci_compiles(self):
        tags = set(re.findall(r"-tags=(\w+(?:,\w+)*)", MAKEFILE + WORKFLOW))
        self.assertTrue(tags)
        self.assertLessEqual(tags, set(ci_scope.LANE_TAGS))

    def test_make_covers_exactly_the_listed_packages(self):
        def recipe(target, packages):
            return subprocess.check_output(["make", "-s", "-n", target, f"PACKAGES={packages}"],
                                           cwd=ROOT, timeout=30).decode().strip()
        self.assertEqual(recipe("test-gowalker", "all").split()[-1], "./gowalker/...")
        # A nested module's packages belong to it, not to the module around it.
        self.assertTrue(recipe("test-config", "config/kongprovider:config/kongprovider parse:parse")
                        .endswith(" ./config/kongprovider"))
        self.assertEqual(recipe("test-gitx", "parse:parse parse:parse/filter"), 'echo "no covered packages in gitx"')
        self.assertEqual(recipe("test-parse-unit", "parse:parse parse:parse/filter"), "go test -tags=unit ./parse ./parse/filter")


if __name__ == "__main__":
    unittest.main()
