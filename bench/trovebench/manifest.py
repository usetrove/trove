"""Task manifest: one YAML file per task.

A manifest says how to build the environment, what to ask the agent and how to
decide success. Validation is strict so a typo fails loudly before any money is
spent.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, fields, replace
from pathlib import Path
from typing import Any

import yaml

ID_RE = re.compile(r"^[a-z0-9][a-z0-9._-]{0,127}$")
SOURCES = {"swebench", "polyglot", "mined", "smoke"}


class ManifestError(ValueError):
    """Raised with a plain-English message that says which file and field to fix."""


@dataclass(frozen=True)
class Task:
    id: str
    source: str
    repo: str                 # repo URL, or a local path for hand-made tasks
    base_commit: str          # exact commit the agent starts from
    license: str              # SPDX id of the repo, or "see-upstream"
    prompt: str               # what the agent is asked to do
    image: str                # base Docker image, e.g. python:3.12-slim
    success_cmd: str          # exit code 0 means the task is solved
    timeout_s: int            # budget for setup plus the agent run
    setup: tuple[str, ...] = ()           # run once after checkout, before the agent
    grading_setup: tuple[str, ...] = ()   # run after the agent, before success_cmd (e.g. apply hidden tests)
    issue_url: str = ""
    dataset_commit: str = ""  # pinned revision of the dataset this came from
    tags: tuple[str, ...] = ()
    attribution: str = ""     # required credit line for upstream content
    base_dir: str = ""        # directory of the manifest file; set by the loader, never read from YAML


_REQUIRED = ("id", "source", "repo", "base_commit", "license", "prompt", "image", "success_cmd", "timeout_s")
_LIST_FIELDS = ("setup", "grading_setup", "tags")


def parse_task(data: Any, origin: str = "<memory>") -> Task:
    if not isinstance(data, dict):
        raise ManifestError(f"{origin}: a manifest must be a YAML mapping of fields, got {type(data).__name__}.")
    known = {f.name for f in fields(Task)} - {"base_dir"}
    unknown = sorted(set(data) - known)
    if unknown:
        raise ManifestError(f"{origin}: unknown field(s) {', '.join(unknown)}. Allowed: {', '.join(sorted(known))}.")
    missing = [k for k in _REQUIRED if data.get(k) in (None, "")]
    if missing:
        raise ManifestError(f"{origin}: missing required field(s) {', '.join(missing)}.")

    values = dict(data)
    for key in _LIST_FIELDS:
        raw = values.get(key, [])
        if raw is None:
            raw = []
        if not isinstance(raw, list) or not all(isinstance(x, str) for x in raw):
            raise ManifestError(f"{origin}: '{key}' must be a list of strings.")
        values[key] = tuple(raw)
    for key in ("id", "source", "repo", "base_commit", "license", "prompt", "image", "success_cmd",
                "issue_url", "dataset_commit", "attribution"):
        if key in values and not isinstance(values[key], str):
            raise ManifestError(f"{origin}: '{key}' must be a string.")
    if isinstance(values["timeout_s"], bool) or not isinstance(values["timeout_s"], int) or values["timeout_s"] <= 0:
        raise ManifestError(f"{origin}: 'timeout_s' must be a positive whole number of seconds.")
    if not ID_RE.match(values["id"]):
        raise ManifestError(
            f"{origin}: id '{values['id']}' must be lowercase letters, digits, '.', '_' or '-' (it names a directory)."
        )
    if values["source"] not in SOURCES:
        raise ManifestError(f"{origin}: source '{values['source']}' must be one of {', '.join(sorted(SOURCES))}.")
    return Task(**values)


def load_task(path: Path) -> Task:
    try:
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
    except yaml.YAMLError as e:
        raise ManifestError(f"{path}: not valid YAML ({e}). Fix the syntax and retry.") from e
    task = parse_task(data, str(path))
    return replace(task, base_dir=str(path.resolve().parent))


def load_tasks(root: Path) -> list[Task]:
    """Load every *.yaml under root, sorted by id. Duplicate ids are an error."""
    tasks = [load_task(p) for p in sorted(root.rglob("*.yaml"))]
    seen: dict[str, int] = {}
    for t in tasks:
        seen[t.id] = seen.get(t.id, 0) + 1
    dups = sorted(i for i, n in seen.items() if n > 1)
    if dups:
        raise ManifestError(f"{root}: duplicate task id(s) {', '.join(dups)}. Ids must be unique.")
    return sorted(tasks, key=lambda t: t.id)


def dump_task(task: Task) -> str:
    """Stable YAML for a task (used by the sources and for hashing)."""
    out: dict[str, Any] = {}
    for f in fields(Task):
        if f.name == "base_dir":
            continue
        v = getattr(task, f.name)
        out[f.name] = list(v) if isinstance(v, tuple) else v
    return yaml.safe_dump(out, sort_keys=False, allow_unicode=True)
