"""What the runner needs from a coding agent, and nothing more."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from typing import Protocol


@dataclass(frozen=True)
class ModelUsage:
    input_tokens: int = 0
    output_tokens: int = 0
    cache_read_tokens: int = 0
    cache_write_tokens: int = 0


@dataclass(frozen=True)
class AgentResult:
    parsed: bool                       # False when stdout held no usable result JSON
    is_error: bool = False
    turns: int | None = None
    cost_usd_estimate: float | None = None   # the agent's own client-side estimate, not authoritative
    usage_by_model: dict[str, ModelUsage] = field(default_factory=dict)
    session_id: str | None = None
    detail: str = ""
    # HTTP status when the API call itself failed (401 bad key, 429, 5xx). Seen in a real Claude Code result
    # as `api_error_status`. That is an infrastructure failure, not a verdict on the agent.
    api_error_status: int | None = None


class Agent(Protocol):
    name: str

    def install_lines(self, version: str | None) -> list[str]:
        """Dockerfile instructions that install the agent at an exact version."""

    def version_command(self) -> str: ...

    def command(self, *, model: str, max_budget_usd: float, prompt_path: str, effort: str = "", bare: bool = True,
                resume: str = "", persist: bool = False) -> str:
        """Shell command (run in the repo root inside the container) that runs the agent headless.
        effort is a reasoning-effort level ("" = the model default); bare=False lets hooks and plugins load.
        resume continues the session with that id (a follow-up prompt); persist keeps a new session resumable."""

    def static_env(self) -> dict[str, str]:
        """Non-secret environment for the container (the API key is passed separately)."""

    def base_url_env(self, url: str) -> dict[str, str]:
        """Environment that points the agent at a custom endpoint URL."""

    def parse(self, stdout: str) -> AgentResult: ...


def combine_results(results: list[AgentResult], *, session_totals: bool) -> AgentResult:
    """One result for a session that took several calls (a prompt and its follow-ups).

    session_totals=True: each resumed call already reports the whole session's spend (Claude Code since v2.1.277),
    so the latest parsed result is the session total and summing would double-count. Otherwise each call reports
    only its own usage and the calls are added up. A call that printed no result adds nothing; an error in any
    call marks the session as errored.
    """
    if not results:
        return AgentResult(parsed=False, detail="the agent was not run")
    parsed = [r for r in results if r.parsed]
    if not parsed:
        return results[-1]
    is_error = any(r.is_error for r in results) or not results[-1].parsed
    api_status = next((r.api_error_status for r in reversed(results) if r.api_error_status is not None), None)
    detail = "; ".join(r.detail for r in results if r.detail and r.detail != "success") or parsed[-1].detail
    if session_totals:
        last = parsed[-1]
        return AgentResult(parsed=True, is_error=is_error, turns=last.turns, cost_usd_estimate=last.cost_usd_estimate,
                           usage_by_model=dict(last.usage_by_model), session_id=last.session_id, detail=detail,
                           api_error_status=api_status)
    usage: dict[str, ModelUsage] = {}
    for r in parsed:
        for m, u in r.usage_by_model.items():
            p = usage.get(m, ModelUsage())
            usage[m] = ModelUsage(p.input_tokens + u.input_tokens, p.output_tokens + u.output_tokens,
                                  p.cache_read_tokens + u.cache_read_tokens, p.cache_write_tokens + u.cache_write_tokens)
    costs = [r.cost_usd_estimate for r in parsed]
    turns = [r.turns for r in parsed]
    return AgentResult(parsed=True, is_error=is_error,
                       turns=sum(t for t in turns if t is not None) if any(t is not None for t in turns) else None,
                       cost_usd_estimate=sum(c for c in costs if c is not None) if any(c is not None for c in costs) else None,
                       usage_by_model=usage, session_id=parsed[-1].session_id, detail=detail, api_error_status=api_status)


def last_json_object(text: str) -> dict | None:
    """The agent prints one JSON result; tolerate banner/log lines around it."""
    for line in reversed([ln for ln in text.splitlines() if ln.strip()]):
        line = line.strip()
        if line.startswith("{") and line.endswith("}"):
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(obj, dict):
                return obj
    try:
        obj = json.loads(text)
        return obj if isinstance(obj, dict) else None
    except json.JSONDecodeError:
        return None


def parse_claude_style_result(text: str) -> AgentResult:
    """Parse Claude Code's `--output-format json` result.

    Verified fields (code.claude.com/docs/en/agent-sdk/cost-tracking, 2026-10-29): total_cost_usd,
    usage, modelUsage{model: inputTokens, outputTokens, cacheReadInputTokens, cacheCreationInputTokens,
    costUSD}, subtype, session_id. num_turns and is_error are read if present but not required.
    """
    obj = last_json_object(text)
    if obj is None:
        return AgentResult(parsed=False, detail="no JSON result found in agent output")
    mu = {}
    for model, u in (obj.get("modelUsage") or {}).items():
        mu[model] = ModelUsage(
            int(u.get("inputTokens", 0)), int(u.get("outputTokens", 0)),
            int(u.get("cacheReadInputTokens", 0)), int(u.get("cacheCreationInputTokens", 0)),
        )
    cost = obj.get("total_cost_usd")
    subtype = str(obj.get("subtype", ""))
    return AgentResult(
        parsed=True,
        is_error=bool(obj.get("is_error", False)) or (subtype not in ("", "success")),
        turns=int(obj["num_turns"]) if isinstance(obj.get("num_turns"), int) else None,
        cost_usd_estimate=float(cost) if isinstance(cost, (int, float)) else None,
        usage_by_model=mu,
        session_id=obj.get("session_id") if isinstance(obj.get("session_id"), str) else None,
        detail=subtype,
        api_error_status=obj["api_error_status"] if isinstance(obj.get("api_error_status"), int) else None,
    )
