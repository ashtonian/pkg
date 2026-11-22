#!/usr/bin/env python3
"""Select the release checks a change requires, from go-ci-actions scope's result.

Each release check reads only its module's tree, its own script and the archive
tooling, with every other dependency at the version its go.mod requires, so a
change runs the checks of the module trees it touches. The scope selects the
jobs; this narrows the modules job to those checks, drops it when none is
selected, and adds lint when the cookbook check runs, since the cookbook lints
with it.
"""
import json
import os
from pathlib import Path

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
# Prose outside every module, and the SDK's scripts, which its own job runs.
UNCHECKED = {"CONTRIBUTING.md", "README.md"}
SDK_TOOLING = "tools/check-parse-sdk-"
# Jobs that validate a change, as ci.yml names them. parse-benchmarks records
# main's benchmarks and validates nothing, so acceptance does not require it.
VALIDATION_JOBS = ("test", "test-darwin", "parse-postgres", "parse-sdk", "minimum-go", "modules", "lint")
JOBS = VALIDATION_JOBS + ("parse-benchmarks",)


def reads(check, path):
    if any(path.startswith(nested) for nested in NESTED.get(check, ())):
        return False
    return any(path == tree or path.startswith(tree) for tree in CHECKS[check])


def release_checks(paths):
    """The release checks whose inputs changed; every one when unsure."""
    if paths is None:
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


def plan(jobs, paths):
    """The jobs to run and release checks to make, from the scope's jobs and changed paths."""
    unknown = set(jobs) - set(JOBS)
    if unknown:
        raise ValueError("scope selected jobs ci.yml does not define: " + " ".join(sorted(unknown)))
    checks = release_checks(paths)
    selected = set(jobs)
    if not checks:
        selected.discard("modules")
    if "cookbook-check" in checks:
        selected.add("lint")
    return [job for job in JOBS if job in selected], checks


def changed_paths(env):
    """The scope's changed paths, or None for a full run."""
    if env["SCOPE_FULL"] == "true" or not env.get("SCOPE_FILES"):
        return None
    return [line for line in Path(env["SCOPE_FILES"]).read_text().splitlines() if line]


def main():
    jobs, checks = plan(json.loads(os.environ["SCOPE_JOBS"]), changed_paths(os.environ))
    required = [job for job in jobs if job in VALIDATION_JOBS]
    message = "jobs: " + (" ".join(jobs) or "none") + "; release checks: " + (" ".join(checks) or "none")
    print(message)
    compact = {"separators": (",", ":")}
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"jobs={json.dumps(jobs, **compact)}\nrequired={json.dumps(required, **compact)}\n"
                     f"checks={' '.join(checks)}\n")
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a") as output:
            output.write(message + "\n")


if __name__ == "__main__":
    main()
