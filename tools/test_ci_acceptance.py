"""Only complete, matching merge validation may replace repeated CI work."""
import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import urllib.error
import zipfile

import ci_acceptance as ci

SOURCE = {"sha": "a" * 40, "parents": ["b" * 40, "c" * 40], "tree": "d" * 40}
ENV = {"GITHUB_RUN_ATTEMPT": "1", "GITHUB_EVENT_NAME": "push", "GITHUB_REF": "refs/heads/main", "GITHUB_SHA": "a" * 40,
       "GITHUB_REPOSITORY": "o/r", "BEFORE": "b" * 40, "CI_VARIABLES_JSON": "{}", "RUNTIME_REQUIRED": "true"}
RUN = {"id": 4, "run_attempt": 2, "event": "pull_request", "head_sha": "c" * 40,
       "status": "completed", "conclusion": "success", "updated_at": "2026-11-07T06:45:00Z",
       "repository": {"full_name": "o/r"}, "head_repository": {"full_name": "o/r"}, "html_url": "https://example/run/4"}


class Acceptance(unittest.TestCase):
    def setUp(self):
        self.receipt = {"version": 1, "repository": "o/r", "event": "pull_request", "source": copy.deepcopy(SOURCE),
                        "policy": {"workflow": "policy", "variables": "vars"}, "runtime": "true", "run_id": 4, "attempt": 2}
        self.run = copy.deepcopy(RUN)
        self.artifacts = [{"id": 1, "name": "acceptance-2", "expired": False}]
        patches = [patch.object(ci, "source", return_value=copy.deepcopy(SOURCE)),
                   patch.object(ci, "policy", return_value=copy.deepcopy(self.receipt["policy"])),
                   patch.object(ci.time, "time", return_value=1794034800)]
        for p in patches:
            p.start(); self.addCleanup(p.stop)

    def verify(self, env=None):
        outer = self
        class API:
            def get(self, path, **query):
                return {"workflow_runs": [outer.run]} if path.endswith("/runs") else {"artifacts": outer.artifacts}
            def receipt(self, artifact):
                return outer.receipt
        return ci.verify(env or ENV, API())[0]

    def test_explicit_rerun_cannot_hide_an_older_failure(self):
        self.assertFalse(self.verify({**ENV, "GITHUB_RUN_ATTEMPT": "2"}))

    def test_identical_merge_reuses_pr_validation(self):
        self.assertTrue(self.verify())

    def test_owner_dispatch_can_validate_bot_head_without_omitting_merge_content(self):
        self.run["event"] = self.receipt["event"] = "workflow_dispatch"
        self.receipt["source"]["sha"] = SOURCE["parents"][1]
        self.assertTrue(self.verify())
        self.receipt["source"]["tree"] = "e" * 40
        self.assertFalse(self.verify())

    def test_changed_inputs_or_scope_cannot_reuse(self):
        for field, key, value in (("source", "parents", ["e" * 40, "c" * 40]), ("source", "tree", "e" * 40),
                                  ("policy", "workflow", "changed"), ("policy", "variables", "changed")):
            with self.subTest(field=field, key=key):
                previous = copy.deepcopy(self.receipt)
                self.receipt[field][key] = value
                self.assertFalse(self.verify())
                self.receipt = previous
        self.assertFalse(self.verify({**ENV, "RUNTIME_REQUIRED": "false"}))

    def test_failure_staleness_foreign_repo_and_missing_evidence_run_checks(self):
        for key, value in (("conclusion", "failure"), ("conclusion", "cancelled"), ("status", "in_progress"),
                           ("updated_at", "2026-11-01T00:00:00Z"), ("updated_at", "2026-11-09T00:00:00Z"),
                           ("head_repository", {"full_name": "fork/r"}), ("head_sha", "e" * 40), ("run_attempt", 3)):
            with self.subTest(key=key):
                self.run = {**RUN, key: value}
                self.assertFalse(self.verify())
        self.run = dict(RUN)
        self.artifacts = []
        self.assertFalse(self.verify())

    def test_manual_pr_and_direct_push_always_validate(self):
        for key, value in (("GITHUB_EVENT_NAME", "workflow_dispatch"), ("GITHUB_EVENT_NAME", "pull_request"),
                           ("GITHUB_REF", "refs/heads/work"), ("BEFORE", "e" * 40)):
            self.assertFalse(self.verify({**ENV, key: value}))
        with patch.object(ci, "source", return_value={**SOURCE, "parents": ["b" * 40]}):
            self.assertFalse(self.verify())

    def test_receipt_requires_all_selected_jobs_and_archive_examples(self):
        with tempfile.TemporaryDirectory() as directory:
            event = Path(directory, "event.json")
            event.write_text('{}')
            jobs = {name: {"result": "success"} for name in ci.JOBS | {"scope"}}
            jobs["scope"]["outputs"] = {"runtime": "true"}
            env = {**ENV, "GITHUB_EVENT_NAME": "workflow_dispatch", "GITHUB_ACTOR_ID": "4212452",
                   "GITHUB_RUN_ID": "4", "GITHUB_RUN_ATTEMPT": "2", "GITHUB_EVENT_PATH": str(event)}
            output = Path(directory, "acceptance.json")
            ci.record({**env, "VALIDATION_JOBS": json.dumps(jobs)}, output)
            for name in ci.JOBS:
                jobs[name]["result"] = "skipped"
                with self.assertRaises(ValueError):
                    ci.record({**env, "VALIDATION_JOBS": json.dumps(jobs)}, output)
                jobs[name]["result"] = "success"
            jobs["scope"]["outputs"]["runtime"] = "false"
            for name in ci.JOBS - {"modules"}:
                jobs[name]["result"] = "skipped"
            ci.record({**env, "VALIDATION_JOBS": json.dumps(jobs)}, output)
            jobs["modules"]["result"] = "skipped"
            with self.assertRaises(ValueError):
                ci.record({**env, "VALIDATION_JOBS": json.dumps(jobs)}, output)

    def test_receipt_download_does_not_forward_credentials_to_storage(self):
        data = io.BytesIO()
        with zipfile.ZipFile(data, 'w') as archive:
            archive.writestr('acceptance.json', '{}')
        requests = []
        class Opener:
            def open(self, request, timeout):
                requests.append(request)
                if len(requests) == 1:
                    raise urllib.error.HTTPError(request.full_url, 302, 'redirect', {'Location': 'https://storage.example/receipt'}, None)
                return io.BytesIO(data.getvalue())
        with patch('urllib.request.build_opener', return_value=Opener()):
            self.assertEqual(ci.API({**ENV, 'GITHUB_TOKEN': 'private'}).receipt({'id': 1}), {})
        self.assertEqual(requests[0].get_header('Authorization'), 'Bearer private')
        self.assertIsNone(requests[1].get_header('Authorization'))


class Wiring(unittest.TestCase):
    def test_post_merge_benchmarks_do_not_reuse_pr_receipts(self):
        text = (Path(__file__).resolve().parents[1] / '.github/workflows/ci.yml').read_text()
        benchmark = text.split('  parse-benchmarks:\n', 1)[1].split('  httpx-minimum:\n', 1)[0]
        self.assertNotIn('outputs.reused', benchmark)
        self.assertIn('outputs.runtime', benchmark)
        self.assertIn("github.event_name != 'pull_request'", benchmark)
        self.assertIn('test_ci_*.py', text)


if __name__ == '__main__':
    unittest.main()
