#!/usr/bin/env python3
"""Select the CI work an event's changes require.

A change reaches the Go packages it edits and every workspace package whose
build or tests link one of them, under each platform and build tag set CI
compiles (`go list -test`). The test and lint targets cover only reached
packages, and a job runs only when its modules hold one. Release checks follow
the module trees they read; the TypeScript SDK job follows its sources and the
corpora it shares with parse's Go tests. Module manifests, go.work, shared CI
inputs, unknown paths and an unknown delta reach everything.
"""
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess

import ci_github

GUIDES = re.compile(r"^(config|gowalker|httpx|parse)/docs/.+\.md$")
ARCHIVE_TOOLING = "tools/modarchive/"
# Make targets in the order release-check runs them, with the trees each reads.
CHECKS = {
    "gitx-release-check": ("gitx/",),
    "config-release-check": ("config/",),
    "kongprovider-release-check": ("config/kongprovider/",),
    "native-release-check": ("process/", "state/", "onepassword/", "tools/check-native-release.sh"),
    "jsonvalue-release-check": ("jsonvalue/", "tools/check-jsonvalue-release.sh"),
    "gowalker-release-check": ("gowalker/", "tools/check-gowalker-release.sh"),
    "httpx-release-check": ("httpx/", "tools/check-httpx-release.sh"),
    "parse-release-check": ("parse/", "tools/check-parse-release.sh"),
    # The cookbook replaces both config modules with their working trees.
    "cookbook-check": ("config/",),
    "tool-check": (ARCHIVE_TOOLING,),
    "release-please-check": ("tools/release-please/",),
}
# Modules nested in another module's directory are not part of its archive.
NESTED = {"config-release-check": ("config/kongprovider/", "config/examples/cookbook/")}
# Prose outside every module and its tooling.
UNCHECKED = {"CONTRIBUTING.md", "README.md"}
# Tooling only the release checks above read.
RELEASE_TOOLING = tuple(tree for trees in CHECKS.values() for tree in trees if tree.startswith("tools/"))
# The SDK job's inputs: its sources, the corpora parse's Go tests write and it
# replays, and its check scripts. The SDK tree and the scripts hold no Go.
SDK_TREE = "parse/sdk/"
SDK_TOOLING = "tools/check-parse-sdk-"
SDK_INPUTS = (SDK_TREE, "parse/testdata/", SDK_TOOLING)

# Jobs that validate a change, as ci.yml names them. parse-benchmarks records
# main's benchmarks and validates nothing.
VALIDATION_JOBS = ("test", "test-darwin", "parse-postgres", "parse-sdk", "minimum-go", "modules", "lint")
JOBS = VALIDATION_JOBS + ("parse-benchmarks",)
# The modules a job tests; the Makefile's module lists name the same sets.
DARWIN_MODULES = ("process", "state", "onepassword", "config", "config/kongprovider")
MINIMUM_GO_MODULES = ("httpx", "httpx/oauth2", "httpx/codec/msgpack", "httpx/codec/proto", "httpx/compress/brotli", "gitx")
POSTGRES_MODULES = ("parse",)
BENCHMARKED = "parse/filter"
# What CI compiles: each Go job's platform, and every -tags set of the Makefile
# and ci.yml. A file joins a package only under some of them.
PLATFORMS = (("linux", "amd64"), ("darwin", "arm64"))
LANE_TAGS = ("", "unit", "integration", "concurrency", "unit,integration")
EVERYTHING = "all"


class Unscoped(Exception):
    """A change whose reach this tool cannot bound."""


def prose(path):
    parts = PurePosixPath(path).parts
    return (path in UNCHECKED or bool(GUIDES.fullmatch(path))) and "testdata" not in parts


def reads(check, path):
    if any(path.startswith(nested) for nested in NESTED.get(check, ())):
        return False
    return any(path == tree or path.startswith(tree) for tree in CHECKS[check])


def release_checks(paths):
    """The release checks whose inputs changed; every one when unsure."""
    if not paths:
        return list(CHECKS)
    selected = set()
    for path in paths:
        if path in UNCHECKED or path.startswith(SDK_TOOLING):
            continue
        owners = [check for check in CHECKS if reads(check, path)]
        if not owners or path.startswith(ARCHIVE_TOOLING):
            # Shared build files, workflow, tooling, manifests and unknown paths.
            return list(CHECKS)
        selected.update(owners)
    return [check for check in CHECKS if check in selected]


