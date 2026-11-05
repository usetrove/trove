import json
import sqlite3
from datetime import date
from pathlib import Path

import pytest
from helpers import paired_runs, rec

from trovebench.agent import get_agent
from trovebench.agent.base import AgentResult, ModelUsage, parse_claude_style_result
from trovebench.agent.claude_code import ClaudeCode
from trovebench.collect import build_record, direct_cost, pins_match, read_gateway_db
from trovebench.config import ConfigError, load_config
from trovebench.conditions import parse_conditions
from trovebench.docker_env import render_dockerfile
from trovebench.manifest import parse_task
from trovebench.pricing import load
from trovebench.report import write_all
from trovebench.stats import ShipRule

TABLE = load()
TODAY = date(2026, 10, 29)


def make_db(path: Path, rows, pins):
    con = sqlite3.connect(path)
    con.execute("""CREATE TABLE requests (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER, session_key TEXT,
        status_code INTEGER, input_uncached_tokens INTEGER, cache_write_tokens INTEGER, cache_read_tokens INTEGER,
        output_tokens INTEGER, reasoning_tokens INTEGER, cost_usd REAL, signals TEXT)""")
    con.execute("CREATE TABLE session_pins (session_key TEXT PRIMARY KEY, states TEXT)")
    for r in rows:
        con.execute("INSERT INTO requests (ts, session_key, status_code, input_uncached_tokens, cache_write_tokens,"
                    " cache_read_tokens, output_tokens, reasoning_tokens, cost_usd, signals) VALUES (?,?,?,?,?,?,?,?,?,?)", r)
    for i, p in enumerate(pins):
        con.execute("INSERT INTO session_pins VALUES (?, ?)", (f"s{i}", json.dumps(p)))
    con.commit()
    con.close()


def sig(tc, dc):
    return json.dumps({"messages": 5, "tool_calls": tc, "distinct_calls": dc})


def test_read_gateway_db_sums_and_session_signals(tmp_path):
    p = tmp_path / "trove.db"
    make_db(p, [
        (1, "a", 200, 100, 10, 50, 20, 0, 0.01, sig(1, 1)),
        (2, "a", 200, 100, 10, 50, 20, 0, 0.01, sig(3, 2)),     # session a ends with 3 calls, 2 distinct
        (3, "b", 200, 10, 0, 0, 5, 0, 0.002, sig(2, 2)),        # session b: 2 calls, 0 retries
    ], [{"noise_normalizer": "on"}])
    s = read_gateway_db(p)
    assert s.n_requests == 3 and s.cost_usd == pytest.approx(0.022)
    assert (s.input_uncached, s.cache_write, s.cache_read, s.output) == (210, 20, 100, 45)
    assert s.tool_calls == 5 and s.retries == 1


def test_unpriced_request_makes_cost_unknown(tmp_path):
    p = tmp_path / "trove.db"
    make_db(p, [(1, "a", 200, 1, 0, 0, 1, 0, None, None)], [])
    s = read_gateway_db(p)
    assert s.cost_usd is None and s.n_unpriced == 1


@pytest.mark.parametrize(
    "pinned, expected, bad",
    [
        ([], {"a": "off"}, False),                         # engine skipped when everything is off
        ([], {"a": "on"}, True),
        ([{"a": "on"}], {"a": "on"}, False),
        ([{"a": "shadow"}], {"a": "on"}, True),            # silently downgraded by the baseline gate
        ([{"a": "on"}, {"a": "shadow"}], {"a": "on"}, True),
        ([{}], {"a": "off"}, False),
    ],
)
def test_pins_match(pinned, expected, bad):
    assert (pins_match(pinned, expected) is not None) == bad


def test_build_record_marks_downgrade_as_error(tmp_path):
    p = tmp_path / "trove.db"
    make_db(p, [(1, "a", 200, 10, 0, 0, 5, 0, 0.01, sig(1, 1))], [{"noise_normalizer": "shadow"}])
    r = build_record(run_id="r", task_id="t", condition="c", seed=0, passed=True, timed_out=False, wall_s=3.0,
                     agent=AgentResult(parsed=True, turns=4), gateway=read_gateway_db(p),
                     expected_states={"noise_normalizer": "on"}, table=TABLE, today=TODAY, agent_exit_ok=True)
    assert r.status == "error" and "downgraded" in r.note


