"""Release checks follow the module trees a change touches, and the scope's
configuration in ci.yml stays in step with the Makefile and the tests."""
import contextlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import ci_release_checks as ci

ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = (ROOT / ".github/workflows/ci.yml").read_text()
NIGHTLY = (ROOT / ".github/workflows/nightly.yml").read_text()
MAKEFILE = (ROOT / "Makefile").read_text()
MODULE_PATH = "github.com/ashtonian/pkg"


def job(name):
    """The text of one ci.yml job."""
    return re.split(r"\n  (?=[a-z-]+:\n)", WORKFLOW.split(f"\n  {name}:\n", 1)[1], maxsplit=1)[0]


def scope_input(name):
    """A scope step input: a block scalar's lines, or a plain scalar."""
    step = job("scope").split("uses: ashtonian/go-ci-actions/scope@", 1)[1]
    match = re.search(rf"^          {name}: (.*)$", step, re.M)
    if match[1] != "|":
        return match[1]
    lines = []
    for line in step[match.end() + 1:].split("\n"):
        if not line.startswith("            "):
            break
        lines.append(line.strip())
    return "\n".join(lines)


def make_list(name):
    words = MAKEFILE.split(f"\n{name} := ", 1)[1].split("\n", 1)[0].split()
    return [w for v in words for w in (make_list(v[2:-1]) if v.startswith("$(") else [v])]


def glob(pattern):
    """go-ci-actions scope's glob: ** as a whole segment spans directories."""
    parts = pattern.split("/")
    source = "".join(("(?:[^/]+/)*" if i < len(parts) - 1 else ".*") if part == "**"
                     else re.escape(part).replace(r"\*", "[^/]*").replace(r"\?", "[^/]") + ("/" if i < len(parts) - 1 else "")
                     for i, part in enumerate(parts))
    return re.compile(f"^{source}$")


def ignored(path):
    return any(glob(pattern).match(path) for pattern in scope_input("go-ignore").split("\n"))


SCOPE_JOBS = json.loads(scope_input("jobs"))
OWNERS = json.loads(scope_input("go-owners"))


