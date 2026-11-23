"""Bound the generated fuzz corpus the nightly fuzz job carries between runs.

go test -fuzz writes the inputs that widen coverage under
$(go env GOCACHE)/fuzz/<import path>/<FuzzTarget>/, one file per input,
and never removes one. The nightly job restores that directory from the
GitHub cache, fuzzes, and saves it again, so without a bound it would grow
every night. Before the save this keeps, in each target's directory, the
newest FUZZ_CORPUS_MAX_ENTRIES inputs, then removes the oldest inputs
anywhere until the whole corpus is at most FUZZ_CORPUS_MAX_BYTES.

usage: python3 tools/fuzz_corpus.py <corpus dir>
"""
import os
import sys
from pathlib import Path

DEFAULT_MAX_ENTRIES = 1000
DEFAULT_MAX_BYTES = 32 << 20


def bound(root, max_entries, max_bytes):
    """Prune root to max_entries files per directory and max_bytes in all,
    removing the oldest first (by modification time, then name). Returns
    the files kept and the bytes they hold."""
    if max_entries < 1 or max_bytes < 1:
        raise ValueError("the corpus bounds must be positive")
    root = Path(root)
    if not root.is_dir():
        return 0, 0
    kept = []
    for directory, _, names in os.walk(root):
        inputs = []
        for name in names:
            path = Path(directory, name)
            st = path.stat()
            inputs.append((st.st_mtime_ns, name, st.st_size, path))
        inputs.sort(reverse=True)
        for _, _, _, path in inputs[max_entries:]:
            path.unlink()
        kept.extend(inputs[:max_entries])
    kept.sort(reverse=True)
    total = sum(size for _, _, size, _ in kept)
    while kept and total > max_bytes:
        _, _, size, path = kept.pop()
        path.unlink()
        total -= size
    for directory, _, _ in os.walk(root, topdown=False):
        if directory != str(root) and not os.listdir(directory):
            os.rmdir(directory)
    return len(kept), total


def positive(name, default):
    value = os.environ.get(name, "")
    if value == "":
        return default
    try:
        n = int(value)
    except ValueError:
        n = 0
    if n < 1:
        sys.exit(f"fuzz_corpus: {name}={value!r} is not a positive integer")
    return n


def main(argv):
    if len(argv) != 2:
        sys.exit(__doc__.strip().splitlines()[-1])
    max_entries = positive("FUZZ_CORPUS_MAX_ENTRIES", DEFAULT_MAX_ENTRIES)
    max_bytes = positive("FUZZ_CORPUS_MAX_BYTES", DEFAULT_MAX_BYTES)
    files, size = bound(argv[1], max_entries, max_bytes)
    print(f"fuzz corpus {argv[1]}: {files} inputs, {size} bytes "
          f"(at most {max_entries} per target, {max_bytes} bytes in all)")


if __name__ == "__main__":
    main(sys.argv)
