#!/usr/bin/env python3
"""Reuse complete PR or owner-dispatched validation of identical merge contents."""
import argparse
import datetime
import hashlib
import io
import json
import os
import re
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile

MAX_BYTES = 128 * 1024
JOBS = {"test", "parse-postgres", "parse-sdk", "httpx-minimum", "gitx-minimum", "modules", "lint"}


def git(*args):
    return subprocess.check_output(["git", *args], timeout=15, stderr=subprocess.PIPE).decode().strip()


def source():
    fields = git("rev-list", "--parents", "-n", "1", "HEAD").split()
    return {"sha": fields[0], "parents": fields[1:], "tree": git("rev-parse", "HEAD^{tree}")}


def policy(env):
    variables = json.loads(env["CI_VARIABLES_JSON"])
    if not isinstance(variables, dict):
        raise ValueError("invalid repository variables")
    return {"workflow": hashlib.sha256(Path(".github/workflows/ci.yml").read_bytes()).hexdigest(),
            "variables": hashlib.sha256(json.dumps(variables, sort_keys=True).encode()).hexdigest()}


def record(env, output):
    identity = source()
    event = json.loads(Path(env["GITHUB_EVENT_PATH"]).read_text())
    event_name = env["GITHUB_EVENT_NAME"]
    if identity["sha"] != env["GITHUB_SHA"] or env["GITHUB_ACTOR_ID"] != "4212452":
        raise ValueError("receipt is not for this owner-validated checkout")
    if event_name == "pull_request":
        pr = event["pull_request"]
        if (pr["head"]["repo"]["full_name"] != env["GITHUB_REPOSITORY"]
                or pr["base"]["repo"]["full_name"] != env["GITHUB_REPOSITORY"]
                or len(identity["parents"]) != 2 or identity["parents"][1] != pr["head"]["sha"]):
            raise ValueError("checkout is not this same-repository PR merge")
    elif event_name != "workflow_dispatch":
        raise ValueError("only PR and explicit validation runs produce receipts")
    results = json.loads(env["VALIDATION_JOBS"])
    if set(results) != JOBS | {"scope"}:
        raise ValueError("incomplete validation job inventory")
    runtime = results["scope"]["outputs"]["runtime"]
    for name, job in results.items():
        allowed = {"success", "skipped"} if runtime == "false" and name not in {"scope", "modules"} else {"success"}
        if job["result"] not in allowed:
            raise ValueError("validation did not pass: " + name)
    receipt = {"version": 1, "repository": env["GITHUB_REPOSITORY"], "event": event_name,
               "source": identity, "policy": policy(env), "runtime": runtime,
               "run_id": int(env["GITHUB_RUN_ID"]), "attempt": int(env["GITHUB_RUN_ATTEMPT"])}
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(receipt, sort_keys=True) + "\n")