class ReleaseChecks(unittest.TestCase):
    def test_archive_examples_remain_required_for_prose(self):
        self.assertEqual(ci.release_checks(["parse/docs/queries.md"]), ["parse-release-check", "parse-example-check", "docs-check"])
        modules = job("modules")
        self.assertIn("contains(needs.scope.outputs.jobs, '\"modules\"')", modules)
        self.assertIn("needs.scope.outputs.checks", modules)
        self.assertIn('make "${checks[@]}"', modules)
        for check in ci.CHECKS:
            with self.subTest(check=check):
                self.assertIn(f"\n{check}:", MAKEFILE)
        listed = MAKEFILE.split("\nRELEASE_CHECKS := ", 1)[1].split("\n", 1)[0].split()
        self.assertEqual(listed, [c for c in ci.CHECKS if c.endswith("-release-check")])

    def test_release_checks_follow_the_changed_modules(self):
        cases = {
            ("parse/internal/dialect/dialect.go",): ["parse-release-check", "parse-example-check", "docs-check"],
            ("parse/sdk/ts/src/client.ts", "parse/README.md"): ["parse-release-check", "parse-example-check", "docs-check"],
            ("httpx/codec/proto/codec.go",): ["httpx-release-check", "docs-check"],
            ("tools/check-httpx-release.sh",): ["httpx-release-check"],
            ("state/store.go", "onepassword/client.go"): ["native-release-check", "docs-check"],
            ("config/watch.go",): ["config-release-check", "cookbook-check", "docs-check"],
            ("config/kongprovider/load.go",): ["kongprovider-release-check", "cookbook-check", "docs-check"],
            ("config/examples/cookbook/main.go",): ["cookbook-check", "docs-check"],
            ("parse/examples/server/server.go", "parse/examples/server/go.mod"): ["parse-example-check", "docs-check"],
            ("gitx/merge.go", "jsonvalue/value.go"): ["gitx-release-check", "jsonvalue-release-check", "docs-check"],
            ("tools/doccheck/check.go",): ["docs-check"],
            ("httpx/examples/gin/main.go",): ["httpx-release-check", "docs-check"],
            ("tools/release-please/first-parent.mjs",): ["release-please-check"],
            ("tools/check-parse-sdk-release.sh",): [],
            ("CONTRIBUTING.md", "README.md"): [],
            (): [],
        }
        for paths, want in cases.items():
            with self.subTest(paths=paths):
                self.assertEqual(ci.release_checks(list(paths)), want)

    def test_the_documentation_check_reads_every_module(self):
        # doccheck reads every go.mod's module outside the directories it
        # skips, in the workspace or not; a change to any of them runs it.
        skipped = ("testdata", "vendor", "node_modules")
        tracked = subprocess.check_output(["git", "ls-files", "-z", "--", "*go.mod"], cwd=ROOT).decode().split("\0")
        modules = [PurePosixPath(p).parent for p in tracked if PurePosixPath(p).name == "go.mod"]
        modules = [m for m in modules if not any(part in skipped or part[0] in "._" for part in m.parts)]
        for module in modules:
            with self.subTest(module=module):
                self.assertTrue(ci.reads("docs-check", f"{module}/README.md"))
        for tree in ci.CHECKS["docs-check"]:
            with self.subTest(tree=tree):
                self.assertTrue(any(f"{m}/".startswith(tree) for m in modules), f"{tree} holds no module")

    def test_shared_unknown_or_unavailable_inputs_run_every_release_check(self):
        every = list(ci.CHECKS)
        for path in ("Makefile", "go.work", ".github/workflows/ci.yml", "tools/ci_release_checks.py",
                     "tools/modarchive/main.go", ".release-please-manifest.json", "new-module/go.mod"):
            with self.subTest(path=path):
                self.assertEqual(ci.release_checks(["parse/list.go", path]), every)
        self.assertEqual(ci.release_checks(None), every)


    def test_lint_configuration_runs_the_lint_job(self):
        # golangci-lint's configuration and ruleguard rules reach no package
        # and run lint over every package. The configuration reaches no release
        # check; the rules are a module, which the documentation check reads.
        config, rules = [".golangci.yml"], ["tools/gorules/rules.go", "tools/gorules/go.mod"]
        self.assertEqual(ci.release_checks(config), [])
        self.assertEqual(ci.plan(["modules", "lint"], config), (["lint"], []))
        self.assertEqual(ci.release_checks(rules), ["docs-check"])
        self.assertEqual(ci.plan(["modules", "lint"], rules), (["modules", "lint"], ["docs-check"]))
        paths = config + rules
        self.assertEqual(SCOPE_JOBS["lint"]["paths"], [".golangci.yml", "tools/gorules/**"])
        for path in paths:
            with self.subTest(path=path):
                self.assertTrue(ignored(path))
        rules = re.search(r'rules: "\$\{base-path\}/(\S+)"', (ROOT / ".golangci.yml").read_text())[1]
        self.assertEqual(rules, "tools/gorules/rules.go")
        self.assertIn("go work edit -use=./tools/gorules", MAKEFILE)
        # new-from-merge-base needs main's history in the lint job's checkout.
        self.assertIn("fetch-depth: 0", job("lint"))


