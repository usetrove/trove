"""Generate the recommender's bundled defaults (and the experiment priors) from benchmark results.

The output is a pure function of two inputs: the seed rules (bench/defaults_seed.toml, evidence
"none") and the result folders passed in. Nothing is written by hand. A rule becomes
evidence = "benchmark" only when a pre-registered study's pooled result clears the ship rule and no
effort level contradicts it; a rule the results contradict is dropped. Simulated and fake-agent
results are refused outright.
"""

from __future__ import annotations

import csv
import json
import re
from dataclasses import dataclass
from pathlib import Path

try:
    import tomllib
except ModuleNotFoundError:  # Python 3.10
    import tomli as tomllib

from .study import StudyError, load_study
from .studyreport import ALL, ship_rule_for

SUPPORTED = "supported"
CONTRADICTED = "contradicted"
INCONCLUSIVE = "inconclusive"
# Mirrors internal/experiment/prior.go (ShipMinCostReduction, ShipSuccessMargin, ShipMinPairs); a test keeps them in sync.
GO_MIN_COST_REDUCTION_PCT = 5.0
GO_SUCCESS_MARGIN_PTS = 5.0
GO_MIN_PAIRS = 30


class DefaultsError(ValueError):
    pass


@dataclass(frozen=True)
class Finding:
    condition: str
    rule_id: str
    kind: str
    name: str
    reason: str          # template for a rule that has no seed entry
    status: str
    why: str
    source: str
    study_id: str
    results_dir: str
    pairs: int
    cost_change: float   # fractions, as priors.toml wants them
    cost_ci: tuple[float, float]
    success_diff: float
    success_ci_low: float


def _read_csv(path: Path) -> list[dict]:
    with open(path, encoding="utf-8", newline="") as f:
        return list(csv.DictReader(f))


def evaluate(results_dir: Path, *, allow_simulated: bool = False) -> list[Finding]:
    """Findings for every condition a study registered a recommendation for."""
    lock_p, cfg_p, ver_p, cmp_p = (results_dir / n for n in ("study.lock.json", "config.used.yaml", "versions.json", "comparisons.csv"))
    missing = [p.name for p in (lock_p, cfg_p, ver_p, cmp_p) if not p.exists()]
    if missing:
        raise DefaultsError(f"{results_dir} is not a finished study folder (missing {', '.join(missing)}).")
    lock = json.loads(lock_p.read_text(encoding="utf-8"))
    versions = json.loads(ver_p.read_text(encoding="utf-8"))
    if (versions.get("simulated") or versions.get("fake_agent")) and not allow_simulated:
        raise DefaultsError(f"{results_dir} comes from simulated or fake-agent runs. Defaults are only generated from real measurements.")
    try:
        study = load_study(cfg_p)
    except StudyError as e:
        raise DefaultsError(str(e)) from e
    if study.sha256 != lock.get("study_sha256"):
        raise DefaultsError(f"{results_dir}: the study file no longer matches the hash locked at registration, so these results are not trusted.")
    rule = ship_rule_for(study)
    strict = (rule.min_cost_reduction_pct >= GO_MIN_COST_REDUCTION_PCT and rule.success_margin_pts <= GO_SUCCESS_MARGIN_PTS
              and rule.min_pairs >= GO_MIN_PAIRS)
    rows = _read_csv(cmp_p)
    out = []
    for cond, rec in sorted(study.recommends.items()):
        mine = [r for r in rows if r["condition"] == cond]
        pooled = next((r for r in mine if r["effort"] == ALL), None)
        source = f"bench:{study.id}@{lock['study_sha256'][:12]}"
        base = dict(condition=cond, rule_id=rec["rule_id"], kind=rec["kind"], name=rec["name"], reason=rec.get("reason", ""),
                    source=source, study_id=study.id, results_dir=f"{results_dir.resolve().parent.name}/{results_dir.resolve().name}")
        if pooled is None:
            out.append(Finding(**base, status=INCONCLUSIVE, why="no pooled comparison was produced", pairs=0,
                               cost_change=0.0, cost_ci=(0.0, 0.0), success_diff=0.0, success_ci_low=0.0))
            continue
        nums = dict(pairs=int(pooled["n_pairs"]), cost_change=float(pooled["rule_cost_change_pct"]) / 100,
                    cost_ci=(float(pooled["rule_cost_ci_lo"]) / 100, float(pooled["rule_cost_ci_hi"]) / 100),
                    success_diff=float(pooled["rule_success_diff_pts"]) / 100, success_ci_low=float(pooled["rule_success_ci_lo"]) / 100)
        bad_levels = [r["effort"] for r in mine if r["effort"] != ALL and r["verdict"] == "dont_ship"]
        if pooled["verdict"] == "dont_ship":
            status, why = CONTRADICTED, "the pooled result is a registered 'don't ship'"
        elif pooled["verdict"] != "ship":
            status, why = INCONCLUSIVE, f"the pooled verdict is {pooled['verdict']}"
        elif not strict:
            status, why = INCONCLUSIVE, ("the study's registered rule is weaker than the one Trove enforces "
                                         f"({GO_MIN_COST_REDUCTION_PCT:g}% reduction, {GO_SUCCESS_MARGIN_PTS:g}-point margin, {GO_MIN_PAIRS} pairs)")
        elif bad_levels:
            status, why = INCONCLUSIVE, f"the effect is contradicted at effort level(s) {', '.join(bad_levels)}"
        else:
            status, why = SUPPORTED, "the pooled result clears the registered ship rule and no effort level contradicts it"
        out.append(Finding(**base, status=status, why=why, **nums))
    return out


