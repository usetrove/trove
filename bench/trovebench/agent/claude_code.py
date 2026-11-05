"""Claude Code, headless, API-key auth only.

Every flag below is from the official docs fetched 2026-10-29
(code.claude.com/docs/en/{headless,cli-reference,env-vars,setup}). Re-verify when you change
the pinned version: `claude --help` inside the image is the final word.
"""

from __future__ import annotations

import shlex

from .base import AgentResult, parse_claude_style_result

# Tools a bug-fixing run needs. --bare already limits Claude to Bash, file read and file edit.
ALLOWED_TOOLS = "Bash,Read,Edit,Write"


class ClaudeCode:
    name = "claude_code"

    def install_lines(self, version: str | None) -> list[str]:
        if not version:
            raise ValueError(
                "claude_version is not set. Pin the exact Claude Code version in bench/config.yaml "
                "(for example the output of `claude --version`), so results can be reproduced."
            )
        v = shlex.quote(version)
        return [
            f"RUN curl -fsSL https://claude.ai/install.sh | bash -s {v}",
            "ENV PATH=/root/.local/bin:$PATH",
            # The harness runs every command with `bash -lc`. On Debian a login shell resets PATH and drops
            # /root/.local/bin, so `claude` would be "command not found". /usr/local/bin is on that PATH.
            "RUN ln -sf /root/.local/bin/claude /usr/local/bin/claude && claude --version",
            # No auto-update, no telemetry or other nonessential traffic: one fixed version, quiet network.
            "ENV DISABLE_UPDATES=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
        ]

    def version_command(self) -> str:
        return "claude --version"

    def command(self, *, model: str, max_budget_usd: float, prompt_path: str) -> str:
        return (
            f'claude --bare -p "$(cat {shlex.quote(prompt_path)})" '
            f"--model {shlex.quote(model)} --output-format json "
            f"--max-budget-usd {max_budget_usd:g} --permission-mode acceptEdits "
            f"--allowedTools {shlex.quote(ALLOWED_TOOLS)} --no-session-persistence"
        )

    def static_env(self) -> dict[str, str]:
        return {}

    def base_url_env(self, url: str) -> dict[str, str]:
        return {"ANTHROPIC_BASE_URL": url}

    def parse(self, stdout: str) -> AgentResult:
        return parse_claude_style_result(stdout)
