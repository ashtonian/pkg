#!/usr/bin/env python3
"""Refuse tracked files that name private code, in their text or their path.

The repository is public, so the patterns that name private code are kept out
of it: CI reads them from the PRIVATE_REFERENCE_PATTERNS secret, and a local
run takes a file of them. Each non-blank line not starting with # is a POSIX
extended regular expression (git grep -E), matched case-insensitively. A
matching line is reported as path:line, and a matching path by its position
in `git ls-files`, never by its name. git's own messages are not repeated,
so no log carries a pattern or the text it matched.
"""
import os
from pathlib import Path
import subprocess
import sys
import tempfile

ENV = "PRIVATE_REFERENCE_PATTERNS"


def patterns(text):
    return [line.strip() for line in text.splitlines() if line.strip() and not line.lstrip().startswith("#")]


def git(cwd, *args):
    res = subprocess.run(["git", "-C", str(cwd), *args], capture_output=True, timeout=120)
    if res.returncode not in (0, 1):
        # git's message can quote a pattern; report only that it failed.
        raise RuntimeError(f"git {args[0]} exited {res.returncode}")
    return res


def grep(cwd, pats_file, *args):
    """(path, line) of each line `git grep -z` matches, paths verbatim."""
    res = git(cwd, "grep", "-z", "-n", "-I", "-i", "-E", "-f", pats_file, *args)
    if res.returncode == 1:
        return []
    hits, out = [], res.stdout
    # Each record is path NUL line NUL text LF; text holds neither NUL nor LF.
    while out:
        path, rest = out.split(b"\0", 1)
        number, rest = rest.split(b"\0", 1)
        _, _, out = rest.partition(b"\n")
        hits.append((path.decode("utf-8", "surrogateescape"), int(number)))
    return hits


def matches(root, pats):
    """Where pats match tracked files: path:line for a line, and `tracked
    path #N` (git ls-files order, from 1) for a path, whose name stays out
    of the report."""
    paths = [p.decode("utf-8", "surrogateescape") for p in git(root, "ls-files", "-z").stdout.split(b"\0") if p]
    with tempfile.TemporaryDirectory(prefix="private-references-") as tmp:
        pats_file = os.path.join(tmp, "patterns")
        Path(pats_file).write_text("\n".join(pats) + "\n")
        # One path per line: the line number is the path's position.
        Path(tmp, "paths").write_text("\n".join(p.replace("\n", " ") for p in paths) + "\n",
                                      errors="surrogateescape")
        named = {n for _, n in grep(tmp, pats_file, "--no-index", "--", "paths")}
        lines = grep(root, pats_file, "--", ".")
    position = {p: i + 1 for i, p in enumerate(paths)}
    out = [f"tracked path #{n}" for n in sorted(named)]
    for path, number in lines:
        n = position.get(path)
        out.append(f"tracked path #{n}:{number}" if n in named else f"{path}:{number}")
    return out


def main(argv, env, root):
    try:
        text = Path(argv[1]).read_text() if len(argv) > 1 else env.get(ENV, "")
    except OSError as err:
        print(f"reading private reference patterns: {err.strerror}", file=sys.stderr)
        return 2
    pats = patterns(text)
    if not pats:
        print(f"no private reference patterns: set {ENV} or pass a file of them", file=sys.stderr)
        return 2
    try:
        hits = matches(root, pats)
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as err:
        print(f"private reference check failed: {err}", file=sys.stderr)
        return 2
    for hit in hits:
        print(f"{hit}: names private code", file=sys.stderr)
    if hits:
        print(f"{len(hits)} place(s) name private code; describe it generically ('a downstream consumer'). "
              "Find tracked path #N with: git ls-files -z | tr '\\0' '\\n' | sed -n Np", file=sys.stderr)
        return 1
    print(f"no private references in tracked files ({len(pats)} patterns)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv, os.environ, Path(__file__).resolve().parent.parent))