def go(*args, root, offline=False, platform=None):
    env = {**os.environ, "GOTOOLCHAIN": "local", "GOWORK": "", "GOFLAGS": ""}
    if offline:
        env["GOPROXY"] = "off"
    if platform:
        env["GOOS"], env["GOARCH"] = platform
    return subprocess.check_output(["go", *args], cwd=root, env=env, timeout=120, stderr=subprocess.PIPE).decode()


def json_stream(text):
    decoder, index, values = json.JSONDecoder(), 0, []
    while True:
        while index < len(text) and text[index].isspace():
            index += 1
        if index == len(text):
            return values
        value, index = decoder.raw_decode(text, index)
        values.append(value)


class Workspace:
    """The go.work modules' packages, and the local packages each one's build and tests link."""

    def __init__(self, modules, all_modules, packages, links):
        self.modules = set(modules)
        self.all_modules = set(all_modules)
        self.packages = packages
        self.links = links

    @classmethod
    def load(cls, root):
        root = Path(root).resolve()

        def relative(directory):
            return Path(directory).resolve().relative_to(root).as_posix()

        tracked = subprocess.check_output(["git", "ls-files", "-z", "--", "go.mod", ":(glob)**/go.mod"],
                                          cwd=root, timeout=30, stderr=subprocess.PIPE).decode().split("\0")[:-1]
        all_modules = {str(PurePosixPath(path).parent) for path in tracked}
        main = {module["Path"]: relative(module["Dir"])
                for module in json_stream(go("list", "-m", "-json", root=root, offline=True))}
        # The one online step fetches the requirement graph's go.mod files, which
        # go.sum already pins. Package listing then runs offline, so it never
        # downloads module sources.
        for edge in go("mod", "graph", root=root).splitlines():
            user, _, required = edge.partition(" ")
            if user.split("@")[0] not in main and required.split("@")[0] in main:
                # Its packages could import workspace packages this listing cannot see.
                raise Unscoped(f"{user} requires workspace module {required}")
        patterns = [f"./{directory}/..." for directory in sorted(main.values())]

        def listing(platform, tags):
            return json_stream(go("list", "-e", "-test", f"-tags={tags}",
                                  "-json=ImportPath,Dir,Module,ForTest,Deps,Error", *patterns,
                                  root=root, offline=True, platform=platform))

        with ThreadPoolExecutor() as pool:
            listings = list(pool.map(lambda args: listing(*args),
                                     [(platform, tags) for platform in PLATFORMS for tags in LANE_TAGS]))
        packages, imports, deps = {}, {}, {}
        for entry in (entry for result in listings for entry in result):
            module = entry.get("Module") or {}
            if not module.get("Main"):
                continue
            if entry.get("Error"):
                raise Unscoped(f"go list {entry['ImportPath']}: {entry['Error'].get('Err')}")
            directory = relative(entry["Dir"])
            if not entry.get("ForTest") and not entry["ImportPath"].endswith(".test"):
                packages[directory] = relative(module["Dir"])
                imports[entry["ImportPath"]] = directory
            # A package's own entry carries its build's imports; its .test entry, its tests'.
            if not entry.get("ForTest"):
                deps.setdefault(directory, set()).update(dep.split(" ")[0] for dep in entry.get("Deps") or ())
        links = {directory: {imports[dep] for dep in found if dep in imports} | {directory}
                 for directory, found in deps.items() if directory in packages}
        return cls(set(main.values()), all_modules, packages, links)

    def module_of(self, path):
        """The deepest module directory holding path, or None."""
        holders = [module for module in self.all_modules if path.startswith(module + "/")]
        return max(holders, key=len) if holders else None

    def reached(self, sources, tests):
        """Packages whose build or tests link a changed source, or whose tests changed."""
        return {directory for directory, linked in self.links.items() if directory in tests or linked & sources}


def changes(paths, workspace):
    """The changed package sources, test-only changes and whether the SDK's inputs changed."""
    sources, tests, sdk = set(), set(), False
    for path in paths:
        if prose(path):
            continue
        if path.startswith(SDK_INPUTS):
            sdk = True
        if path.startswith((SDK_TREE, SDK_TOOLING) + RELEASE_TOOLING):
            continue
        module = workspace.module_of(path)
        if module is None:
            # go.work, the Makefile, workflows, CI scripts, release configuration and unknown paths.
            raise Unscoped(path)
        if module not in workspace.modules:
            # A module outside go.work (examples, benchmarks, release tooling): only its checks read it.
            continue
        if path in (f"{module}/go.mod", f"{module}/go.sum"):
            # Any module's requirements can move the version every workspace module builds against.
            raise Unscoped(path)
        name, directory = PurePosixPath(path).name, str(PurePosixPath(path).parent)
        if name.endswith(".go") and directory in workspace.packages:
            (tests if name.endswith("_test.go") else sources).add(directory)
        else:
            # Tests read files anywhere in their module (READMEs, testdata) and packages embed them.
            sources.update(package for package, owner in workspace.packages.items() if owner == module)
    return sources, tests, sdk


