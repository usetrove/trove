import base64
import json
import subprocess

import pytest

from trovebench.manifest import load_tasks
from trovebench.sources import mine, polyglot, swebench
from trovebench.sources.common import apply_patch_step, is_test_path, split_diff

DIFF = (
    "diff --git a/src/app.py b/src/app.py\n--- a/src/app.py\n+++ b/src/app.py\n@@ -1 +1 @@\n-x = 1\n+x = 2\n"
    "diff --git a/tests/test_app.py b/tests/test_app.py\n--- a/tests/test_app.py\n+++ b/tests/test_app.py\n@@ -1 +1 @@\n-a\n+b\n"
)


def test_split_diff_and_test_paths():
    parts = split_diff(DIFF)
    assert [p for p, _ in parts] == ["src/app.py", "tests/test_app.py"]
    assert "".join(c for _, c in parts) == DIFF
    for p in ("tests/a.py", "pkg/test_x.py", "x_test.go", "web/a.test.ts", "src/__tests__/a.js", "spec/a.rb"):
        assert is_test_path(p), p
    for p in ("src/app.py", "latest/readme.md", "contest.py", "src/attestation.py"):
        assert not is_test_path(p), p


def test_apply_patch_step_roundtrips_and_limits():
    step = apply_patch_step("it's a 'quoted' EOF\n")
    b64 = step.split()[1]
    assert base64.b64decode(b64).decode() == "it's a 'quoted' EOF\n"
    with pytest.raises(ValueError, match="limit"):
        apply_patch_step("x" * 200_000)


# --- polyglot -------------------------------------------------------------------

def fake_checkout(root):
    for name in ("alpha", "beta", "gamma"):
        ex = root / "python" / "exercises" / "practice" / name
        (ex / ".docs").mkdir(parents=True)
        (ex / ".meta").mkdir()
        (ex / ".docs" / "instructions.md").write_text(f"Do the {name} thing.\n")
        (ex / ".meta" / "config.json").write_text(json.dumps({"files": {"solution": [f"{name}.py"], "test": [f"{name}_test.py"]}}))
    (root / "python" / "exercises" / "practice" / "alpha" / ".docs" / "instructions.append.md").write_text("Extra note.\n")


def test_polyglot_generate(tmp_path):
    co, out = tmp_path / "co", tmp_path / "out"
    fake_checkout(co)
    tasks = polyglot.generate(co, "a" * 40, out, n=2, seed=1)
    assert len(tasks) == 2
    loaded = load_tasks(out)
    assert [t.id for t in loaded] == sorted(t.id for t in tasks)
    t = next(t for t in loaded if t.id.endswith("alpha")) if any(t.id.endswith("alpha") for t in loaded) else loaded[0]
    assert "Do not modify the tests" in t.prompt
    assert any(".meta" in s and "rm -rf" in s for s in t.setup)           # reference solutions are removed
    assert t.grading_setup[0].startswith("git checkout " + "a" * 40)       # tests restored before grading
    assert t.dataset_commit == "a" * 40 and "Exercism" in t.attribution
    # deterministic for a seed
    again = polyglot.generate(co, "a" * 40, tmp_path / "out2", n=2, seed=1)
    assert [x.id for x in again] == [x.id for x in tasks]


def test_polyglot_empty_checkout(tmp_path):
    (tmp_path / "python" / "exercises" / "practice").mkdir(parents=True)
    with pytest.raises(ValueError, match="No Python exercises"):
        polyglot.generate(tmp_path, "c", tmp_path / "o", n=1, seed=1)


# --- swebench -------------------------------------------------------------------

def swe_row(i, repo="psf/requests"):
    return {"repo": repo, "instance_id": f"{repo.replace('/', '__')}-{i}", "base_commit": f"{i:040d}", "patch": "gold\n",
            "test_patch": DIFF, "problem_statement": f"problem {i}", "difficulty": "<15 min fix"}


def fake_get(rows, shas):
    calls = {"sha": 0}

    def get(url):
        if url == swebench.HF_API:
            s = shas[min(calls["sha"], len(shas) - 1)]
            calls["sha"] += 1
            return {"sha": s}
        off = int(url.split("offset=")[1].split("&")[0])
        return {"rows": [{"row": r} for r in rows[off:off + 100]], "num_rows_total": len(rows)}

    return get


def test_swebench_fetch_pages_and_revision_check():
    rows = [swe_row(i) for i in range(230)]
    got, rev = swebench.fetch_rows(fake_get(rows, ["abc", "abc"]))
    assert len(got) == 230 and rev == "abc"
    with pytest.raises(RuntimeError, match="changed while downloading"):
        swebench.fetch_rows(fake_get(rows, ["abc", "def"]))


