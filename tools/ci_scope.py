#!/usr/bin/env python3
"""Keep archive/document checks while avoiding runtime suites for known prose paths."""
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess

GUIDES = re.compile(r"^(config|gowalker|httpx|parse)/docs/.+\.md$")


def prose(path):
    parts = PurePosixPath(path).parts
    return (path == "CONTRIBUTING.md" or bool(GUIDES.fullmatch(path))) and "testdata" not in parts


def runtime_required(paths):
    return not paths or not all(prose(path) for path in paths)


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
        message = "runtime and archive checks required" if runtime else "known prose only: archive, documentation and example checks remain required"
    except (KeyError, TypeError, ValueError, OSError, subprocess.SubprocessError):
        runtime, message = True, "change scope unavailable: all checks required"
    print(message)
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"runtime={str(runtime).lower()}\n")
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a") as output:
            output.write(message + "\n")


if __name__ == "__main__":
    main()
