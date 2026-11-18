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


class CcAgent:
    name = "claude_code"
    # Since v2.1.277 a resumed `claude -p` run reports the whole session's total_cost_usd and modelUsage, earlier
    # runs' spend included; summing results would double-count (code.claude.com/docs/en/headless and
    # /agent-sdk/cost-tracking, verified 2026-11-18). The bench pins a later version.
    resume_reports_session_total = True
    min_version_for_session_totals = "2.1.277"

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

    def command(self, *, model: str, max_budget_usd: float, prompt_path: str, effort: str = "", bare: bool = True,
                resume: str = "", persist: bool = False) -> str:
        # --bare skips hooks, installed plugins and CLAUDE.md (docs: code.claude.com/docs/en/headless, verified
        # 2026-11-06), so a condition that depends on Trove's hooks must run without it. The container is fresh,
        # holds no OAuth or keychain login, and gets only an API key, so dropping --bare adds no credential path.
        # --effort is documented in code.claude.com/docs/en/cli-reference (low|medium|high|xhigh|max).
        #
        # Multi-turn tasks (long sessions): `claude -p "<next>" --resume <session_id>` continues a print-mode session,
        # and --no-session-persistence would make it impossible to resume, so a session that will be continued is
        # persisted (code.claude.com/docs/en/headless "Continue conversations" and /cli-reference, verified 2026-11-18).
        # The container is thrown away after the run, so nothing outlives it.
        return (
            f'claude {"--bare " if bare else ""}-p "$(cat {shlex.quote(prompt_path)})" '
            + (f"--resume {shlex.quote(resume)} " if resume else "")
            + f"--model {shlex.quote(model)} --output-format json "
            + (f"--effort {shlex.quote(effort)} " if effort else "")
            + f"--max-budget-usd {max_budget_usd:g} --permission-mode acceptEdits "
            f"--allowedTools {shlex.quote(ALLOWED_TOOLS)}"
            + ("" if (persist or resume) else " --no-session-persistence")
        )

    def static_env(self) -> dict[str, str]:
        return {}

    def base_url_env(self, url: str) -> dict[str, str]:
        return {"ANTHROPIC_BASE_URL": url}

    def parse(self, stdout: str) -> AgentResult:
        return parse_claude_style_result(stdout)
