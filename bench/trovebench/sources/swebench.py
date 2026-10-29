"""SWE-bench Verified adapter.

Data comes from the Hugging Face datasets-server rows API (verified 2026-10-29: returns repo, instance_id,
base_commit, patch, test_patch, problem_statement, FAIL_TO_PASS, PASS_TO_PASS, version, ...). The dataset
revision (the HF repo sha) is read before and after the download and must match, then recorded in each manifest.

Licensing: SWE-bench code is MIT, but the Verified dataset card declares no license and the tasks come from
upstream repos with their own licenses (bench/LICENSES.md). Manifests stay local; we publish IDs and aggregates only.

Environments: the official per-instance images were NOT verified against documentation, so we do not assume them.
Each task builds its environment from the repo at its base commit. Many older instances will not build that
way, which is why every generated task must pass `python -m trovebench validate` (gold patch makes the tests
pass, the unpatched repo fails them) before it is used. v1 covers the repos that use plain pytest; Django and
SymPy use their own test runners and are skipped.
"""

from __future__ import annotations

import random
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


def row_task(row: dict, revision: str, *, image: str) -> tuple[Task, str]:
    test_files = sorted({p for p, _ in split_diff(row["test_patch"])})
    task = Task(
        id=f"swebench-{row['instance_id'].lower()}",
        source="swebench", repo=f"https://github.com/{row['repo']}", base_commit=row["base_commit"],
        license="see-upstream", prompt=row["problem_statement"], image=image,
        setup=("apt-get update -qq && apt-get install -y -qq build-essential", "pip install -q -e . pytest"),
        grading_setup=(apply_patch_step(row["test_patch"]),),
        success_cmd="python -m pytest -q -p no:cacheprovider " + " ".join(test_files),
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