class Plan(unittest.TestCase):
    def test_the_modules_job_runs_only_with_a_release_check(self):
        self.assertEqual(ci.plan(["test", "modules"], ["CONTRIBUTING.md"]), (["test"], []))
        self.assertEqual(ci.plan(["test", "modules"], ["parse/list.go"]), (["test", "modules"], ["parse-release-check", "parse-example-check", "docs-check"]))

    def test_the_cookbook_lints_whenever_its_check_runs(self):
        self.assertEqual(ci.plan(["modules"], ["config/examples/cookbook/main.go"]),
                         (["modules", "lint"], ["cookbook-check", "docs-check"]))

    def test_a_full_run_makes_every_check(self):
        jobs, checks = ci.plan(list(ci.JOBS), None)
        self.assertEqual((jobs, checks), (list(ci.JOBS), list(ci.CHECKS)))

    def test_an_unknown_job_fails(self):
        with self.assertRaises(ValueError):
            ci.plan(["deploy"], [])

    def test_main_reads_the_scope_and_writes_jobs_for_acceptance(self):
        parse = f"{MODULE_PATH}/parse/filter"
        with tempfile.TemporaryDirectory() as directory:
            files, parent_files, output = Path(directory, "files"), Path(directory, "parent"), Path(directory, "output")
            files.write_text("parse/filter/sql.go\nhttpx/client.go\n")
            parent_files.write_text("parse/filter/sql.go\n")
            env = {"SCOPE_JOBS": '["test","parse-benchmarks","modules"]', "SCOPE_FULL": "false",
                   "SCOPE_FILES": str(files), "GITHUB_OUTPUT": str(output),
                   "SCOPE_SELECTIONS": json.dumps({"test": [parse, f"{MODULE_PATH}/httpx"], "parse-benchmarks": [parse],
                                                   "modules": "all"}),
                   "SCOPE_PARENT_SELECTIONS": json.dumps({"test": [parse], "parse-benchmarks": [parse], "modules": "all"}),
                   "SCOPE_PARENT_FILES": str(parent_files)}
            with patch.dict(os.environ, env, clear=True), contextlib.redirect_stdout(io.StringIO()):
                ci.main()
            values = dict(line.split("=", 1) for line in output.read_text().splitlines())
            self.assertEqual(json.loads(values["jobs"]), ["test", "modules", "parse-benchmarks"])
            self.assertEqual(json.loads(values["required"]), ["test", "modules"])
            self.assertEqual(values["checks"], "httpx-release-check parse-release-check parse-example-check docs-check")
            self.assertEqual(values["lint-cookbook"], "false")
            self.assertEqual(json.loads(values["selections"])["modules"], ["httpx-release-check", "parse-release-check", "parse-example-check", "docs-check"])
            # The pull request's run, measured from the merge's first parent,
            # made only parse's release check: it does not cover this push's.
            self.assertEqual(json.loads(values["parent-selections"]),
                             {"test": [parse], "modules": ["parse-release-check", "parse-example-check", "docs-check"], "parse-benchmarks": [parse]})
            for full, listing in (("true", str(files)), ("false", "")):
                with self.subTest(full=full, listing=listing):
                    self.assertIsNone(ci.changed_paths({"SCOPE_FULL": full, "SCOPE_FILES": listing}))

    def test_without_a_parent_measurement_main_writes_no_parent_selections(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory, "output")
            env = {"SCOPE_JOBS": '["modules"]', "SCOPE_FULL": "true", "SCOPE_FILES": "",
                   "SCOPE_SELECTIONS": '{"modules":"all"}', "GITHUB_OUTPUT": str(output)}
            with patch.dict(os.environ, env, clear=True), contextlib.redirect_stdout(io.StringIO()):
                ci.main()
            values = dict(line.split("=", 1) for line in output.read_text().splitlines())
            self.assertEqual(values["parent-selections"], "")
            self.assertEqual(json.loads(values["selections"]), {"modules": list(ci.CHECKS), "lint": ["cookbook-check"]})


class Selections(unittest.TestCase):
    """What each job checks, for acceptance to compare a push with its pull request."""

    def test_the_modules_job_checks_its_release_checks(self):
        jobs, checks, chosen = ci.measure(["test", "modules"], ["gitx/merge.go"], {"test": ["x"], "modules": "all"})
        self.assertEqual(chosen, {"test": ["x"], "modules": ["gitx-release-check", "docs-check"]})

    def test_lint_holds_the_cookbook_when_it_lints_it(self):
        _, _, chosen = ci.measure(["modules"], ["config/examples/cookbook/main.go"], {"modules": "all"})
        self.assertEqual(chosen["lint"], ["cookbook-check"])
        _, _, chosen = ci.measure(["lint", "modules"], ["config/watch.go"], {"lint": ["c"], "modules": "all"})
        self.assertEqual(chosen["lint"], ["c", "cookbook-check"])
        _, _, chosen = ci.measure(["lint"], ["httpx/client.go"], {"lint": ["h"]})
        self.assertEqual(chosen["lint"], ["h"])

    def test_lint_of_every_package_lints_the_cookbook_too(self):
        # A go.mod change outside config reaches every package but no cookbook
        # check: lint's "all" must still hold the cookbook it then lints.
        _, checks, chosen = ci.measure(["lint", "modules"], ["httpx/go.mod"], {"lint": "all", "modules": "all"})
        self.assertNotIn("cookbook-check", checks)
        self.assertTrue(ci.lints_cookbook(checks, {"lint": "all"}))
        self.assertEqual(chosen["lint"], "all")


