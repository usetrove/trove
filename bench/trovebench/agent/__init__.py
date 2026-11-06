from __future__ import annotations

from .base import Agent
from .cc import CcAgent
from .fake import FakeAgent


def get_agent(name: str) -> Agent:
    if name == "claude_code":
        return CcAgent()
    if name == "fake":
        return FakeAgent()
    if name == "codex":
        raise NotImplementedError(
            "The Codex adapter is not built yet. It needs `codex exec --json` plus "
            "openai_base_url in ~/.codex/config.toml (see bench/README.md). Use claude_code."
        )
    raise ValueError(f"unknown agent '{name}'. Use claude_code.")
