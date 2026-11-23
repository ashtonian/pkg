"""tools/postgres-up.sh, which make parse-postgres-up and cookbook-postgres-up
run, against a fake docker: a server that stops while starting, or is not
ready in time, is reported with its log and removed; a container docker
refused to create, such as another's of the same name, is never touched; and
nothing reaches stdout, which the make targets leave to their exports.
"""
import os
import subprocess
import tempfile
import textwrap
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "tools" / "postgres-up.sh"

# The fake records each call on a line of $FAKE_DOCKER_CALLS. FAKE_DOCKER_RUN
# is how docker run ends: "ok", "conflict" (refused before creating the
# container) or "port" (created, then failed to start it). FAKE_DOCKER_STATUS
# is what docker inspect reports, and FAKE_DOCKER_READY whether pg_isready
# passes.
FAKE_DOCKER = textwrap.dedent("""\
    #!/usr/bin/env bash
    echo "$*" >>"$FAKE_DOCKER_CALLS"
    case $1 in
    run)
        cidfile=''
        while [ $# -gt 0 ]; do
            [ "$1" = --cidfile ] && cidfile=$2
            shift
        done
        case $FAKE_DOCKER_RUN in
        ok) echo c0ffee >"$cidfile"; echo c0ffee ;;
        conflict) echo 'docker: Conflict. The container name is already in use' >&2; exit 125 ;;
        port) echo c0ffee >"$cidfile"; echo 'docker: port is already allocated' >&2; exit 125 ;;
        esac
        ;;
    inspect) echo "$FAKE_DOCKER_STATUS (exit code 1)" ;;
    exec) [ "$FAKE_DOCKER_READY" = 1 ] ;;
    logs) echo 'FATAL:  the server log' ;;
    rm) ;;
    esac
""")


class PostgresUp(unittest.TestCase):
    def run_script(self, run="ok", status="running", ready="1", ready_seconds="30"):
        with tempfile.TemporaryDirectory() as tmp:
            bin_dir = Path(tmp, "bin")
            bin_dir.mkdir()
            docker = bin_dir / "docker"
            docker.write_text(FAKE_DOCKER)
            docker.chmod(0o755)
            calls = Path(tmp, "calls")
            calls.touch()
            env = dict(os.environ, PATH=f"{bin_dir}{os.pathsep}{os.environ['PATH']}", FAKE_DOCKER_CALLS=str(calls),
                       FAKE_DOCKER_RUN=run, FAKE_DOCKER_STATUS=status, FAKE_DOCKER_READY=ready)
            result = subprocess.run(
                ["bash", str(SCRIPT), "--name", "pg-test", "--image", "postgres:19", "--port", "55499",
                 "--user", "parse", "--ready-seconds", ready_seconds],
                env=env, capture_output=True, text=True, timeout=60)
            return result, calls.read_text().splitlines()

    def test_outcomes(self):
        tests = [
            # name, fake docker, exit code, stderr holds, the container removed
            ("ready", {}, 0, None, False),
            ("stops while starting", {"status": "exited"}, 1, "pg-test: stopped while starting: exited (exit code 1)", True),
            ("not ready in time", {"ready": "0", "ready_seconds": "1"}, 1, "pg-test: not ready after 1s", True),
            ("name in use", {"run": "conflict"}, 1, "pg-test: docker run failed", False),
            ("port in use", {"run": "port"}, 1, "pg-test: docker run failed", True),
        ]
        for name, fake, code, message, removed in tests:
            with self.subTest(name):
                result, calls = self.run_script(**fake)
                self.assertEqual(result.returncode, code, result.stderr)
                self.assertEqual(result.stdout, "")
                if message:
                    self.assertIn(message, result.stderr)
                self.assertEqual("rm -f c0ffee" in calls, removed, calls)
                self.assertEqual("FATAL:  the server log" in result.stderr, removed, "the log of a removed container")
                self.assertFalse(any(c.startswith("rm") and c != "rm -f c0ffee" for c in calls), calls)

    def test_starts_without_rm_so_a_failed_start_keeps_its_log(self):
        _, calls = self.run_script()
        self.assertTrue(calls[0].startswith("run -d --cidfile "), calls)
        self.assertNotIn("--rm", calls[0])

    def test_refuses_a_wait_that_is_no_positive_number_of_seconds(self):
        for seconds in ("0", "ten", "-1", ""):
            with self.subTest(seconds=seconds):
                result, calls = self.run_script(ready_seconds=seconds)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(calls, [])


if __name__ == "__main__":
    unittest.main()
