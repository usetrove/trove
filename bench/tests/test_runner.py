from datetime import date

import pytest
from helpers import rec

from trovebench.conditions import parse_conditions
from trovebench.cost import Profile, estimate
from trovebench.ledger import Ledger
from trovebench.manifest import parse_task
from trovebench.plan import RunSpec, build_plan, read_plan, run_id, write_plan
from trovebench.pricing import PricingError, load, request_cost
from trovebench.redact import redact
from trovebench.runner import STOP_CAP, STOP_DONE, STOP_ERRORS, STOP_INTERRUPTED, execute


def task(i):
    return parse_task(dict(id=f"t{i}", source="smoke", repo="r", base_commit="c", license="MIT",
                           prompt="p", image="i", success_cmd="true", timeout_s=60))


CONDS = parse_conditions(["direct", "passthrough", "alone:cache_breakpoints", "all_safe"], "claude_code")


def specs(n=3, repeats=2):
    return build_plan([task(i) for i in range(n)], CONDS, repeats, order_seed=7)


def fake(cost=0.1, status="ok", calls=None):
    def run_one(s: RunSpec):
        if calls is not None:
            calls.append(s.run_id)
        return rec(s.task_id, s.seed, s.condition, cost, status=status)
    return run_one


# --- plan -------------------------------------------------------------------

def test_plan_skips_unmeasurable_and_is_deterministic():
    a, b = specs(), specs()
    assert a == b
    assert len(a) == 3 * 3 * 2          # 3 measurable conditions
    assert not any("cache_breakpoints" in s.condition for s in a)
    assert a != build_plan([task(i) for i in range(3)], CONDS, 2, order_seed=8)
    # randomized, not grouped by condition
    assert [s.condition for s in a][:6] != sorted(s.condition for s in a)[:6]


def test_plan_roundtrip_and_ids(tmp_path):
    p = tmp_path / "plan.json"
    write_plan(p, specs())
    assert read_plan(p) == specs()
    assert run_id("t", "all_safe", 1) == "t__all_safe__s1"
    with pytest.raises(ValueError):
        build_plan([task(0)], CONDS, 0, 1)


# --- executor ---------------------------------------------------------------

def test_runs_everything_when_budget_allows(tmp_path):
    led = Ledger(tmp_path / "l.jsonl")
    r = execute(specs(), led, fake(0.1), batch_cap_usd=100, run_cap_usd=1.5, log=lambda m: None)
    assert r.stop_reason == STOP_DONE and r.ran == 18 and r.remaining == 0
    assert r.spent_usd == pytest.approx(1.8)


def test_hard_cap_stops_before_overspending(tmp_path):
    led = Ledger(tmp_path / "l.jsonl")
    r = execute(specs(), led, fake(0.4), batch_cap_usd=3.0, run_cap_usd=1.0, log=lambda m: None)
    assert r.stop_reason == STOP_CAP
    assert r.spent_usd <= 3.0
    # The rule is spent + run_cap > batch_cap (strict): 5 runs = 2.0, 2.0 + 1.0 is not > 3.0, so a 6th runs;
    # after 6 runs 2.4 + 1.0 > 3.0 stops the batch.
    assert r.ran == 6 and r.remaining == 12


def test_mismatched_run_id_is_a_loud_bug(tmp_path):
    from helpers import rec as mk

    led = Ledger(tmp_path / "l.jsonl")
    with pytest.raises(RuntimeError, match="asked to run"):
        execute(specs(), led, lambda s: mk("other", 0, "direct", 0.1), batch_cap_usd=100, run_cap_usd=1.0,
                log=lambda m: None)


def test_resume_does_not_rerun_finished(tmp_path):
    path = tmp_path / "l.jsonl"
    first = []
    execute(specs(), Ledger(path), fake(0.1, calls=first), batch_cap_usd=0.55, run_cap_usd=0.1, log=lambda m: None)
    assert 0 < len(first) < 18
    second = []
    r = execute(specs(), Ledger(path), fake(0.1, calls=second), batch_cap_usd=100, run_cap_usd=1.0, log=lambda m: None)
    assert r.stop_reason == STOP_DONE and r.skipped_done == len(first)
    assert set(first).isdisjoint(second) and len(first) + len(second) == 18
    assert Ledger(path).spent(1.0) == pytest.approx(1.8)


