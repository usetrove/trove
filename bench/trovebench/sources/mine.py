"""Mine fresh tasks from recently closed GitHub issues.

The issue is the task; the tests added by the merged PR that closed it are the success check. Tasks newer than a
model's training cutoff are less likely to have been seen in training.

Auth: GITHUB_TOKEN from the environment (never stored or printed). Repos come from a curated allowlist file
(bench/repos.yaml) so we only pull from projects whose license we have checked:

    repos:
      - repo: owner/name
        image: python:3.12-slim
        setup: ["pip install -q -e . pytest"]
        test_cmd: "python -m pytest -q {test_files}"     # {test_files} = the PR's test files

A mined task is only *accepted* after `python -m trovebench validate` proves the tests fail on the base commit and
pass once the PR's source change is applied. Mined manifests stay local (gitignored).
"""

from __future__ import annotations

import re
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import Callable

import yaml

from ..manifest import Task
from .common import apply_patch_step, is_test_path, split_diff, write_task

API = "https://api.github.com"
PERMISSIVE = {"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "0BSD", "Unlicense"}
CLOSES_RE = re.compile(r"(?i)\b(?:fix(?:e[sd])?|close[sd]?|resolve[sd]?)\s+(?:[\w.-]+/[\w.-]+)?#(\d+)")

# get(url, accept) -> parsed JSON (dict/list) or text for the diff media type
Get = Callable[[str, str], object]


@dataclass(frozen=True)
class RepoSpec:
    repo: str
    image: str
    setup: tuple[str, ...]
    test_cmd: str


def load_repos(path: Path) -> list[RepoSpec]:
    data = yaml.safe_load(path.read_text(encoding="utf-8")) or {}
    out = []
    for e in data.get("repos", []):
        missing = [k for k in ("repo", "image", "test_cmd") if not e.get(k)]
        if missing:
            raise ValueError(f"{path}: repo entry {e.get('repo', '?')} is missing {', '.join(missing)}.")
        if "{test_files}" not in e["test_cmd"]:
            raise ValueError(f"{path}: test_cmd for {e['repo']} must contain {{test_files}}.")
        out.append(RepoSpec(e["repo"], e["image"], tuple(e.get("setup", [])), e["test_cmd"]))
    if not out:
        raise ValueError(f"{path}: no repos listed. Add entries under `repos:`.")
    return out


def _closing_issue(pr: dict) -> int | None:
    m = CLOSES_RE.search((pr.get("body") or "") + "\n" + (pr.get("title") or ""))
    return int(m.group(1)) if m else None


def mine_repo(spec: RepoSpec, get: Get, since: str, out_dir: Path, *, limit: int) -> tuple[list[Task], list[str]]:
    """Returns (accepted tasks, skip reasons). Every skip is explained so the user can see why."""
    tasks: list[Task] = []
    skips: list[str] = []
    meta = get(f"{API}/repos/{spec.repo}", "application/vnd.github+json")
    spdx = ((meta.get("license") or {}).get("spdx_id")) or "NONE"
    if spdx not in PERMISSIVE:
        return [], [f"{spec.repo}: license {spdx} is not in the allowed permissive set, skipped entirely"]
    since_dt = datetime.fromisoformat(since)
    page = 1
    while len(tasks) < limit and page <= 10:
        prs = get(f"{API}/repos/{spec.repo}/pulls?state=closed&sort=updated&direction=desc&per_page=50&page={page}",
                  "application/vnd.github+json")
        if not prs:
            break
        for pr in prs:
            if len(tasks) >= limit:
                break
            merged = pr.get("merged_at")
            if not merged or datetime.fromisoformat(merged.replace("Z", "+00:00")).replace(tzinfo=None) < since_dt:
                continue
            tag = f"{spec.repo}#{pr['number']}"
            num = _closing_issue(pr)
            if num is None:
                skips.append(f"{tag}: no 'fixes #N' issue reference")
                continue
            diff = get(f"{API}/repos/{spec.repo}/pulls/{pr['number']}", "application/vnd.github.v3.diff")
            files = split_diff(diff if isinstance(diff, str) else "")
            tests = [(p, c) for p, c in files if is_test_path(p)]
            src = [(p, c) for p, c in files if not is_test_path(p)]
            if not tests or not src:
                skips.append(f"{tag}: needs both source and test changes")
                continue
            commit = get(f"{API}/repos/{spec.repo}/commits/{pr['merge_commit_sha']}", "application/vnd.github+json")
            if not commit.get("parents"):
                skips.append(f"{tag}: merge commit has no parent")
                continue
            base = commit["parents"][0]["sha"]
            issue = get(f"{API}/repos/{spec.repo}/issues/{num}", "application/vnd.github+json")
            if not issue.get("body") and not issue.get("title"):
                skips.append(f"{tag}: issue #{num} is empty")
                continue
            test_patch = "".join(c for _, c in tests)
            try:
                grading = apply_patch_step(test_patch)
            except ValueError as e:
                skips.append(f"{tag}: {e}")
                continue
            test_files = " ".join(p for p, _ in tests)
            task = Task(
                id=f"mined-{spec.repo.replace('/', '-').lower()}-{pr['number']}",
                source="mined", repo=f"https://github.com/{spec.repo}", base_commit=base, license=spdx,
                prompt=f"{issue.get('title', '')}\n\n{issue.get('body') or ''}".strip(),
                image=spec.image, setup=spec.setup, grading_setup=(grading,),
                success_cmd=spec.test_cmd.format(test_files=test_files), timeout_s=1800,
                issue_url=issue.get("html_url", f"https://github.com/{spec.repo}/issues/{num}"),
                tags=("mined", f"merged:{merged[:10]}"),
                attribution=f"{spec.repo} issue #{num} and PR #{pr['number']}, license {spdx}.",
            )
            write_task(out_dir, task, "".join(c for _, c in src))
            tasks.append(task)
        page += 1
    return tasks, skips
