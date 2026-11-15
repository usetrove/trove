"""What did the gateway optimizers actually do in a finished study? Free; reads only the saved run folders.

    python bench/scripts/activity.py bench/results/<DATE>/<STUDY_ID>            # diagnosis table
    python bench/scripts/activity.py bench/results/<DATE>/<STUDY_ID> --markdown # a labelled block for the README

Reads <study>/ledger.jsonl and each run's trove-data/trove.db (metadata only: counts, tokens, money). It prints,
per condition: requests, billed input and output tokens, cache hit share, and for the optimizers the tokens they
removed (ESTIMATED: removed bytes over a bytes-per-token ratio, as the gateway records them), plus whether the
conditions really ran in the optimizer state they were meant to (a Trove downgrade to shadow would be a no-op).
It also splits runs by how long the session was, because collapsing duplicate results should matter more in long
sessions. Nothing here changes a verdict.
"""

from __future__ import annotations

import argparse
import json
import sqlite3
import statistics as st
import sys
from collections import defaultdict
from pathlib import Path


def load_ledger(study: Path) -> dict[str, dict]:
    p = study / "ledger.jsonl"
    if not p.exists():
        sys.exit(f"STOP: {p} not found. Point this at a finished study folder.")
    latest = {}
    for line in p.read_text(encoding="utf-8").splitlines():
        if line.strip():
            r = json.loads(line)
            latest[r["run_id"]] = r
    return {k: v for k, v in latest.items() if v["status"] == "ok"}


