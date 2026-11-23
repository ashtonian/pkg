#!/usr/bin/env python3
"""Select the release checks a change requires, from go-ci-actions scope's result.

Each release check reads only its module's tree, its own script and the archive
tooling, with every other dependency at the version its go.mod requires, so a
change runs the checks of the module trees it touches. The scope selects the
jobs; this narrows the modules job to those checks, drops it when none is
selected, and adds lint when the cookbook check runs, since the cookbook lints
with it.

It also writes what each job checks for acceptance: the scope's selection,
with the modules job's release checks and the cookbook lint in place of what
the scope cannot see. On a merge push it computes the same from the scope's
measurement against the merge's first parent, as the pull request's run
measured it, and the push reuses a job only when that covers its own.
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
    # The Parse server example replaces Parse with its working tree.
    "parse-example-check": ("parse/",),
    "tool-check": (ARCHIVE_TOOLING,),
    "release-please-check": ("tools/release-please/",),
    # The documentation check reads every module's Markdown, API and comments,
    # the workspace's and those kept out of it.
    "docs-check": ("config/", "gitx/", "gowalker/", "httpx/", "jsonvalue/", "onepassword/", "parse/", "process/",
                   "state/", "tools/doccheck/", "tools/modarchive/"),
}
# Modules nested in another module's directory are not part of its archive.
NESTED = {
    "config-release-check": ("config/kongprovider/", "config/examples/cookbook/"),
    "parse-release-check": ("parse/examples/server/",),
}
# Prose outside every module, and the SDK's scripts, which its own job runs.
UNCHECKED = {"CONTRIBUTING.md", "README.md"}
SDK_TOOLING = "tools/check-parse-sdk-"
# golangci-lint's configuration and ruleguard rules, which only the lint job reads.
LINT_CONFIG = (".golangci.yml", "tools/gorules/")
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
        if path in UNCHECKED or path.startswith(SDK_TOOLING) or path.startswith(LINT_CONFIG):
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


def lints_cookbook(checks, scope_selections):
    """The lint job lints the cookbook when its check runs, and whenever it lints
    every package, so that its "all" holds the cookbook too."""
    return "cookbook-check" in checks or scope_selections.get("lint") == "all"


def selections(jobs, checks, scope_selections):
    """What each job checks: the scope's selection, except the modules job's
    release checks, and lint's packages plus the cookbook when it lints it."""
    chosen = {}
    for job in jobs:
        if job == "modules":
            chosen[job] = checks
        elif job == "lint":
            packages = scope_selections.get("lint", [])
            cookbook = ["cookbook-check"] if lints_cookbook(checks, scope_selections) else []
            chosen[job] = "all" if packages == "all" else sorted(packages) + cookbook
        else:
            chosen[job] = scope_selections[job]
    return chosen


def measure(scope_jobs, paths, scope_selections):
    """The jobs, release checks and selections of one scope measurement."""
    jobs, checks = plan(scope_jobs, paths)
    return jobs, checks, selections(jobs, checks, scope_selections)


def listed_paths(listing):
    """The paths in a scope file listing, or None without one (a full run)."""
    if not listing:
        return None
    return [line for line in Path(listing).read_text().splitlines() if line]


def changed_paths(env):
    """The scope's changed paths, or None for a full run."""
    if env["SCOPE_FULL"] == "true":
        return None
    return listed_paths(env.get("SCOPE_FILES"))


def main():
    env = os.environ
    scope_selections = json.loads(env.get("SCOPE_SELECTIONS") or "{}")
    jobs, checks, chosen = measure(json.loads(env["SCOPE_JOBS"]), changed_paths(env), scope_selections)
    required = [job for job in jobs if job in VALIDATION_JOBS]
    compact = {"separators": (",", ":")}
    # The scope's parent measurement selects the jobs the pull request's run did.
    parent = ""
    if env.get("SCOPE_PARENT_SELECTIONS"):
        parent_scope = json.loads(env["SCOPE_PARENT_SELECTIONS"])
        _, _, parent_chosen = measure(list(parent_scope), listed_paths(env.get("SCOPE_PARENT_FILES")), parent_scope)
        parent = json.dumps(parent_chosen, **compact)
    message = "jobs: " + (" ".join(jobs) or "none") + "; release checks: " + (" ".join(checks) or "none")
    print(message)
    with open(env["GITHUB_OUTPUT"], "a") as output:
        output.write(f"jobs={json.dumps(jobs, **compact)}\nrequired={json.dumps(required, **compact)}\n"
                     f"checks={' '.join(checks)}\nlint-cookbook={str(lints_cookbook(checks, scope_selections)).lower()}\n"
                     f"selections={json.dumps(chosen, **compact)}\nparent-selections={parent}\n")
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a") as output:
            output.write(message + "\n")


if __name__ == "__main__":
    main()
