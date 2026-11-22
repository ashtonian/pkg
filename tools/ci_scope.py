#!/usr/bin/env python3
"""Select the CI work an event's changes require.

Runtime suites skip for known prose paths. Release checks run per module: each
one reads only its module's tree, its own script and the archive tooling, with
every other dependency at the version its go.mod requires. Parse's benchmarks
run when the Go code they build changed.
"""
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess

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
}
# Modules nested in another module's directory are not part of its archive.
NESTED = {"config-release-check": ("config/kongprovider/", "config/examples/cookbook/")}
# Prose outside every module and its tooling.
UNCHECKED = {"CONTRIBUTING.md", "README.md"}
# The benchmarks build parse/filter through go.work, against gowalker's working
# tree; ci.yml defines the job. The TypeScript SDK holds no Go.
BENCHMARK_INPUTS = ("parse/", "gowalker/", "go.work", "go.work.sum", ".github/workflows/ci.yml")
BENCHMARK_IGNORED = ("parse/sdk/",)


def prose(path):
    parts = PurePosixPath(path).parts
    return (path == "CONTRIBUTING.md" or bool(GUIDES.fullmatch(path))) and "testdata" not in parts


def runtime_required(paths):
    return not paths or not all(prose(path) for path in paths)


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
        if path in UNCHECKED:
            continue
        owners = [check for check in CHECKS if reads(check, path)]
        if not owners or path.startswith(ARCHIVE_TOOLING):
            # Shared build files, workflow, tooling, manifests and unknown paths.
            return list(CHECKS)
        selected.update(owners)
    return [check for check in CHECKS if check in selected]


def parse_changed(paths):
    """Whether the Go code parse's benchmarks build changed; true when unsure."""
    if not paths:
        return True
    return any(path.startswith(BENCHMARK_INPUTS) and not path.startswith(BENCHMARK_IGNORED) for path in paths)


def git(*args):
    return subprocess.check_output(["git", *args], timeout=30, stderr=subprocess.PIPE).decode()


def changed_paths(env):
    if env["GITHUB_EVENT_NAME"] not in {"push", "pull_request"}:
        return None
    if git("rev-parse", "HEAD").strip() != env["GITHUB_SHA"]:
        return None
    event = json.loads(Path(env["GITHUB_EVENT_PATH"]).read_text())
    if env["GITHUB_EVENT_NAME"] == "pull_request":
        parents = git("rev-list", "--parents", "-n", "1", "HEAD").split()
        if len(parents) != 3 or parents[2] != event["pull_request"]["head"]["sha"]:
            return None
        base = parents[1]
    else:
        base = event["before"]
        if not re.fullmatch(r"[0-9a-f]{40}", base) or int(base, 16) == 0:
            return None
    # A missing shallow base runs the complete suite; never trust a truncated API diff.
    return git("diff", "--no-renames", "--name-only", "-z", base, "HEAD").split("\0")[:-1]


def main():
    try:
        paths = changed_paths(os.environ)
        runtime = runtime_required(paths)
        checks = release_checks(paths)
        parse = parse_changed(paths)
        message = "runtime and archive checks required" if runtime else "known prose only: archive, documentation and example checks remain required"
    except (KeyError, TypeError, ValueError, OSError, subprocess.SubprocessError):
        runtime, checks, parse, message = True, list(CHECKS), True, "change scope unavailable: all checks required"
    message += "; release checks: " + (" ".join(checks) or "none")
    message += "; benchmarked parse code: " + ("changed" if parse else "unchanged")
    print(message)
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"runtime={str(runtime).lower()}\nchecks={' '.join(checks)}\nparse={str(parse).lower()}\n")
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a") as output:
            output.write(message + "\n")


if __name__ == "__main__":
    main()
