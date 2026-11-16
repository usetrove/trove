from dataclasses import replace

from trovebench import docker_env as de
from trovebench import validate
from trovebench.manifest import parse_task


def make_task(tmp_path):
    (tmp_path / "gold.patch").write_text("x", encoding="utf-8")
    t = parse_task(dict(id="t1", source="swebench", repo="r", base_commit="c", license="MIT",
                        prompt="p", image="i", success_cmd="true", timeout_s=60))
    return replace(t, base_dir=str(tmp_path))


def test_build_failure_is_a_rejection_not_an_abort(tmp_path, monkeypatch):
    def boom(*a, **k):
        raise de.DockerError("docker build failed (exit 1): pip install\nModuleNotFoundError: setuptools.dep_util")

    monkeypatch.setattr(de, "build_task_image", boom)
    v = validate.validate_task(make_task(tmp_path), agent=None, agent_version=None)
    assert v.status == "rejected"
    assert v.reason.startswith("environment build failed:")
    assert "dep_util" in v.reason
    assert "\n" not in v.reason
