"""Bounded GitHub REST reads for CI tooling, with one deadline per process."""
import json
import time
import urllib.parse
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class API:
    def __init__(self, env, deadline_seconds=100):
        self.base = env.get("GITHUB_API_URL", "https://api.github.com").rstrip("/") + "/repos/" + env["GITHUB_REPOSITORY"] + "/"
        self.token = env["GITHUB_TOKEN"]
        self.deadline = time.monotonic() + deadline_seconds

    def remaining(self):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("GitHub API deadline")
        return remaining

    def read(self, url, auth=True, redirect=True, limit=4 * 1024 * 1024):
        remaining = self.remaining()
        headers = {"Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
        if auth:
            headers["Authorization"] = "Bearer " + self.token
        opener = urllib.request.build_opener() if redirect else urllib.request.build_opener(NoRedirect())
        with opener.open(urllib.request.Request(url, headers=headers), timeout=min(10, remaining)) as response:
            data = response.read(limit + 1)
        if len(data) > limit:
            raise ValueError("oversized GitHub API response")
        return data

    def get(self, path, **query):
        return json.loads(self.read(self.base + path + ("?" + urllib.parse.urlencode(query) if query else "")))
