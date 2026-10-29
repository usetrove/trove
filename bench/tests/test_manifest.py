import pytest

from trovebench.manifest import ManifestError, dump_task, load_tasks, parse_task


def valid(**over):
    d = dict(
        id="demo-1", source="smoke", repo="https://example.com/r.git", base_commit="abc123",
        license="MIT", prompt="Fix the bug.", image="python:3.12-slim",
        success_cmd="pytest -q", timeout_s=600,
    )
    d.update(over)
    return d


def test_valid_minimal_task():
    t = parse_task(valid())
    assert t.id == "demo-1"
    assert t.setup == () and t.tags == ()


def test_lists_become_tuples_and_roundtrip():
    t = parse_task(valid(setup=["pip install -e ."], tags=["py"]))
    assert t.setup == ("pip install -e .",)
    assert parse_task(__import__("yaml").safe_load(dump_task(t))) == t


@pytest.mark.parametrize("field", ["id", "source", "repo", "base_commit", "license", "prompt", "image", "success_cmd", "timeout_s"])
def test_missing_required_field(field):
    d = valid()
    del d[field]
    with pytest.raises(ManifestError, match=field):
        parse_task(d, "t.yaml")


@pytest.mark.parametrize(
    "over, match",
    [
        ({"timeout_s": 0}, "timeout_s"),
        ({"timeout_s": True}, "timeout_s"),
        ({"timeout_s": "600"}, "timeout_s"),
        ({"id": "Bad Id"}, "id"),
        ({"source": "nope"}, "source"),
        ({"setup": "pip install"}, "setup"),
        ({"surprise": 1}, "unknown field"),
    ],
)
def test_rejects_bad_values(over, match):
    with pytest.raises(ManifestError, match=match):
        parse_task(valid(**over))


def test_non_mapping_rejected():
    with pytest.raises(ManifestError, match="mapping"):
        parse_task(["a"], "t.yaml")


def test_load_tasks_sorted_and_duplicates(tmp_path):
    import yaml

    (tmp_path / "b.yaml").write_text(yaml.safe_dump(valid(id="b")))
    (tmp_path / "a.yaml").write_text(yaml.safe_dump(valid(id="a")))
    assert [t.id for t in load_tasks(tmp_path)] == ["a", "b"]
    (tmp_path / "c.yaml").write_text(yaml.safe_dump(valid(id="a")))
    with pytest.raises(ManifestError, match="duplicate"):
        load_tasks(tmp_path)


def test_bad_yaml_message_names_file(tmp_path):
    p = tmp_path / "x.yaml"
    p.write_text("id: [unclosed")
    with pytest.raises(ManifestError, match="x.yaml"):
        load_tasks(tmp_path)
