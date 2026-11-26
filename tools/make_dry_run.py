"""The commands the repository's Makefile would run, whatever the caller's environment.

make takes a `?=` variable's value from the environment when one is set, and
CI sets some of them for every job: ci.yml's UNIT_TEST_COUNT is -count=1 on
the nightly run. A test that reads a recipe passes the variables it means and
gets the Makefile's defaults for the rest, so it reads the same recipe on a
pull request, on the nightly run and in a shell that exports any of them.
"""
from pathlib import Path
import os
import re
import subprocess

ROOT = Path(__file__).resolve().parents[1]
MAKEFILE = ROOT / "Makefile"
# A parent make hands its command-line variables to a child through these.
MAKE_CONTROL = frozenset({"MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES", "GNUMAKEFLAGS", "MAKELEVEL"})
DEFAULTED = re.compile(r"^([A-Za-z_]\w*)\s*\?=", re.M)


def defaulted_variables(makefile_text):
    """The variables a Makefile assigns with ?=: the ones the environment overrides."""
    return frozenset(DEFAULTED.findall(makefile_text))


def environment(base=None):
    """base (the process environment by default) without what would override the Makefile."""
    drop = defaulted_variables(MAKEFILE.read_text()) | MAKE_CONTROL
    return {name: value for name, value in (os.environ if base is None else base).items() if name not in drop}


def dry_run(*targets, timeout=60, **variables):
    """What `make -s -n <targets> NAME=value…` prints, one command per line."""
    args = ["make", "-s", "-n", *targets, *(f"{name}={value}" for name, value in variables.items())]
    return subprocess.check_output(args, cwd=ROOT, env=environment(), timeout=timeout, text=True)
