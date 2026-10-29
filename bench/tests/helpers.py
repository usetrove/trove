from trovebench.plan import run_id
from trovebench.records import RunRecord


def rec(task, seed, cond, cost, passed=True, status="ok", turns=10, retries=0, **kw):
    return RunRecord(
        run_id=run_id(task, cond, seed), task_id=task, condition=cond, seed=seed, status=status,
        passed=passed, timed_out=False, wall_s=60.0, turns=turns, tool_calls=turns * 2, retries=retries,
        cost_usd=cost, cost_source="gateway", **kw,
    )


def paired_runs(n, ref_cost, tr_cost, ref_pass=lambda i: True, tr_pass=lambda i: True,
                ref="passthrough", treated="all_safe"):
    out = []
    for i in range(n):
        t = f"task{i // 2}"
        s = i % 2
        c_ref = ref_cost(i) if callable(ref_cost) else ref_cost
        c_tr = tr_cost(i) if callable(tr_cost) else tr_cost
        out.append(rec(t, s, ref, c_ref, ref_pass(i)))
        out.append(rec(t, s, treated, c_tr, tr_pass(i)))
    return out