def matches(receipt, run, identity, env):
    if (receipt.get("version") != 1 or receipt.get("repository") != env["GITHUB_REPOSITORY"]
            or receipt.get("policy") != policy(env) or receipt.get("runtime") != env["RUNTIME_REQUIRED"]
            or receipt.get("run_id") != run["id"] or receipt.get("attempt") != run["run_attempt"]
            or receipt.get("event") != run["event"] or receipt.get("source", {}).get("tree") != identity["tree"]):
        return False
    if run["event"] == "pull_request":
        return receipt["source"].get("parents") == identity["parents"]
    return run["event"] == "workflow_dispatch" and receipt["source"].get("sha") == identity["parents"][1]


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class API:
    def __init__(self, env):
        self.base = env.get("GITHUB_API_URL", "https://api.github.com").rstrip("/") + "/repos/" + env["GITHUB_REPOSITORY"] + "/"
        self.token = env["GITHUB_TOKEN"]
        self.artifact_tool = env.get("CI_ARTIFACT_TOOL")
        self.deadline = time.monotonic() + 100

    def read(self, url, auth=True, redirect=True, limit=4 * 1024 * 1024):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("acceptance lookup deadline")
        headers = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
        if auth:
            headers["Authorization"] = "Bearer " + self.token
        opener = urllib.request.build_opener() if redirect else urllib.request.build_opener(NoRedirect())
        with opener.open(urllib.request.Request(url, headers=headers), timeout=min(10, remaining)) as response:
            data = response.read(limit + 1)
        if len(data) > limit:
            raise ValueError("oversized acceptance evidence")
        return data

    def get(self, path, **query):
        artifact_run = re.fullmatch(r"actions/runs/([0-9]+)/artifacts", path)
        if artifact_run and self.artifact_tool:
            artifacts = json.loads(self.artifact_command("list", "--run-id", artifact_run[1]))
            if artifacts:
                return {"artifacts": [{"name": item["name"], "expired": False,
                                       "seaweed": item} for item in artifacts]}
            # Existing GitHub receipts retain their original lifetime. New runs
            # publish only to SeaweedFS; a backend error above fails acceptance.
        return json.loads(self.read(self.base + path + ("?" + urllib.parse.urlencode(query) if query else "")))

    def artifact_command(self, *args):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("acceptance lookup deadline")
        return subprocess.check_output([sys.executable, self.artifact_tool, *args], timeout=remaining)

    def receipt(self, artifact):
        if item := artifact.get("seaweed"):
            if item["size"] > MAX_BYTES:
                raise ValueError("oversized receipt archive")
            with tempfile.TemporaryDirectory(prefix="ci-acceptance-") as directory:
                archive_path = Path(directory) / "receipt.zip"
                self.artifact_command("download", "--run-id", item["run_id"],
                                      "--run-attempt", item["run_attempt"], "--name", item["name"],
                                      "--path", str(archive_path), "--archive", "--max-bytes", str(MAX_BYTES))
                data = archive_path.read_bytes()
            return self.decode_receipt(data)
        try:
            data = self.read(self.base + f"actions/artifacts/{int(artifact['id'])}/zip", redirect=False, limit=MAX_BYTES)
        except urllib.error.HTTPError as error:
            if error.code not in (301, 302, 303, 307, 308):
                raise
            location = error.headers["Location"]
            error.close()
            if urllib.parse.urlsplit(location).scheme != "https":
                raise ValueError("receipt download is not HTTPS")
            data = self.read(location, auth=False, limit=MAX_BYTES)
        return self.decode_receipt(data)

    @staticmethod
    def decode_receipt(data):
        if len(data) > MAX_BYTES:
            raise ValueError("oversized receipt archive")
        with zipfile.ZipFile(io.BytesIO(data)) as archive:
            if archive.namelist() != ["acceptance.json"] or archive.getinfo("acceptance.json").file_size > MAX_BYTES:
                raise ValueError("unexpected receipt archive")
            return json.loads(archive.read("acceptance.json"))


def verify(env, api=None):
    if env.get("GITHUB_RUN_ATTEMPT") != "1":
        return False, "explicit reruns execute validation"
    if env.get("GITHUB_EVENT_NAME") != "push" or env.get("GITHUB_REF") != "refs/heads/main":
        return False, "this event requires validation"
    try:
        identity = source()
        if (identity["sha"] != env["GITHUB_SHA"] or len(identity["parents"]) != 2
                or identity["parents"][0] != env["BEFORE"]):
            return False, "push is not a single tested merge"
        api = api or API(env)
        runs = api.get("actions/workflows/ci.yml/runs", head_sha=identity["parents"][1], per_page=100)["workflow_runs"]
        runs = [r for r in runs if r["event"] in {"pull_request", "workflow_dispatch"}
                and r["head_sha"] == identity["parents"][1]
                and r.get("repository", {}).get("full_name") == env["GITHUB_REPOSITORY"]
                and r.get("head_repository", {}).get("full_name") == env["GITHUB_REPOSITORY"]]
        if not runs:
            return False, "no prior validation for this head"
        run = max(runs, key=lambda r: (r["updated_at"], r["id"]))
        age = time.time() - datetime.datetime.fromisoformat(run["updated_at"].replace("Z", "+00:00")).timestamp()
        if run["status"] != "completed" or run["conclusion"] != "success" or not 0 <= age <= 6 * 3600:
            return False, "latest validation is incomplete, failed or stale"
        artifacts = api.get(f"actions/runs/{run['id']}/artifacts", per_page=100)["artifacts"]
        for artifact in artifacts:
            if artifact.get("name") == f"acceptance-{run['run_attempt']}" and not artifact.get("expired"):
                if matches(api.receipt(artifact), run, identity, env):
                    return True, f"matching validation: {run['html_url']}"
        return False, "no matching receipt"
    except (KeyError, TypeError, ValueError, AttributeError, OSError, subprocess.SubprocessError, zipfile.BadZipFile):
        return False, "acceptance evidence unavailable: all selected checks run"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["record", "verify"])
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    if args.command == "record":
        if args.output is None:
            parser.error("record requires --output")
        record(os.environ, args.output)
        return
    reused, reason = verify(os.environ)
    print(reason)
    with open(os.environ["GITHUB_OUTPUT"], "a") as output:
        output.write(f"reused={str(reused).lower()}\n")
    if summary := os.environ.get("GITHUB_STEP_SUMMARY"):
        with open(summary, "a") as output:
            output.write(reason + "\n")


if __name__ == "__main__":
    main()