class Scope(unittest.TestCase):
    """ci.yml's scope configuration."""

    def test_scope_names_every_job_and_each_job_follows_it(self):
        self.assertEqual(list(SCOPE_JOBS), [j for j in SCOPE_JOBS if j in ci.JOBS])
        self.assertEqual(set(SCOPE_JOBS), set(ci.JOBS))
        for name in ci.VALIDATION_JOBS:
            with self.subTest(job=name):
                self.assertIn(f"contains(needs.scope.outputs.jobs, '\"{name}\"')", job(name))
        self.assertIn("contains(needs.scope.outputs.selected, '\"parse-benchmarks\"')", job("parse-benchmarks"))
        self.assertIn("needs: [" + ", ".join(("scope",) + ci.VALIDATION_JOBS) + "]", job("accepted"))
        scope = job("scope")
        for line in ("base: ${{ steps.scope.outputs.base }}", "required: ${{ steps.checks.outputs.required }}",
                     "selections: ${{ steps.checks.outputs.selections }}",
                     "parent-selections: ${{ steps.checks.outputs.parent-selections }}",
                     "SCOPE_SELECTIONS: ${{ steps.scope.outputs.selections }}",
                     "SCOPE_PARENT_SELECTIONS: ${{ steps.scope.outputs.parent-selections }}",
                     "SCOPE_PARENT_FILES: ${{ steps.scope.outputs.parent-files }}"):
            with self.subTest(line=line):
                self.assertIn(line, scope)
        self.assertIn("if: needs.scope.outputs.lint-cookbook == 'true'", job("lint"))

    def test_job_packages_are_the_makefile_modules(self):
        def patterns(modules):
            return [f"./{module}/..." for module in modules]
        self.assertEqual(SCOPE_JOBS["test-darwin"]["go"], patterns(make_list("NATIVE_MODULES") + make_list("CONFIG_MODULES")))
        self.assertEqual(SCOPE_JOBS["minimum-go"]["go"], patterns(make_list("HTTPX_MODULES") + ["gitx"]))
        self.assertEqual(SCOPE_JOBS["parse-postgres"]["go"], patterns(make_list("PARSE_MODULES")))
        self.assertEqual(SCOPE_JOBS["parse-benchmarks"]["go"], ["./parse/filter"])
        self.assertIn("./parse/filter | tee", job("parse-benchmarks"))
        darwin = re.findall(r"run: make (\S+)", job("test-darwin"))
        self.assertEqual(darwin, ["test-native-unit", "test-native-integration", "test-config"])

    def test_go_jobs_cover_what_the_change_reaches(self):
        for name in ("test", "test-darwin", "parse-postgres", "lint"):
            with self.subTest(job=name):
                self.assertIn(f"PACKAGES: ${{{{ contains(needs.scope.outputs.wide-jobs, '\"{name}\"') && 'all' "
                              "|| needs.scope.outputs.go-args }}", job(name))
        recipes = [line for line in MAKEFILE.splitlines() if line.startswith("\t")]
        self.assertEqual([line for line in recipes if re.match(r"\tgo (test|vet|run)", line)], [])

    def test_the_listing_sees_every_file_ci_compiles(self):
        lanes = set(re.findall(r"-tags=(\w+(?:,\w+)*)", MAKEFILE + WORKFLOW)) | {"-"}
        self.assertLessEqual(lanes, set(scope_input("go-tags").split("\n")))
        builds = scope_input("go-builds").split("\n")
        self.assertEqual(builds, ["GOOS=linux GOARCH=amd64", "GOOS=darwin GOARCH=arm64"])
        self.assertIn("runs-on: macos-latest", job("test-darwin"))

    def test_ignored_paths_hold_no_workspace_code(self):
        used = json.loads(subprocess.check_output(["go", "work", "edit", "-json"], cwd=ROOT))["Use"]
        workspace = {str(PurePosixPath(u["DiskPath"]).relative_to(".")) if u["DiskPath"] != "." else "" for u in used}
        tracked = subprocess.check_output(["git", "-C", str(ROOT), "ls-files", "-z"]).decode().split("\0")[:-1]
        modules = {str(PurePosixPath(p).parent) for p in tracked if PurePosixPath(p).name == "go.mod"}
        self.assertEqual(workspace, set(make_list("MODULES")))
        for module in sorted(modules - workspace):
            with self.subTest(module=module):
                self.assertTrue(ignored(module + "/main.go"), "a module outside go.work: add it to go-ignore")
        for module in sorted(workspace):
            with self.subTest(module=module):
                self.assertFalse(ignored(module + "/x.go"))
        self.assertTrue(ignored("parse/sdk/ts/src/client.ts"))
        self.assertFalse(ignored("parse/testdata/sdk_wire.json"))
        docs = [p for p in tracked if re.match(r"^[^/]+/docs/", p) and not p.endswith(".md")]
        self.assertTrue(all(not ignored(p) for p in docs), docs)

    def test_tests_reading_other_directories_declare_them(self):
        # A test that reads a tracked file outside its package directory and
        # testdata names it in go-owners, or a change to that file skips it.
        tracked = subprocess.check_output(["git", "-C", str(ROOT), "ls-files", "-z", "--", "*_test.go"]).decode().split("\0")
        missing = []
        for test in (PurePosixPath(p) for p in tracked if p and not ignored(p)):
            source = (ROOT / test).read_text()
            reads = set(re.findall(r'"([\w.-]+(?:/[\w.-]+)+\.\w+)"', source))
            for joined in re.findall(r'filepath\.Join\(((?:"[^"]+",\s*)+"[^"]+")\)', source):
                reads.add("/".join(re.findall(r'"([^"]+)"', joined)))
            for relative in reads:
                path = PurePosixPath(os.path.normpath(test.parent / relative))
                if not (ROOT / path).is_file() or str(path.parent) == str(test.parent):
                    continue
                if path.is_relative_to(test.parent / "testdata"):
                    continue
                owners = [dirs for pattern, dirs in OWNERS.items() if glob(pattern).match(str(path))]
                if f"./{test.parent}" not in [d for dirs in owners for d in dirs]:
                    missing.append(f"{test} reads {path}")
        self.assertEqual(missing, [])

    def test_ci_scripts_are_shared_inputs(self):
        shared = scope_input("shared").split("\n")
        for script in sorted((ROOT / "tools").glob("*.py")):
            path = f"tools/{script.name}"
            with self.subTest(path=path):
                self.assertTrue(any(glob(pattern).match(path) for pattern in shared))
        for path in (".github/workflows/ci.yml", ".github/workflows/nightly.yml", "Makefile"):
            self.assertTrue(any(glob(pattern).match(path) for pattern in shared), path)

    def test_the_nightly_is_ci_at_full_scope(self):
        self.assertIn("uses: ./.github/workflows/ci.yml", NIGHTLY)
        self.assertIn("nightly: true", NIGHTLY)
        self.assertIn("workflow: ci.yml", job("scope"))