def test_build_record_ok_and_drift_note(tmp_path):
    p = tmp_path / "trove.db"
    make_db(p, [(1, "a", 200, 10, 0, 0, 5, 0, 0.10, sig(2, 1))], [])
    r = build_record(run_id="r", task_id="t", condition="passthrough", seed=0, passed=True, timed_out=False, wall_s=3.0,
                     agent=AgentResult(parsed=True, turns=4, cost_usd_estimate=0.5), gateway=read_gateway_db(p),
                     expected_states={"noise_normalizer": "off"}, table=TABLE, today=TODAY, agent_exit_ok=True)
    assert r.status == "ok" and r.cost_usd == pytest.approx(0.10) and r.cost_source == "gateway"
    assert r.turns == 4 and r.retries == 1 and "differs" in r.note


def test_no_requests_reaching_gateway_is_error(tmp_path):
    p = tmp_path / "trove.db"
    make_db(p, [], [])
    r = build_record(run_id="r", task_id="t", condition="passthrough", seed=0, passed=False, timed_out=False, wall_s=1,
                     agent=AgentResult(parsed=False), gateway=read_gateway_db(p), expected_states={"x": "off"},
                     table=TABLE, today=TODAY, agent_exit_ok=False)
    assert r.status == "error" and "custom endpoint" in r.note


def test_direct_run_with_no_agent_result_is_error():
    # Regression: a run where the agent never started ("claude: command not found") used to be recorded as
    # status=ok, passed=False, cost=0, which would count as a free failed attempt in the analysis.
    r = build_record(run_id="r", task_id="t", condition="direct", seed=0, passed=False, timed_out=False, wall_s=1,
                     agent=AgentResult(parsed=False), gateway=None, expected_states=None,
                     table=TABLE, today=TODAY, agent_exit_ok=False)
    assert r.status == "error"


def test_api_error_in_agent_result_is_error_not_a_failed_attempt():
    # Shape seen in a real run: is_error true, terminal_reason api_error, api_error_status 401, zero usage.
    out = json.dumps({"type": "result", "subtype": "success", "is_error": True, "terminal_reason": "api_error",
                      "api_error_status": 401, "total_cost_usd": 0, "modelUsage": {}, "num_turns": 1})
    res = parse_claude_style_result(out)
    assert res.api_error_status == 401
    r = build_record(run_id="r", task_id="t", condition="direct", seed=0, passed=False, timed_out=False, wall_s=1,
                     agent=res, gateway=None, expected_states=None, table=TABLE, today=TODAY, agent_exit_ok=False)
    assert r.status == "error" and "401" in r.note


def test_hitting_the_budget_limit_is_still_a_real_failed_attempt():
    out = json.dumps({"type": "result", "subtype": "error_max_budget_usd", "is_error": True, "total_cost_usd": 1.5,
                      "num_turns": 30, "modelUsage": {"claude-haiku-5-5": {"inputTokens": 5, "outputTokens": 6}}})
    res = parse_claude_style_result(out)
    assert res.api_error_status is None
    r = build_record(run_id="r", task_id="t", condition="direct", seed=0, passed=False, timed_out=False, wall_s=1,
                     agent=res, gateway=None, expected_states=None, table=TABLE, today=TODAY, agent_exit_ok=False)
    assert r.status == "ok"


def test_direct_run_timeout_without_result_is_not_an_infra_error():
    r = build_record(run_id="r", task_id="t", condition="direct", seed=0, passed=False, timed_out=True, wall_s=99,
                     agent=AgentResult(parsed=False), gateway=None, expected_states=None,
                     table=TABLE, today=TODAY, agent_exit_ok=False)
    assert r.status == "ok"


def test_claude_image_puts_claude_on_the_login_shell_path():
    # Regression: `bash -lc` resets PATH on Debian, so only a binary in /usr/local/bin is found.
    lines = ClaudeCode().install_lines("2.1.200")
    assert any("/usr/local/bin/claude" in l for l in lines)


def test_direct_cost_from_usage():
    usage = AgentResult(parsed=True, usage_by_model={"claude-haiku-5-5": ModelUsage(1_000_000, 1_000_000, 0, 0)})
    cost, tok = direct_cost(usage, TABLE, TODAY)
    assert cost == pytest.approx(0.6) and tok["output"] == 1_000_000
    assert direct_cost(AgentResult(parsed=True, usage_by_model={"nope": ModelUsage(1, 1)}), TABLE, TODAY)[0] is None