def run_numbers(db: Path) -> dict | None:
    if not db.exists():
        return None
    con = sqlite3.connect(f"file:{db.as_posix()}?mode=ro", uri=True)
    try:
        row = con.execute(
            "SELECT COUNT(*), COALESCE(SUM(input_uncached_tokens),0), COALESCE(SUM(cache_write_tokens),0),"
            " COALESCE(SUM(cache_read_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(tokens_removed),0),"
            " COALESCE(SUM(cost_diff_usd),0), COALESCE(SUM(cost_usd),0),"
            " COALESCE(SUM(tokens_removed > 0),0) FROM requests").fetchone()
        pins = [json.loads(s) for (s,) in con.execute("SELECT states FROM session_pins")]
    finally:
        con.close()
    keys = ["requests", "uncached", "cache_write", "cache_read", "output", "removed", "cost_diff_usd", "billed_usd", "req_trimmed"]
    out = dict(zip(keys, row))
    out["pins"] = pins
    return out


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("study", type=Path)
    ap.add_argument("--markdown", action="store_true", help="print a README block with every figure labelled")
    a = ap.parse_args()
    led = load_ledger(a.study)
    by_cond: dict[str, list[dict]] = defaultdict(list)
    missing = 0
    for rid, rec in led.items():
        nums = run_numbers(a.study / "runs" / rid / "trove-data" / "trove.db")
        if nums is None:
            missing += 1
            continue
        by_cond[rec["condition"]].append({**nums, "turns": rec["turns"], "task": rec["task_id"], "passed": rec["passed"]})
    if not by_cond:
        sys.exit("STOP: no run has a trove-data/trove.db. Gateway conditions save one; was this a hooks-only study?")

    rows = []
    for cond, runs in sorted(by_cond.items()):
        tot = lambda k: sum(r[k] for r in runs)
        inp = tot("uncached") + tot("cache_write") + tot("cache_read")
        pinned_on = sum(1 for r in runs if any(v == "on" for p in r["pins"] for v in p.values()))
        rows.append(dict(cond=cond, runs=len(runs), requests=tot("requests"), input=inp, output=tot("output"),
                         cache_read_share=100 * tot("cache_read") / inp if inp else 0.0,
                         removed=tot("removed"), removed_pct=100 * tot("removed") / (inp + tot("removed")) if inp else 0.0,
                         trimmed_req_pct=100 * tot("req_trimmed") / tot("requests") if tot("requests") else 0.0,
                         diff=tot("cost_diff_usd"), billed=tot("billed_usd"), pinned_on=pinned_on))

    if a.markdown:
        print("### What the gateway optimizers did in this study\n")
        print("| condition | runs | input tokens (measured) | tokens removed (estimated) | share of input removed (estimated) | requests changed |")
        print("|---|---|---|---|---|---|")
        for r in rows:
            print(f"| {r['cond']} | {r['runs']} | {r['input']:,} | {r['removed']:,} | {r['removed_pct']:.2f}% | {r['trimmed_req_pct']:.1f}% |")
        print("\nTokens removed are estimates (removed bytes over a bytes-per-token ratio). They are not a bill saving: "
              "the bill is compared in the study report, where cost is also estimated (agent-reported usage at list price) "
              "and pass/fail is measured. Say what the study does not cover: these tasks only, one model, one agent version.")
        return

    print(f"study: {a.study}   ok runs with a gateway database: {sum(r['runs'] for r in rows)}   without: {missing}\n")
    hdr = f"{'condition':34} {'runs':>5} {'requests':>9} {'input tok':>12} {'cache-read %':>12} {'removed tok (est)':>18} {'removed % (est)':>16} {'reqs changed %':>15} {'runs w/ state on':>17}"
    print(hdr); print("-" * len(hdr))
    for r in rows:
        print(f"{r['cond']:34} {r['runs']:>5} {r['requests']:>9} {r['input']:>12,} {r['cache_read_share']:>11.1f}% {r['removed']:>18,} {r['removed_pct']:>15.2f}% {r['trimmed_req_pct']:>14.1f}% {r['pinned_on']:>17}")

    on = [r for r in rows if r["removed"] > 0 or r["pinned_on"] > 0]
    print("\nHow to read this:")
    if not on:
        print("  * No optimizer ever removed anything and none ran in the 'on' state. The optimizer conditions were effectively a no-op.")
    for r in on:
        if r["pinned_on"] < r["runs"]:
            print(f"  * {r['cond']}: only {r['pinned_on']} of {r['runs']} runs ran with the optimizer 'on'. The rest were downgraded or off, which dilutes any effect.")
        if r["removed_pct"] < 1.0:
            print(f"  * {r['cond']}: removed about {r['removed_pct']:.2f}% of input tokens. That is too little to move the bill, which explains a ~0% cost change.")
        else:
            print(f"  * {r['cond']}: removed about {r['removed_pct']:.1f}% of input tokens (ESTIMATED). If the bill did not fall, look at the cache: removal early in a session can invalidate cached prefixes.")
    base = next((r for r in rows if r["cond"] == "passthrough"), None)
    for r in on:
        if base and r["cond"] != base["cond"]:
            print(f"  * {r['cond']} vs passthrough cache-read share: {r['cache_read_share']:.1f}% vs {base['cache_read_share']:.1f}%. "
                  f"{'A lower share means the optimizer broke cached prefixes.' if r['cache_read_share'] < base['cache_read_share'] - 2 else 'No sign of cache damage.'}")

    print("\nBy session length (all conditions with removal; turns are measured, removed tokens estimated):")
    treated = [r for c, runs in by_cond.items() if c != "passthrough" for r in runs]
    if len(treated) >= 6:
        cut = st.median(r["turns"] for r in treated)
        for label, sel in (("short (<= median turns)", [r for r in treated if r["turns"] <= cut]),
                           ("long  (> median turns)", [r for r in treated if r["turns"] > cut])):
            inp = sum(r["uncached"] + r["cache_write"] + r["cache_read"] for r in sel)
            rem = sum(r["removed"] for r in sel)
            print(f"  {label:26} runs {len(sel):>4}   removed {rem:>10,} tok   = {100 * rem / (inp + rem) if inp else 0:.2f}% of input")
        print(f"  (median turns = {cut:g}). If long sessions remove much more, test long tasks: bash bench/scripts/92_long_tasks_study.sh")
    else:
        print("  too few treated runs to split.")


if __name__ == "__main__":
    main()