class Make(unittest.TestCase):
    def recipe(self, target, packages):
        return subprocess.check_output(["make", "-s", "-n", target, f"PACKAGES={packages}"],
                                       cwd=ROOT, timeout=30).decode().strip()

    def test_make_covers_exactly_the_listed_packages(self):
        self.assertEqual(self.recipe("test-gowalker", "all").split()[-1], "./gowalker/...")
        # A nested module's packages belong to it, not to the module around it.
        listed = f"{MODULE_PATH}/config/kongprovider {MODULE_PATH}/parse"
        self.assertTrue(self.recipe("test-config", listed).endswith(f" {MODULE_PATH}/config/kongprovider"))
        self.assertEqual(self.recipe("test-gitx", f"{MODULE_PATH}/parse"), 'echo "no covered packages in gitx"')
        self.assertEqual(self.recipe("test-parse-unit", f"{MODULE_PATH}/parse {MODULE_PATH}/parse/filter"),
                         f"go test -tags=unit {MODULE_PATH}/parse {MODULE_PATH}/parse/filter")
        # A full scope lists each module's path/...
        full = f"{MODULE_PATH}/config/... {MODULE_PATH}/config/kongprovider/... {MODULE_PATH}/httpx/..."
        self.assertTrue(self.recipe("test-config", full).endswith(f"{MODULE_PATH}/config/... {MODULE_PATH}/config/kongprovider/..."))

    def test_make_drops_a_listed_package_its_tags_exclude(self):
        # Every file of parse/internal/dbtest is tagged unit or integration; go
        # refuses it named under -tags=concurrency, where parse/... skips it.
        listed = f"{MODULE_PATH}/parse/internal/dbtest {MODULE_PATH}/parse/core"
        self.assertTrue(self.recipe("test-parse-race", listed).endswith(f"-tags=concurrency {MODULE_PATH}/parse/core"))
        self.assertTrue(self.recipe("test-parse-unit", listed).endswith(f"-tags=unit {listed}"))
        self.assertTrue(self.recipe("test-parse-race", f"{MODULE_PATH}/parse/...").endswith(f"-tags=concurrency {MODULE_PATH}/parse/..."))


if __name__ == "__main__":
    unittest.main()
