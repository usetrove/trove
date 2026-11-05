"""Turn one finished run's artifacts into a RunRecord.

Gateway conditions: cost and tokens come from the run's own Trove database
(measured from the provider's reported usage). Direct: recomputed from the agent's
reported per-model usage at list price (estimated; long-context rates are not
modeled because usage is aggregated).
"""

from __future__ import annotations

import json
import sqlite3
from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from pathlib import Path

from .agent.base import AgentResult
from .pricing import PricingTable, request_cost
from .records import OK, RunRecord


@dataclass(frozen=True)
class GatewayStats:
    n_requests: int
    n_failed: int                  # non-2xx responses
    n_unpriced: int                # requests whose cost the gateway could not compute
    cost_usd: float | None         # None if any request was unpriced
    input_uncached: int
    cache_write: int
    cache_read: int
    output: int
    reasoning: int
    tool_calls: int
    retries: int
    pinned_states: list[dict[str, str]]   # one dict per pinned session


def read_gateway_db(path: Path) -> GatewayStats:
    uri = f"file:{path.as_posix()}?mode=ro"
    con = sqlite3.connect(uri, uri=True)
    try:
        n, failed, unpriced, cost, a, b, c, d, e = con.execute(
            "SELECT COUNT(*), COALESCE(SUM(status_code < 200 OR status_code >= 300),0),"
            " COALESCE(SUM(cost_usd IS NULL),0), COALESCE(SUM(cost_usd),0),"
            " COALESCE(SUM(input_uncached_tokens),0), COALESCE(SUM(cache_write_tokens),0),"
            " COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(output_tokens),0),"
            " COALESCE(SUM(reasoning_tokens),0) FROM requests"
        ).fetchone()
        # `signals` is cumulative per request within a session, so the last request of each
        # session holds that session's totals. A run can touch several sessions (the agent's
        # helper calls derive their own session keys), so sum across sessions.
        tool_calls = retries = 0
        for (sig,) in con.execute(
            "SELECT signals FROM requests r WHERE signals IS NOT NULL AND id = "
            "(SELECT MAX(id) FROM requests WHERE session_key = r.session_key AND signals IS NOT NULL)"
        ):
            s = json.loads(sig)
            tool_calls += int(s.get("tool_calls", 0))
            retries += int(s.get("tool_calls", 0)) - int(s.get("distinct_calls", 0))
        pins = [json.loads(s) for (s,) in con.execute("SELECT states FROM session_pins")]
    finally:
        con.close()
    return GatewayStats(
        n_requests=n, n_failed=int(failed), n_unpriced=int(unpriced),
        cost_usd=float(cost) if unpriced == 0 else None,
        input_uncached=a, cache_write=b, cache_read=c, output=d, reasoning=e,
        tool_calls=tool_calls, retries=max(retries, 0), pinned_states=pins,
    )


def pins_match(pinned: list[dict[str, str]], expected: dict[str, str]) -> str | None:
    """None if every session ran under the condition's intended state; else a plain-English reason.

    This catches the silent case where Trove downgrades "on" to "shadow" (baseline gate,
    guardrail rollback, DB error), which would make an optimizer condition a no-op.
    """
    if not pinned:
        # With every optimizer off Trove skips the engine and pins nothing; that is consistent.
        if all(v == "off" for v in expected.values()):
            return None
        return "the gateway recorded no pinned sessions, so the optimizer states could not be confirmed"
    for states in pinned:
        for name, want in expected.items():
            got = states.get(name, "off")
            if got != want:
                return f"optimizer {name} ran as '{got}' but the condition needs '{want}' (Trove downgraded it?)"
    return None


def direct_cost(usage: AgentResult, table: PricingTable, at: date) -> tuple[float | None, dict[str, int]]:
    """List-price cost from the agent's per-model usage. None if any model has no price."""
    total = Decimal(0)
    tok = {"input_uncached": 0, "cache_write": 0, "cache_read": 0, "output": 0}
    for model, u in usage.usage_by_model.items():
        try:
            rates = table.rates(model, at)
        except Exception:
            return None, tok
        total += request_cost(rates, input_uncached=u.input_tokens, cache_write=u.cache_write_tokens,
                              cache_read=u.cache_read_tokens, output=u.output_tokens)
        tok["input_uncached"] += u.input_tokens
        tok["cache_write"] += u.cache_write_tokens
        tok["cache_read"] += u.cache_read_tokens
        tok["output"] += u.output_tokens
    return float(total), tok


def build_record(
    *, run_id: str, task_id: str, condition: str, seed: int, passed: bool, timed_out: bool, wall_s: float,
    agent: AgentResult, gateway: GatewayStats | None, expected_states: dict[str, str] | None,
    table: PricingTable, today: date, agent_exit_ok: bool,
) -> RunRecord:
    notes: list[str] = []
    status = OK
    if not agent.parsed and not timed_out:
        # The agent never produced a result (not installed, crashed, bad auth). That is an infrastructure
        # failure, not a failed attempt, so it must not count as "ran and failed" with a cost of zero.
        status = "error"
        notes.append(agent.detail or "agent printed no result")
    if agent.parsed and agent.api_error_status is not None:
        status = "error"
        notes.append(f"the API call failed with HTTP {agent.api_error_status}; this run says nothing about the agent "
                     "(for 401, check ANTHROPIC_API_KEY is a pay-per-use API key)")
    if gateway is not None:
        why = pins_match(gateway.pinned_states, expected_states or {})
        if why:
            status = "error"
            notes.append(why)
        if gateway.n_requests == 0 and not timed_out:
            status = "error"
            notes.append("no request reached the gateway; the agent may not be using the custom endpoint")
        cost, source = gateway.cost_usd, "gateway"
        if cost is None:
            notes.append(f"{gateway.n_unpriced} request(s) had no price, so cost is unknown")
        turns = agent.turns if agent.turns is not None else gateway.n_requests - gateway.n_failed
        if cost is not None and agent.cost_usd_estimate and cost > 0:
            drift = abs(agent.cost_usd_estimate - cost) / cost
            if drift > 0.10:
                notes.append(f"agent's own cost estimate differs from the gateway's by {drift:.0%}")
        tokens = dict(input_uncached=gateway.input_uncached, cache_write=gateway.cache_write,
                      cache_read=gateway.cache_read, output=gateway.output)
        reasoning, tool_calls, retries = gateway.reasoning, gateway.tool_calls, gateway.retries
    else:
        cost, tokens = direct_cost(agent, table, today)
        source = "agent-usage"
        if cost is None:
            notes.append("agent reported no usable per-model usage, so cost is unknown")
        turns = agent.turns if agent.turns is not None else 0
        reasoning = tool_calls = retries = 0   # not observable without a gateway
    return RunRecord(
        run_id=run_id, task_id=task_id, condition=condition, seed=seed, status=status, passed=passed,
        timed_out=timed_out, wall_s=round(wall_s, 1), turns=turns, tool_calls=tool_calls, retries=retries,
        cost_usd=cost, cost_source=source,
        input_uncached_tokens=tokens["input_uncached"], cache_write_tokens=tokens["cache_write"],
        cache_read_tokens=tokens["cache_read"], output_tokens=tokens["output"], reasoning_tokens=reasoning,
        note="; ".join(notes),
    )
