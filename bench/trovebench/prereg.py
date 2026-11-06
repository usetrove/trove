"""Pre-registration lock: freeze a study file before any run, and refuse to analyse a changed one."""

from __future__ import annotations

import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path

from .study import Study, StudyError, load_study


def lock_path(study_path: Path) -> Path:
    return study_path.with_suffix(".lock.json")


def _git_commit(cwd: Path) -> str | None:
    try:
        out = subprocess.run(["git", "-C", str(cwd), "rev-parse", "HEAD"], capture_output=True, text=True, timeout=10)
    except (OSError, subprocess.SubprocessError):
        return None
    return out.stdout.strip() or None if out.returncode == 0 else None


def write_lock(study_path: Path, *, now: datetime | None = None) -> Path:
    """Record the study file's hash, the repo commit and the time. Refuses to overwrite a lock
    for a different file: a registered study is changed by registering a new study id."""
    study = load_study(study_path)
    lp = lock_path(study_path)
    if lp.exists():
        old = json.loads(lp.read_text(encoding="utf-8"))
        if old.get("study_sha256") == study.sha256:
            return lp
        raise StudyError(f"{lp} already locks a different version of this study. A registered study cannot be edited; "
                         "copy it to a new id and register that instead.")
    lock = {
        "study_id": study.id, "study_file": study_path.name, "study_sha256": study.sha256,
        "git_commit": _git_commit(study_path.parent),
        "locked_at": (now or datetime.now(timezone.utc)).isoformat(timespec="seconds"),
    }
    lp.write_text(json.dumps(lock, indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")
    return lp


def verify_lock(study_path: Path) -> tuple[Study, dict]:
    """Load the study and check it still matches its lock. Raises StudyError with the next step."""
    study = load_study(study_path)
    lp = lock_path(study_path)
    if not lp.exists():
        raise StudyError(f"{study_path.name} is not pre-registered. Run `python -m trovebench prereg {study_path}` "
                         "before running or reporting it.")
    lock = json.loads(lp.read_text(encoding="utf-8"))
    if lock.get("study_sha256") != study.sha256:
        raise StudyError(f"{study_path.name} has changed since it was locked on {lock.get('locked_at')}. "
                         "Restore the registered version, or register the changed design under a new study id.")
    return study, lock