def everything():
    return {"jobs": dict.fromkeys(JOBS, True), "packages": EVERYTHING, "checks": list(CHECKS)}


def plan(paths, load):
    """The jobs, packages and release checks paths require; everything when unsure."""
    if not paths:
        return everything()
    checks = release_checks(paths)
    workspace = load()
    sources, tests, sdk = changes(paths, workspace)
    reached = workspace.reached(sources, tests)
    modules = {workspace.packages[package] for package in reached}
    jobs = {
        "test": bool(reached),
        "test-darwin": bool(modules & set(DARWIN_MODULES)),
        "parse-postgres": bool(modules & set(POSTGRES_MODULES)),
        "parse-sdk": sdk,
        "minimum-go": bool(modules & set(MINIMUM_GO_MODULES)),
        "modules": bool(checks),
        # The cookbook lints with the check that builds it.
        "lint": bool(reached) or "cookbook-check" in checks,
        "parse-benchmarks": BENCHMARKED in reached,
    }
    packages = " ".join(f"{workspace.packages[package]}:{package}" for package in sorted(reached))
    return {"jobs": jobs, "packages": packages, "checks": checks}


def git(*args):
    return subprocess.check_output(["git", *args], timeout=30, stderr=subprocess.PIPE).decode()


def last_green_push(env, api):
    """The newest commit on main's history whose push run of ci.yml passed."""
    runs = api.get("actions/workflows/ci.yml/runs", branch="main", event="push", status="success",
                   per_page=30)["workflow_runs"]
    for run in sorted(runs, key=lambda r: r["run_number"], reverse=True):
        # A rerun's own earlier attempt is not evidence for itself.
        if str(run["id"]) == env.get("GITHUB_RUN_ID") or not re.fullmatch(r"[0-9a-f]{40}", run["head_sha"]):
            continue
        if subprocess.run(["git", "merge-base", "--is-ancestor", run["head_sha"], "HEAD"],
                          timeout=30, capture_output=True).returncode == 0:
            return run["head_sha"]
    return None


def changed_paths(env, api=None):
    if env["GITHUB_EVENT_NAME"] not in {"push", "pull_request"}:
        return None
    if git("rev-parse", "HEAD").strip() != env["GITHUB_SHA"]:
        return None
    if env["GITHUB_EVENT_NAME"] == "pull_request":
        event = json.loads(Path(env["GITHUB_EVENT_PATH"]).read_text())
        parents = git("rev-list", "--parents", "-n", "1", "HEAD").split()
        if len(parents) != 3 or parents[2] != event["pull_request"]["head"]["sha"]:
            return None
        base = parents[1]
    else:
        # The last passing run, not the push's before: a failed, cancelled or
        # coalesced run left its changes unvalidated.
        base = last_green_push(env, api or ci_github.API(env))
        if base is None:
            return None
    # A missing shallow base runs the complete suite; never trust a truncated API diff.
    return git("diff", "--no-renames", "--name-only", "-z", base, "HEAD").split("\0")[:-1]


def describe(selected):
    jobs = " ".join(name for name in JOBS if selected["jobs"][name]) or "none"
    packages = selected["packages"]
    count = "every package" if packages == EVERYTHING else f"{len(packages.split())} packages"
    return f"jobs: {jobs}; {count}: {packages or 'none'}; release checks: {' '.join(selected['checks']) or 'none'}"


def main():
    try:
        selected = plan(changed_paths(os.environ), lambda: Workspace.load(Path.cwd()))
        message = describe(selected)
    except Unscoped as reason:
        selected, message = everything(), f"change reaches everything ({reason}): {describe(everything())}"
    except (KeyError, TypeError, ValueError, OSError, subprocess.SubprocessError) as error:
        selected, message = everything(), f"change scope unavailable ({type(error).__name__}): all checks required"
    print(message)
    jobs = json.dumps(selected["jobs"], sort_keys=True, separators=(",", ":"))
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"jobs={jobs}\npackages={selected['packages']}\nchecks={' '.join(selected['checks'])}\n")
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a") as output:
            output.write(message + "\n")


if __name__ == "__main__":
    main()