def test_swebench_generate_filters_runners(tmp_path):
    rows = [swe_row(i) for i in range(5)] + [swe_row(9, "django/django"), swe_row(8, "sympy/sympy")]
    tasks = swebench.generate(rows, "rev123", tmp_path, n=50, seed=1)
    assert len(tasks) == 5 and all("django" not in t.id and "sympy" not in t.id for t in tasks)
    t = load_tasks(tmp_path)[0]
    assert t.dataset_commit == "rev123" and t.success_cmd.endswith("tests/test_app.py")
    assert (tmp_path / t.id / "gold.patch").read_text() == "gold\n"
    assert t.id == t.id.lower() and t.source == "swebench"


# --- miner ----------------------------------------------------------------------

REPO = mine.RepoSpec("o/r", "python:3.12-slim", ("pip install -e .",), "python -m pytest -q {test_files}")


def mining_get(license="MIT", body="Fixes #7", diff=DIFF):
    pr = {"number": 12, "merged_at": "2026-10-20T10:00:00Z", "body": body, "title": "fix it", "merge_commit_sha": "m" * 40}
    old = {"number": 3, "merged_at": "2026-01-01T00:00:00Z", "body": "Fixes #1", "title": "old", "merge_commit_sha": "o" * 40}
    unmerged = {"number": 4, "merged_at": None, "body": "Fixes #2", "title": "x", "merge_commit_sha": None}

    def get(url, accept):
        if url.endswith("/repos/o/r"):
            return {"license": {"spdx_id": license}}
        if "/pulls?" in url:
            return [pr, old, unmerged] if "page=1" in url else []
        if url.endswith("/pulls/12"):
            return diff
        if "/commits/" in url:
            return {"parents": [{"sha": "b" * 40}]}
        if url.endswith("/issues/7"):
            return {"title": "Crash on start", "body": "Steps...", "html_url": "https://github.com/o/r/issues/7"}
        raise AssertionError(url)

    return get


def test_mine_accepts_recent_pr_with_tests(tmp_path):
    tasks, skips = mine.mine_repo(REPO, mining_get(), "2026-10-01", tmp_path, limit=5)
    assert len(tasks) == 1 and skips == []
    t = tasks[0]
    assert t.base_commit == "b" * 40 and t.license == "MIT" and "Crash on start" in t.prompt
    assert t.success_cmd == "python -m pytest -q tests/test_app.py"
    assert t.issue_url.endswith("/issues/7") and "merged:2026-10-20" in t.tags
    gold = (tmp_path / t.id / "gold.patch").read_text()
    assert "src/app.py" in gold and "tests/test_app.py" not in gold            # fix and tests are kept apart


def test_mine_rejects_bad_candidates(tmp_path):
    t, s = mine.mine_repo(REPO, mining_get(license="GPL-3.0"), "2026-10-01", tmp_path, limit=5)
    assert t == [] and "license GPL-3.0" in s[0]
    t, s = mine.mine_repo(REPO, mining_get(body="no reference"), "2026-10-01", tmp_path, limit=5)
    assert t == [] and "no 'fixes #N'" in s[0]
    only_src = DIFF.split("diff --git a/tests")[0]
    t, s = mine.mine_repo(REPO, mining_get(diff=only_src), "2026-10-01", tmp_path, limit=5)
    assert t == [] and "source and test" in s[0]


def test_load_repos_validation(tmp_path):
    p = tmp_path / "r.yaml"
    p.write_text("repos:\n  - repo: a/b\n    image: i\n    test_cmd: pytest\n")
    with pytest.raises(ValueError, match="test_files"):
        mine.load_repos(p)
    p.write_text("repos: []\n")
    with pytest.raises(ValueError, match="no repos"):
        mine.load_repos(p)
    p.write_text("repos:\n  - repo: a/b\n    image: i\n    test_cmd: 'pytest {test_files}'\n")
    assert mine.load_repos(p)[0].repo == "a/b"


def test_gold_and_test_patches_apply_to_a_real_repo(tmp_path):
    """The split diff must really apply with git, not just look right."""
    r = tmp_path / "repo"
    (r / "src").mkdir(parents=True)
    (r / "tests").mkdir()
    (r / "src" / "app.py").write_text("x = 1\n")
    (r / "tests" / "test_app.py").write_text("a\n")
    run = lambda *a, **k: subprocess.run(a, cwd=r, check=True, capture_output=True, text=True, **k)
    run("git", "init", "-q")
    run("git", "add", "-A")
    run("git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base")
    parts = dict(split_diff(DIFF))
    for chunk in parts.values():
        run("git", "apply", "--whitespace=nowarn", "-", input=chunk)
    assert (r / "src" / "app.py").read_text() == "x = 2\n" and (r / "tests" / "test_app.py").read_text() == "b\n"
