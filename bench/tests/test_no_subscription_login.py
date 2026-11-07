"""The benchmark lab starts real agent sessions, so it must run only on the operator's own API key.

CLAUDE.md rules 3, 10 and 14: Trove never handles a subscription login and never pays for or intermediates
model usage. These tests pin that for the lab: Codex has no runnable adapter, a run refuses to start without an
API key, only that key is handed to a container (by name), and nothing from the operator's home directory
(~/.codex, ~/.claude, auth.json) is mounted or copied into one.
"""
from __future__ import annotations

import re
from pathlib import Path

import pytest

from trovebench import docker_env, docker_run
from trovebench.agent import get_agent

SRC = Path(docker_env.__file__).parent


def test_codex_cannot_be_started_and_says_why():
    with pytest.raises(NotImplementedError) as e:
        get_agent("codex")
    msg = str(e.value)
    assert "OPENAI_API_KEY" in msg and "ChatGPT" in msg and "auth.json" in msg


def test_only_the_anthropic_api_key_is_ever_handed_to_a_container():
    assert docker_run.KEY_ENV == "ANTHROPIC_API_KEY"
    text = (SRC / "docker_run.py").read_text(encoding="utf-8")
    for name in ("CODEX", "OPENAI", "CLAUDE_CODE_OAUTH", "ANTHROPIC_AUTH_TOKEN"):
        assert name not in text, f"docker_run.py mentions {name}; a new credential path needs a rule and a test"
    # The run refuses to start without the key.
    assert re.search(r'if not key:\s*raise RunEnvError', text) or "is not set" in text


def test_no_host_directory_is_mounted_and_only_lab_made_files_are_copied_in():
    for path in SRC.rglob("*.py"):
        text = path.read_text(encoding="utf-8")
        assert not re.search(r'"(-v|--volume|--mount)"', text), f"{path.name} mounts a host path"
        if path.parent.name != "agent" or path.name != "__init__.py":   # the Codex refusal message names it
            assert "auth.json" not in text, f"{path.name} mentions auth.json"
        assert "Path.home()" not in text and not re.search(r'expanduser\(', text), f"{path.name} reads the operator's home"
    # Everything copied INTO a container is a file the lab made: the prompt, the solution, its own binaries, the
    # generated Trove config and the capability snapshot. Never a path from the operator's agent folders.
    run = (SRC / "docker_run.py").read_text(encoding="utf-8")
    allowed = re.compile(r"^(td / \"[\w.]+\"|sol|self\.(trove|fake)_bin)$")
    sources = re.findall(r"c\.put\(([^,]+),", run)
    assert sources, "found no copies into a container; the check is broken"
    for src in sources:
        assert allowed.match(src.strip()), f"c.put copies {src!r} into a container; it must be a file the lab made"


def test_a_run_without_the_key_does_not_start(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    src = (SRC / "docker_run.py").read_text(encoding="utf-8")
    i = src.index("os.environ.get(KEY_ENV")
    window = src[i : i + 400]
    assert "RunEnvError" in window and "is not set" in window