def test_interrupt_saves_progress(tmp_path):
    path = tmp_path / "l.jsonl"
    n = {"i": 0}

    def run_one(s):
        n["i"] += 1
        if n["i"] == 4:
            raise KeyboardInterrupt
        return rec(s.task_id, s.seed, s.condition, 0.1)

    r = execute(specs(), Ledger(path), run_one, batch_cap_usd=100, run_cap_usd=1.0, log=lambda m: None)
    assert r.stop_reason == STOP_INTERRUPTED and len(Ledger(path).records) == 3


def test_exception_becomes_error_record_and_is_retried(tmp_path):
    path = tmp_path / "l.jsonl"
    key = "sk-ant-api03-SECRETSECRETSECRET"

    def boom(s):
        raise RuntimeError(f"docker failed, key={key}")

    led = Ledger(path)
    r = execute(specs(), led, boom, batch_cap_usd=100, run_cap_usd=1.0, log=lambda m: None)
    assert r.stop_reason == STOP_ERRORS and r.ran == 3
    assert key not in path.read_text()
    assert led.spent(1.0) == pytest.approx(3.0)          # unknown cost is charged at the cap
    # errored runs are not "done": a resume retries them, and the old attempt still counts as spend
    again = Ledger(path)
    r2 = execute(specs(), again, fake(0.1), batch_cap_usd=100, run_cap_usd=1.0, log=lambda m: None)
    assert r2.stop_reason == STOP_DONE and r2.ran == 18
    assert again.spent(1.0) == pytest.approx(3.0 + 1.8)
    assert all(x.status == "ok" for x in again.final_records())


def test_ledger_rejects_corrupt_line(tmp_path):
    p = tmp_path / "l.jsonl"
    p.write_text("{not json}\n")
    with pytest.raises(ValueError, match="do not delete"):
        Ledger(p)


def test_ledger_rejects_duplicate_finished(tmp_path):
    led = Ledger(tmp_path / "l.jsonl")
    led.add(rec("t", 0, "direct", 0.1))
    with pytest.raises(ValueError, match="already finished"):
        led.add(rec("t", 0, "direct", 0.1))


# --- estimate / pricing ------------------------------------------------------

def test_pricing_loads_and_costs_haiku():
    t = load()
    r = t.rates("claude-haiku-5-5", date(2026, 10, 29))
    assert str(r.input) == "0.1" and r.long_context_threshold == 100000
    # 1M uncached input at $0.10 + 1M output at $0.50
    assert float(request_cost(r, input_uncached=1_000_000, cache_write=0, cache_read=0, output=1_000_000)) == pytest.approx(0.6)
    assert float(request_cost(r, input_uncached=1_000_000, cache_write=0, cache_read=0, output=0, long_context=True)) == pytest.approx(0.5)


def test_pricing_unknown_model_or_date():
    t = load()
    with pytest.raises(PricingError, match="No price"):
        t.rates("claude-nonexistent", date(2026, 10, 29))
    with pytest.raises(PricingError, match="No price"):
        t.rates("claude-haiku-5-5", date(2020, 1, 1))


def test_estimate_dry_run():
    t = load()
    s = specs()
    e = estimate(s, set(), t, "claude-haiku-5-5", run_cap_usd=1.5, batch_cap_usd=25, today=date(2026, 10, 29))
    assert e.n_runs == e.n_remaining == 18
    assert e.worst_case_usd == pytest.approx(27.0) and not e.worst_case_fits
    assert 0 < e.expected_usd < 5 and "assumed profile" in e.basis
    done = {s[0].run_id, s[1].run_id}
    e2 = estimate(s, done, t, "claude-haiku-5-5", run_cap_usd=1.5, batch_cap_usd=25, observed_mean_usd=0.2)
    assert e2.n_remaining == 16 and e2.expected_usd == pytest.approx(3.2) and "already finished" in e2.basis


# --- redaction ---------------------------------------------------------------

@pytest.mark.parametrize(
    "text",
    [
        "x-api-key: sk-ant-api03-abcdefghijklmnop",
        "Authorization: Bearer sk-proj-abcdefghijklmnopqrstu",
        "ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnop",
        "token ghp_abcdefghijklmnopqrstuvwxyz0123",
        "GITHUB_TOKEN=abcd1234efgh5678",
    ],
)
def test_redact_patterns(text):
    out = redact(text)
    assert "[REDACTED]" in out
    for frag in ("abcdefghijklmnop", "abcd1234efgh5678"):
        assert frag not in out


def test_redact_exact_secret_and_passthrough():
    assert redact("my key is hunter2hunter2", secrets=["hunter2hunter2"]) == "my key is [REDACTED]"
    assert redact("nothing secret here") == "nothing secret here"
