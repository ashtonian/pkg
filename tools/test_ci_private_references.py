#!/usr/bin/env python3
import contextlib
import io
from pathlib import Path
import subprocess
import tempfile
import unittest

import private_references as refs


class PrivateReferences(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.root = Path(self.dir.name)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        self.write("README.md", "Imported by a downstream consumer.\nSee Codename/feature for more.\n")
        self.write("docs/guide.md", "nothing here\n")
        self.write("blob.bin", b"\x00codename\x00")
        self.write("untracked.md", "codename\n", track=False)

    def tearDown(self):
        self.dir.cleanup()

    def write(self, name, content, track=True):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(content, bytes):
            path.write_bytes(content)
        else:
            path.write_text(content)
        if track:
            subprocess.run(["git", "-C", str(self.root), "add", name], check=True)

    def run_main(self, env, *argv):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = refs.main(["private_references.py", *argv], env, self.root)
        return code, out.getvalue(), err.getvalue()

    def test_reports_tracked_text_lines_without_their_text(self):
        code, _, err = self.run_main({refs.ENV: "# private names\n\ncodename\n"})
        self.assertEqual(code, 1)
        self.assertIn("README.md:2: names private code", err)
        self.assertNotIn("Codename/feature", err, "the matched text is never printed")
        self.assertNotIn("blob.bin", err, "binary files are skipped")
        self.assertNotIn("untracked.md", err, "only tracked files are checked")

    def test_patterns_are_case_insensitive_extended_expressions(self):
        self.assertEqual(refs.matches(self.root, ["CODE(NAME|WORD)/"]), ["README.md:2"])
        self.assertEqual(refs.matches(self.root, ["absent"]), [])

    def test_a_file_of_patterns_replaces_the_environment(self):
        path = self.root.parent / (self.root.name + "-patterns")
        path.write_text("downstream\n")
        try:
            code, _, err = self.run_main({refs.ENV: "codename"}, str(path))
        finally:
            path.unlink()
        self.assertEqual(code, 1)
        self.assertIn("README.md:1:", err)
        self.assertNotIn("README.md:2:", err)

    def test_clean_files_pass(self):
        code, out, _ = self.run_main({refs.ENV: "absent\nnowhere"})
        self.assertEqual(code, 0)
        self.assertIn("2 patterns", out)

    def test_no_patterns_fail_closed(self):
        for env in ({}, {refs.ENV: ""}, {refs.ENV: "# only a comment\n \n"}):
            code, _, err = self.run_main(env)
            self.assertEqual(code, 2, env)
            self.assertIn(refs.ENV, err)

    def test_a_path_that_names_private_code_is_reported_by_position(self):
        self.write("docs/codename-notes.md", "clean text\nCodename again\n")
        code, _, err = self.run_main({refs.ENV: "codename"})
        self.assertEqual(code, 1)
        paths = subprocess.run(["git", "-C", str(self.root), "ls-files", "-z"], check=True, capture_output=True).stdout.split(b"\0")
        n = paths.index(b"docs/codename-notes.md") + 1
        self.assertIn(f"tracked path #{n}: names private code", err)
        self.assertIn(f"tracked path #{n}:2: names private code", err, "a line in such a path is reported by position too")
        self.assertIn("README.md:2: names private code", err)
        self.assertNotIn("codename-notes", err, "the path's name is never printed")

    def test_unusual_paths_are_reported_verbatim(self):
        self.write("a:b.md", "x\ncodename\n")
        self.write("caf\u00e9.md", "codename\n")
        self.assertEqual(sorted(refs.matches(self.root, ["codename"])), ["README.md:2", "a:b.md:2", "caf\u00e9.md:1"])

    def test_a_missing_pattern_file_fails_closed(self):
        code, _, err = self.run_main({refs.ENV: "codename"}, str(self.root / "absent-patterns"))
        self.assertEqual(code, 2)
        self.assertIn("reading private reference patterns", err)

    def test_a_bad_pattern_fails_without_repeating_it(self):
        code, _, err = self.run_main({refs.ENV: "secret(unclosed"})
        self.assertEqual(code, 2)
        self.assertNotIn("secret", err)


if __name__ == "__main__":
    unittest.main()
