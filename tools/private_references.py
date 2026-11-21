#!/usr/bin/env python3
"""Refuse tracked files that name private code.

The repository is public, so the patterns that name private code are kept out
of it: CI reads them from the PRIVATE_REFERENCE_PATTERNS secret, and a local
run takes a file of them. Each non-blank line not starting with # is a POSIX
extended regular expression (git grep -E), matched case-insensitively. A
match is reported as path:line and nothing more, and git's own messages are
not repeated, so no log carries a pattern or the text it matched.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ENV = "PRIVATE_REFERENCE_PATTERNS"


def patterns(text):
    return [line.strip() for line in text.splitlines() if line.strip() and not line.lstrip().startswith("#")]


def matches(root, pats):
    """The path:line of every tracked text line one of pats matches."""
    with tempfile.NamedTemporaryFile("w", prefix="private-references-", delete=False) as f:
        f.write("\n".join(pats) + "\n")
        path = f.name
    try:
        res = subprocess.run(
            ["git", "-C", str(root), "grep", "-n", "-I", "-i", "-E", "-f", path, "--", "."],
            capture_output=True, timeout=120,
        )
    finally:
        os.unlink(path)
    if res.returncode == 1:
        return []
    if res.returncode != 0:
        # git's message can quote a pattern; report only that it failed.
        raise RuntimeError(f"git grep exited {res.returncode}")
    hits = []
    for line in res.stdout.decode("utf-8", "replace").splitlines():
        file, number, _ = line.split(":", 2)
        hits.append(f"{file}:{number}")
    return hits


def main(argv, env, root):
    text = Path(argv[1]).read_text() if len(argv) > 1 else env.get(ENV, "")
    pats = patterns(text)
    if not pats:
        print(f"no private reference patterns: set {ENV} or pass a file of them", file=sys.stderr)
        return 2
    try:
        hits = matches(root, pats)
    except (RuntimeError, OSError, subprocess.SubprocessError) as err:
        print(f"private reference check failed: {err}", file=sys.stderr)
        return 2
    for hit in hits:
        print(f"{hit}: names private code", file=sys.stderr)
    if hits:
        print(f"{len(hits)} line(s) name private code; describe it generically ('a downstream consumer')", file=sys.stderr)
        return 1
    print(f"no private references in tracked files ({len(pats)} patterns)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv, os.environ, Path(__file__).resolve().parent.parent))