# --- TOML output ------------------------------------------------------------

def _v(x) -> str:
    if isinstance(x, bool):
        return "true" if x else "false"
    if isinstance(x, int):
        return str(x)
    if isinstance(x, float):
        return repr(round(x, 6))
    if isinstance(x, str):
        return json.dumps(x)
    if isinstance(x, list):
        return "[" + ", ".join(_v(i) for i in x) + "]"
    raise DefaultsError(f"cannot write {x!r} to TOML")


def _rule_text(r: dict) -> str:
    lines = ["[[rule]]"]
    for k in ("id", "kind", "name", "action", "evidence", "source", "reason"):
        if r.get(k) not in (None, ""):
            lines.append(f"{k} = {_v(r[k])}")
    if r.get("when"):
        lines.append("[rule.when]")
        lines += [f"{k} = {_v(v)}" for k, v in r["when"].items()]
    return "\n".join(lines)


def generate(seed_text: str, result_dirs: list[Path], *, allow_simulated: bool = False) -> tuple[str, str, list[Finding]]:
    """Returns (defaults.toml text, priors.toml text, every finding)."""
    seed = tomllib.loads(seed_text)
    if seed.get("version") != 1:
        raise DefaultsError("the seed file must be schema version 1.")
    findings = [f for d in sorted(result_dirs, key=lambda p: str(p.resolve())) for f in evaluate(d, allow_simulated=allow_simulated)]
    rules = [dict(r) for r in seed.get("rule", [])]
    for r in rules:
        if r.get("evidence") != "none":
            raise DefaultsError(f"seed rule {r.get('id')!r} is not evidence = \"none\"; the seed holds only rules of thumb.")
    # per recommended (kind, name): the strongest supported finding wins; any contradiction removes the rule
    best: dict[tuple[str, str], Finding] = {}
    dropped: set[tuple[str, str]] = set()
    for f in findings:
        key = (f.kind, f.name)
        if f.status == CONTRADICTED:
            dropped.add(key)
        elif f.status == SUPPORTED and (key not in best or f.pairs > best[key].pairs):
            best[key] = f
    out_rules: list[dict] = []
    for r in rules:
        key = (r["kind"], r["name"])
        if key in dropped:
            continue
        if key in best:
            r = {**r, "evidence": "benchmark", "source": best[key].source}
        out_rules.append(r)
    have = {(r["kind"], r["name"]) for r in out_rules}
    for key, f in sorted(best.items(), key=lambda kv: kv[1].rule_id):
        if key in have or key in dropped:
            continue
        if not f.reason.strip():
            raise DefaultsError(f"{f.study_id} found support for {f.name} but registered no `reason` template and the seed has no rule for it.")
        out_rules.append({"id": f.rule_id, "kind": f.kind, "name": f.name, "action": "shadow" if f.kind == "optimizer" else "use_profile",
                          "evidence": "benchmark", "source": f.source, "reason": f.reason, "when": {}})
    ids = [r["id"] for r in out_rules]
    if len(set(ids)) != len(ids):
        raise DefaultsError("two generated rules share an id; rename one in the study's `recommends`.")

    used = sorted({f.results_dir for f in best.values()})
    release = seed.get("release", "seed") if not best else "bench-" + _release_stamp(used)
    head = [
        "# GENERATED by `python -m trovebench defaults`. Do not edit by hand.",
        "# Inputs: bench/defaults_seed.toml (rules of thumb, evidence = \"none\") plus these pre-registered result folders:",
        *(f"#   {d}" for d in (used or ["(none: no result met the ship rule, so every rule is still a rule of thumb)"])),
        "# A rule is evidence = \"benchmark\" only when a study's pooled result cleared its registered ship rule and no effort level",
        "# contradicted it. Rules the results contradict are removed. See docs/benchmark-methodology.md and docs/recommend.md.",
        "# Rules only ever suggest the safe first step: an optimizer in shadow mode (estimate only, change nothing) or a settings profile.",
        "# Reasons are templates. Placeholders: {sessions} {files_read} {repeat_reads} {large_results} {dup_ratio} {error_share}",
        "# {compactions} {size} {framework} {task}. They are filled with numbers and fixed names only.",
        "version = 1",
        f"release = {_v(release)}",
        f"min_sessions = {int(seed.get('min_sessions', 3))}",
    ]
    defaults_text = "\n".join(head) + "\n\n" + "\n\n".join(_rule_text(r) for r in out_rules) + "\n"

    pri = ["# GENERATED by `python -m trovebench defaults`. Do not edit by hand. Version 1.",
           "# Each table is one optimizer's result from a pre-registered, real benchmark study, as fractions (-0.08 = 8% cheaper).",
           "version = 1"]
    for (kind, name), f in sorted(best.items()):
        if kind != "optimizer":
            continue
        pri += ["", f"[optimizers.{name}]", f"cost_change = {_v(f.cost_change)}", f"cost_ci_low = {_v(f.cost_ci[0])}",
                f"cost_ci_high = {_v(f.cost_ci[1])}", f"success_diff = {_v(f.success_diff)}", f"success_ci_low = {_v(f.success_ci_low)}",
                f"pairs = {f.pairs}", f"source = {_v(f.source)}"]
    return defaults_text, "\n".join(pri) + "\n", findings


def _release_stamp(dirs: list[str]) -> str:
    stamps = [m.group(0) for d in dirs if (m := re.search(r"\d{4}-\d{2}-\d{2}", d))]
    return max(stamps) if stamps else "-".join(dirs)
