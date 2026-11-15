"""SWE-bench Verified adapter.

Data comes from the Hugging Face datasets-server rows API (verified 2026-10-29: returns repo, instance_id,
base_commit, patch, test_patch, problem_statement, FAIL_TO_PASS, PASS_TO_PASS, version, ...). The dataset
revision (the HF repo sha) is read before and after the download and must match, then recorded in each manifest.

Licensing: SWE-bench code is MIT, but the Verified dataset card declares no license and the tasks come from
upstream repos with their own licenses (bench/LICENSES.md). Manifests stay local; we publish IDs and aggregates only.

Environments: the official per-instance images were NOT verified against documentation, so we do not assume them.
Each task builds its environment from the repo at its base commit, installing dependencies as they existed on
the pull request's `created_at` date (`uv pip install --exclude-newer`; created_at and uv's option were read from
the dataset card and uv docs on 2026-11-14, and the cutoff was seen to reach build dependencies in a manual
test). Without that cutoff current setuptools, numpy and pyparsing break these old repos. Grading runs the
FAIL_TO_PASS tests. Some older instances will still not build, which is why every generated task must pass
`python -m trovebench validate` (gold patch makes the tests pass, the unpatched repo fails them) before it is used. v1 covers the repos that use plain pytest; Django and
SymPy use their own test runners and are skipped.
"""

from __future__ import annotations

import json
import random
import re
import shlex
from pathlib import Path
from typing import Callable
from urllib.parse import quote

from ..manifest import Task
from .common import apply_patch_step, split_diff, write_task

DATASET = "princeton-nlp/SWE-bench_Verified"
HF_API = f"https://huggingface.co/api/datasets/{DATASET}"
ROWS_API = "https://datasets-server.huggingface.co/rows"
PYTEST_REPOS = {
    "astropy/astropy", "matplotlib/matplotlib", "scikit-learn/scikit-learn", "sphinx-doc/sphinx",
    "pytest-dev/pytest", "pylint-dev/pylint", "pydata/xarray", "psf/requests", "mwaskom/seaborn",
}
PAGE = 100   # the rows API returns at most 100 rows per request

GetJson = Callable[[str], dict]


def fetch_rows(get_json: GetJson) -> tuple[list[dict], str]:
    """All rows plus the dataset revision. Fails if the revision moved during the download."""
    rev = get_json(HF_API)["sha"]
    rows: list[dict] = []
    offset = 0
    while True:
        url = f"{ROWS_API}?dataset={quote(DATASET, safe='')}&config=default&split=test&offset={offset}&length={PAGE}"
        data = get_json(url)
        batch = [r["row"] for r in data["rows"]]
        rows += batch
        offset += PAGE
        if not batch or offset >= data["num_rows_total"]:
            break
    after = get_json(HF_API)["sha"]
    if after != rev:
        raise RuntimeError(f"The dataset changed while downloading (revision {rev[:8]} -> {after[:8]}). Run the command again.")
    return rows, rev


PYTEST = "python -m pytest -q -p no:cacheprovider "
MAX_TEST_ARGS = 60_000   # characters of test ids on one command line; above this fall back to whole files
LOCALE_STEP = ("sed -i 's/^# *en_US.UTF-8/en_US.UTF-8/' /etc/locale.gen && locale-gen && "
               "printf 'export LANG=en_US.UTF-8\\nexport LC_ALL=en_US.UTF-8\\n' > /etc/profile.d/locale.sh")


# Editable installs (`-e .`) need a setuptools new enough for PEP 660. Pre-2022 cutoffs picked a setuptools without
# `build_editable` and failed (seen 2026-11-14, matplotlib-20826); 2023-01-15 and 2023-07-15 cutoffs got past that
# step (manual tests). Old tasks therefore get their build tools, and as a fallback all their dependencies, from
# no earlier than this floor.
SETUPTOOLS_FLOOR = "2023-01-15"
# Old C code that newer compilers reject as errors (implicit declarations and the like) is downgraded to warnings.
CFLAGS = "-Wno-error=implicit-function-declaration -Wno-error=incompatible-pointer-types -Wno-error=int-conversion"