def test_parse_claude_result():
    out = "banner\n" + json.dumps({
        "type": "result", "subtype": "success", "num_turns": 7, "total_cost_usd": 0.12, "session_id": "s",
        "modelUsage": {"claude-haiku-5-5": {"inputTokens": 5, "outputTokens": 6, "cacheReadInputTokens": 7, "cacheCreationInputTokens": 8}},
    })
    r = parse_claude_style_result(out)
    assert r.parsed and r.turns == 7 and not r.is_error and r.cost_usd_estimate == 0.12
    assert r.usage_by_model["claude-haiku-5-5"] == ModelUsage(5, 6, 7, 8)
    assert parse_claude_style_result("no json here").parsed is False
    assert parse_claude_style_result('{"subtype":"error_max_budget_usd"}').is_error


def test_claude_command_has_no_secrets_and_needs_pinned_version():
    a = get_agent("claude_code")
    cmd = a.command(model="claude-haiku-5-5", max_budget_usd=1.5, prompt_path="/bench/prompt.txt")
    assert "--bare" in cmd and "--max-budget-usd 1.5" in cmd and "sk-" not in cmd
    with pytest.raises(ValueError, match="Pin the exact"):
        a.install_lines(None)
    assert any("install.sh | bash -s 2.1.200" in l for l in a.install_lines("2.1.200"))
    with pytest.raises(NotImplementedError):
        get_agent("codex")


def task(**over):
    d = dict(id="t", source="smoke", repo="https://example.com/r.git", base_commit="abc", license="MIT", prompt="p",
             image="python:3.12-slim", success_cmd="true", timeout_s=60, setup=["pip install -e ."])
    d.update(over)
    return parse_task(d)


def test_dockerfile_remote_and_local():
    a = get_agent("claude_code")
    df = render_dockerfile(task(), a, "2.1.200")
    assert df.startswith("FROM python:3.12-slim") and "git fetch -q --depth 1 origin abc" in df
    assert 'RUN ["bash", "-lc", "pip install -e ."]' in df and "install.sh | bash -s 2.1.200" in df
    assert "ANTHROPIC_API_KEY" not in df
    local = render_dockerfile(task(repo="local:repo", base_commit="local"), a, "2.1.200")
    assert "COPY repo/ /work/" in local and "git fetch" not in local


def test_config_loading(tmp_path):
    cfg = load_config()
    assert cfg.model == "claude-haiku-5-5" and cfg.batch_cap_usd == 25.0 and cfg.ship_rule.min_pairs == 30
    bad = tmp_path / "c.yaml"
    bad.write_text("agent: claude_code\nmodel: m\nsurprise: 1\nlimits: {batch_cap_usd: 1, run_cap_usd: 1}\nconditions: [direct]\n")
    with pytest.raises(ConfigError, match="unknown key"):
        load_config(bad)
    bad.write_text("agent: claude_code\nmodel: m\nlimits: {batch_cap_usd: 1, run_cap_usd: 2}\nconditions: [passthrough]\n")
    with pytest.raises(ConfigError, match="no run could ever start"):
        load_config(bad)


def test_report_outputs(tmp_path):
    conds = parse_conditions(["direct", "passthrough", "alone:noise_normalizer", "alone:cache_breakpoints", "all_safe"], "claude_code")
    runs = paired_runs(40, 1.0, 0.8) + paired_runs(40, 1.0, 1.0, ref="passthrough", treated="alone_noise_normalizer")
    # paired_runs emits the reference runs twice; keep one set
    seen, uniq = set(), []
    for r in runs:
        if r.run_id not in seen:
            seen.add(r.run_id)
            uniq.append(r)
    rule = ShipRule(bootstrap_resamples=500)
    md_path = write_all(tmp_path, uniq, conds, rule, {"agent": "claude_code", "model": "m", "fake_agent": True}, "x: 1\n", 20)
    md = md_path.read_text(encoding="utf-8")
    assert "SHIP" in md and "DON'T SHIP" in md and "NOT MEASURABLE WITH THIS AGENT" in md
    assert "fake agent" in md and "measured" in md.lower()
    for f in ("runs.csv", "summary.csv", "pairs.csv", "comparisons.csv", "versions.json"):
        assert (tmp_path / f).exists()
    first = (tmp_path / "summary.csv").read_bytes()
    write_all(tmp_path, uniq, conds, rule, {"agent": "claude_code", "model": "m", "fake_agent": True}, "x: 1\n", 20)
    assert (tmp_path / "summary.csv").read_bytes() == first          # reproducible byte for byte
