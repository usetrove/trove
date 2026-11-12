"""Assemble SUITE_REPORT.md from one suite run's outputs. Every figure is labelled measured, estimated or SIMULATED,
and missing pieces are said to be missing, never filled in.

    python qa/suite/report.py qa/results/suite-<stamp>   > SUITE_REPORT.md
"""

from __future__ import annotations

import json
import sys
from pathlib import Path


def read_tsv(path: Path) -> list[list[str]]:
    if not path.exists():
        return []
    rows = []
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        if line.strip() and not line.startswith("#"):
            rows.append(line.split("\t"))
    return rows


def table(header: list[str], rows: list[list[str]]) -> str:
    out = ["| " + " | ".join(header) + " |", "|" + "---|" * len(header)]
    out += ["| " + " | ".join(r) + " |" for r in rows]
    return "\n".join(out)


def main(out: Path) -> None:
    meta = json.loads((out / "suite.json").read_text(encoding="utf-8")) if (out / "suite.json").exists() else {}
    free = out / "free"
    print(f"# Trove savings suite: {meta.get('stamp', out.name)}\n")
    print(f"Commit: `{meta.get('commit', 'unknown')}`. Paid tier: {meta.get('paid_status', 'unknown')}.\n")
    print("Labels: **measured** = observed on this machine; **estimated** = computed by a model or list price; "
          "**SIMULATED** = planted effects, not a result about any agent.\n")

    # 1. Contract and safety.
    print("## 1. Contract and safety (measured, pass/fail)\n")
    cli = read_tsv(free / "cli_matrix.tsv")
    if cli:
        fails = [r for r in cli if len(r) >= 3 and r[2] == "FAIL"]
        print(f"CLI contract checks: {len(cli) - len(fails)} pass, {len(fails)} fail.\n")
        for r in fails:
            print(f"- FAIL {r[0]} {r[1]}: {r[3] if len(r) > 3 else ''}")
    else:
        print("CLI contract checks: not run (no output found).")
    pers = read_tsv(free / "personas.tsv")
    if pers:
        fails = [r for r in pers if len(r) >= 3 and r[2] == "FAIL"]
        print(f"\nUser journeys: {len(pers) - len(fails)} of {len(pers)} checks pass.")
        for r in fails:
            print(f"- FAIL {r[0]}: {r[1]}")
    print()

    # 2. Gateway.
    print("## 2. Gateway as a pro user (measured, zero spend)\n")
    oh = read_tsv(free / "overhead.tsv")
    if oh and oh[0][0] == "path":
        data = {}
        for r in oh[1:]:
            if len(r) >= 6:
                data.setdefault(r[1], {})[r[0]] = r
        rows = []
        for conc in sorted(data, key=lambda c: int(c)):
            d, g = data[conc].get("direct"), data[conc].get("gateway")
            if d and g:
                added = float(g[3]) - float(d[3])
                rows.append([conc, g[2], g[3], g[5], d[3], f"{added:.2f}", g[6] if len(g) > 6 else ""])
        print(table(["sessions", "requests", "p50 via gateway (ms)", "p99 via gateway (ms)", "p50 direct (ms)",
                     "p50 added (ms)", "errors"], rows))
        print("\nThe pro-user budget is 5 ms added at p50 (CLAUDE.md). Rows above that budget are findings.")
    else:
        print("Gateway overhead: not run (no output found).")
    print()

    # 3. Ceilings on real history.
    print("## 3. Ceilings on your real Claude Code history (measured bytes, estimated tokens)\n")
    rp = read_tsv(free / "replay.tsv")
    if rp:
        body = [r for r in rp if len(r) >= 12 and r[0] != "optimizer"]
        hdr = ["optimizer", "mode", "requests", "changed", "changed %", "bytes removed %", "est. tokens removed"]
        rows = [[r[0], r[1], r[2], r[3], r[4], r[7], r[8]] for r in body]
        if rows:
            print(table(hdr, rows))
        else:
            print("\n".join(line for line in (free / "replay.tsv").read_text(encoding="utf-8").splitlines()))
        print("\nLimits: the logs have no system prompt or tool definitions; requests are sampled; tokens are "
              "engine estimates. This is a ceiling on request bytes, not a dollar saving.")
    else:
        print("Ceilings: not run (no output found).")
    print()

    # 4. Statistics.
    print("## 4. Do the statistics tell the truth? (SIMULATED)\n")
    val = free / "validity.md"
    print(val.read_text(encoding="utf-8").strip() if val.exists() else "Validity checks: not run.")
    print()

    # 5. Paid tier.
    print("## 5. Paid tier (real model, capped)\n")
    print(f"Status: {meta.get('paid_status', 'not requested')}.\n")
    print("No savings figure from a real model is reported here, because none was measured. Those figures come from "
          "the paired runs in the bench lab, which are not run yet.")


if __name__ == "__main__":
    main(Path(sys.argv[1]))