def snapshot_date(row: dict) -> str:
    """The pull request's creation date (YYYY-MM-DD). Dependencies are installed as they existed that day."""
    m = re.match(r"\d{4}-\d{2}-\d{2}", str(row.get("created_at") or ""))
    if not m:
        raise ValueError(f"{row.get('instance_id')}: no usable created_at date, so its environment cannot be dated")
    return m.group(0)


def install_step(date: str) -> str:
    """Install the project with dependencies as of `date`. First try keeping setuptools at the floor while everything
    else keeps the task's date (the per-package option is not confirmed in uv's docs, so it may be refused or may
    not reach build dependencies). If that fails, retry with the whole cutoff moved up to the floor."""
    floor = max(date, SETUPTOOLS_FLOOR)
    base = f"CFLAGS='{CFLAGS}' uv pip install --system"
    precise = f"{base} --exclude-newer {date} --exclude-newer-package setuptools={floor} -e . pytest"
    return f"{precise} || {base} --exclude-newer {floor} -e . pytest"


def fail_to_pass(row: dict) -> list[str]:
    raw = row.get("FAIL_TO_PASS")
    try:
        ids = json.loads(raw) if isinstance(raw, str) else list(raw or [])
    except (ValueError, TypeError):
        return []
    return [i for i in ids if isinstance(i, str) and i]


def success_cmd(row: dict, test_files: list[str]) -> str:
    """Grade the tests SWE-bench says the fix resolves (FAIL_TO_PASS). Unrelated tests in the same file can fail
    for reasons of platform or library drift, so running whole files would reject good tasks. PASS_TO_PASS is not
    checked, so this does not detect regressions elsewhere. Falls back to whole files if the list is missing."""
    ids = fail_to_pass(row)
    args = " ".join(shlex.quote(i) for i in ids)
    if not ids or len(args) > MAX_TEST_ARGS:
        return PYTEST + " ".join(test_files)
    return PYTEST + args


def row_task(row: dict, revision: str, *, image: str) -> tuple[Task, str]:
    test_files = sorted({p for p, _ in split_diff(row["test_patch"])})
    date = snapshot_date(row)
    task = Task(
        id=f"swebench-{row['instance_id'].lower()}",
        source="swebench", repo=f"https://github.com/{row['repo']}", base_commit=row["base_commit"],
        license="see-upstream", prompt=row["problem_statement"], image=image,
        setup=("apt-get update -qq && apt-get install -y -qq build-essential locales && " + LOCALE_STEP,
               "pip install -q uv",
               install_step(date)),
        grading_setup=(apply_patch_step(row["test_patch"]),),
        success_cmd=success_cmd(row, test_files),
        timeout_s=1800, dataset_commit=revision,
        tags=("swebench", "verified", row.get("difficulty") or "unknown"),
        issue_url=f"https://github.com/{row['repo']}",
        attribution=f"SWE-bench Verified instance {row['instance_id']} (MIT code; task text from {row['repo']}).",
    )
    return task, row["patch"]


def generate(rows: list[dict], revision: str, out_dir: Path, *, n: int, seed: int, image: str = "python:3.9-slim") -> list[Task]:
    eligible = sorted((r for r in rows if r["repo"] in PYTEST_REPOS), key=lambda r: r["instance_id"])
    if not eligible:
        raise ValueError("No SWE-bench rows use a pytest-based repo; the dataset layout may have changed.")
    chosen = random.Random(seed).sample(eligible, min(n, len(eligible)))
    tasks = []
    for row in sorted(chosen, key=lambda r: r["instance_id"]):
        try:
            task, gold = row_task(row, revision, image=image)
        except ValueError:
            continue   # test patch too large to pass as one argument
        write_task(out_dir, task, gold)
        tasks.append(task)
    return tasks
