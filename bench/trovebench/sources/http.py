"""Tiny HTTP GET with plain-English errors. The GitHub token is sent only to api.github.com."""

from __future__ import annotations

import json
import os
import urllib.error
import urllib.request

from ..redact import redact


class FetchError(RuntimeError):
    pass


def get(url: str, accept: str = "application/json") -> object:
    headers = {"Accept": accept, "User-Agent": "trovebench"}
    token = os.environ.get("GITHUB_TOKEN", "")
    if url.startswith("https://api.github.com/") and token:
        headers["Authorization"] = f"Bearer {token}"
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=60) as r:
            body = r.read().decode("utf-8", errors="replace")
    except urllib.error.HTTPError as e:
        hint = ""
        if e.code in (401, 403) and url.startswith("https://api.github.com/"):
            hint = " Set GITHUB_TOKEN to a token with public-repo read access, and check your API rate limit."
        raise FetchError(redact(f"GET {url} failed with HTTP {e.code}.{hint}", (token,))) from e
    except urllib.error.URLError as e:
        raise FetchError(redact(f"Could not reach {url}: {e.reason}. Check your network connection.", (token,))) from e
    if "json" in accept and "diff" not in accept:
        return json.loads(body)
    return body
