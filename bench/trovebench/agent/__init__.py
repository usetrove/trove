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
        # Fail closed. When this adapter is built it must (a) refuse to start unless OPENAI_API_KEY is set,
        # passed into the container by name, (b) never mount or copy ~/.codex or auth.json, and (c) never use
        # a Sign in with ChatGPT login or a Codex access token. bench/tests/test_no_subscription_login.py
        # pins these rules; update it together with the adapter.
        raise NotImplementedError(
            "The Codex adapter is not built yet, so no Codex run is started. When it exists it will run only "
            "with your own OpenAI API key (OPENAI_API_KEY) and never with a ChatGPT login or ~/.codex/auth.json. "
            "Use claude_code."
        )
    raise ValueError(f"unknown agent '{name}'. Use claude_code.")
