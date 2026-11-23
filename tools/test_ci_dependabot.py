"""Dependabot updates every dependency manifest the repository tracks.

.github/dependabot.yml names its directories one by one, so a new Go module,
pnpm project or composite action goes without updates until it is added
there. These tests derive each list from the tracked tree and fail when the
configuration misses a directory or names one that holds no manifest.
"""
from pathlib import Path, PurePosixPath
import re
import subprocess
import unittest

from test_ci_release_checks import tracked_dirs

ROOT = Path(__file__).resolve().parents[1]
CONFIG = (ROOT / ".github/dependabot.yml").read_text()
# An update entry: its ecosystem, then its indented keys.
ENTRY = re.compile(r"^  - package-ecosystem: (\S+)\n((?:(?:    .*)?\n)*)", re.M)


def updates():
    """Each ecosystem's directories, read from dependabot.yml's block layout."""
    entries = {}
    for ecosystem, body in ENTRY.findall(CONFIG):
        listed = re.search(r"^    directories:\n((?:      - \S+\n)+)", body, re.M)
        single = re.search(r"^    directory: (\S+)$", body, re.M)
        if bool(listed) == bool(single):
            raise AssertionError(f"{ecosystem}: name its directories in one directories list")
        if ecosystem in entries:
            raise AssertionError(f"{ecosystem} has two entries; give it one, with every directory")
        entries[ecosystem] = [line.split("- ", 1)[1] for line in listed[1].splitlines()] if listed else [single[1]]
    if len(entries) != CONFIG.count("package-ecosystem:"):
        raise AssertionError("an update entry is not laid out as the others are, so this test cannot read it")
    return entries


def configured(directories):
    """Repository-relative directories as dependabot.yml writes them."""
    return sorted({"/" if str(d) == "." else f"/{d}" for d in directories})


class Dependabot(unittest.TestCase):
    def setUp(self):
        self.updates = updates()

    def test_every_go_module_is_updated(self):
        # The walk the documentation check reads modules by: published modules,
        # the examples, httpx's benchmarks and the tools alike.
        modules = configured(tracked_dirs("go.mod"))
        self.assertIn("/parse", modules)
        self.assertEqual(self.updates["gomod"], modules)

    def test_every_pnpm_project_is_updated(self):
        projects = tracked_dirs("package.json")
        for project in projects:
            with self.subTest(project=str(project)):
                self.assertTrue((ROOT / project / "pnpm-lock.yaml").is_file(),
                                "the repository's Node.js projects lock their dependencies with pnpm")
        self.assertIn("/parse/sdk/ts", configured(projects))
        self.assertEqual(self.updates["npm"], configured(projects))

    def test_every_composite_action_is_updated(self):
        # "/" covers .github/workflows and a root action.yml; any other
        # action.yml is read only from its own directory.
        tracked = subprocess.check_output(["git", "ls-files", "-z", "--", "*action.yml", "*action.yaml"],
                                          cwd=ROOT).decode().split("\0")
        actions = [PurePosixPath(p).parent for p in tracked if PurePosixPath(p).name in ("action.yml", "action.yaml")]
        self.assertEqual(self.updates["github-actions"], configured(actions + [PurePosixPath(".")]))


if __name__ == "__main__":
    unittest.main()
