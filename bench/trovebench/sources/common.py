"""Helpers shared by the task sources."""

from __future__ import annotations

import base64
import re
from pathlib import Path

from ..manifest import Task, dump_task

MAX_PATCH_B64 = 100_000   # a single `docker exec` argument is limited to ~128 KB


def split_diff(diff: str) -> list[tuple[str, str]]:
    """Split a unified git diff into (path, chunk) per file, keeping each chunk intact."""
    parts = re.split(r"(?m)^(?=diff --git )", diff)
    out = []
    for chunk in parts:
        m = re.match(r"diff --git a/(\S+) b/(\S+)", chunk)
        if m:
            out.append((m.group(2), chunk))
    return out


_TEST_RE = re.compile(r"(^|/)(tests?|testing|__tests__|spec)(/|$)|(^|/)test_[^/]*$|_test\.[a-z]+$|\.test\.[a-z]+$|\.spec\.[a-z]+$")


def is_test_path(path: str) -> bool:
    return bool(_TEST_RE.search(path.lower()))


def apply_patch_step(patch: str) -> str:
    """A shell step that applies `patch` in the repo root. Base64 keeps any content (quotes, heredocs) safe."""
    b64 = base64.b64encode(patch.encode()).decode()
    if len(b64) > MAX_PATCH_B64:
        raise ValueError(f"test patch is {len(b64)} bytes encoded, above the {MAX_PATCH_B64} limit")
    return f"echo {b64} | base64 -d | git apply --whitespace=nowarn -"


def write_task(out_dir: Path, task: Task, gold_patch: str | None = None) -> Path:
    """Writes <out_dir>/<task id>/task.yaml (+ gold.patch, used only by `validate`)."""
    d = out_dir / task.id
    d.mkdir(parents=True, exist_ok=True)
    (d / "task.yaml").write_text(dump_task(task), encoding="utf-8", newline="\n")
    if gold_patch is not None:
        (d / "gold.patch").write_text(gold_patch, encoding="utf-8", newline="\n")
    return d
