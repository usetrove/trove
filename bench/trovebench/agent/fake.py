"""Test double: talks to a mock upstream, never to a real API. See bench/tools/benchfake."""

from __future__ import annotations

import shlex

from .base import AgentResult, parse_claude_style_result

FAKE_BIN = "/usr/local/bin/benchfake"


class FakeAgent:
    name = "fake"

    def install_lines(self, version: str | None) -> list[str]:
        return []

    def version_command(self) -> str:
        return "echo benchfake"

    def command(self, *, model: str, max_budget_usd: float, prompt_path: str) -> str:
        # /bench/solution.sh exists only for tasks that ship one; it stands in for "the agent fixed it".
        sol = '$([ -f /bench/solution.sh ] && echo "-solution /bench/solution.sh")'
        return f"{FAKE_BIN} agent -turns 3 -model {shlex.quote(model)} {sol}"

    def static_env(self) -> dict[str, str]:
        return {}

    def base_url_env(self, url: str) -> dict[str, str]:
        return {"ANTHROPIC_BASE_URL": url}

    def parse(self, stdout: str) -> AgentResult:
        return parse_claude_style_result(stdout)
