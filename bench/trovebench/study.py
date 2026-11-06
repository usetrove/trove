"""Study files: the pre-registered comparison and success metric for one study.

A study file is written and locked (see prereg.py) before any run. Loading is strict so a
typo cannot silently change what was registered.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass, field
from pathlib import Path

import yaml

PRIMARY_METRICS = ("billed_cost_usd", "success_rate", "latency_s")
EFFORTS = ("low", "medium", "high")
_TOP = {"id", "title", "hypothesis", "primary_metric", "secondary_metrics", "baseline_condition", "conditions",
        "efforts", "tasks", "repeats", "order_seed", "analysis", "ship_rule", "budget", "agent", "model", "cost_basis", "parity", "claims", "recommends"}
_ANALYSIS = {"alpha", "bootstrap_resamples", "bootstrap_seed", "multiple_comparison"}
_BUDGET = {"batch_cap_usd", "run_cap_usd"}
_PARITY = {"none", "plugin", "gateway", "both"}
_RECOMMEND = {"rule_id", "kind", "name", "reason"}
_CLAIM = {"condition", "claimed_cost_reduction_pct", "source_url", "retrieved_at"}


class StudyError(ValueError):
    pass


@dataclass(frozen=True)
class Analysis:
    alpha: float = 0.05
    bootstrap_resamples: int = 10000
    bootstrap_seed: int = 1
    multiple_comparison: str = "holm"   # "holm" or "none"


@dataclass(frozen=True)
class Study:
    id: str
    title: str
    hypothesis: str
    primary_metric: str
    baseline_condition: str
    conditions: tuple[str, ...]
    tasks: str                               # folder of task manifests, relative to bench/
    repeats: int
    order_seed: int
    efforts: tuple[str, ...] = ()
    secondary_metrics: tuple[str, ...] = ()
    agent: str = "claude_code"
    model: str | None = None
    analysis: Analysis = field(default_factory=Analysis)
    ship_rule: dict = field(default_factory=dict)
    budget: dict = field(default_factory=dict)
    cost_basis: str = "agent"                # "agent": all conditions priced from the agent's own usage report
    parity: dict = field(default_factory=dict)   # role -> condition name, for the parity headline
    claims: tuple[dict, ...] = ()            # third-party savings claims to test against the measurement
    recommends: dict = field(default_factory=dict)   # condition -> the recommender rule it would justify
    sha256: str = ""

    @property
    def comparisons(self) -> tuple[tuple[str, str], ...]:
        """The registered comparisons: every non-baseline condition against the baseline."""
        return tuple((c, self.baseline_condition) for c in self.conditions if c != self.baseline_condition)


def load_study(path: Path) -> Study:
    text = path.read_text(encoding="utf-8")
    d = yaml.safe_load(text) or {}
    if not isinstance(d, dict):
        raise StudyError(f"{path}: expected a YAML mapping.")
    for key, allowed in ((None, _TOP), ("analysis", _ANALYSIS), ("budget", _BUDGET)):
        sect = d if key is None else (d.get(key) or {})
        extra = sorted(set(sect) - allowed)
        if extra:
            where = "" if key is None else f" under {key}"
            raise StudyError(f"{path}: unknown key(s){where}: {', '.join(extra)}. Allowed: {', '.join(sorted(allowed))}.")
    required = ("id", "title", "hypothesis", "primary_metric", "baseline_condition", "conditions", "tasks", "repeats", "order_seed")
    missing = [k for k in required if k not in d]
    if missing:
        raise StudyError(f"{path}: missing required key(s) {', '.join(missing)}. A study must register its hypothesis, "
                         "primary metric, baseline and conditions before it runs.")
    try:
        study = Study(
            id=str(d["id"]), title=str(d["title"]), hypothesis=str(d["hypothesis"]),
            primary_metric=str(d["primary_metric"]), baseline_condition=str(d["baseline_condition"]),
            conditions=tuple(str(c) for c in d["conditions"]), tasks=str(d["tasks"]),
            repeats=int(d["repeats"]), order_seed=int(d["order_seed"]),
            efforts=tuple(str(e) for e in d.get("efforts") or ()),
            secondary_metrics=tuple(str(m) for m in d.get("secondary_metrics") or ()),
            agent=str(d.get("agent", "claude_code")), model=str(d["model"]) if d.get("model") else None,
            analysis=Analysis(**(d.get("analysis") or {})), ship_rule=dict(d.get("ship_rule") or {}),
            budget=dict(d.get("budget") or {}), cost_basis=str(d.get("cost_basis", "agent")),
            parity=dict(d.get("parity") or {}), claims=tuple(dict(c) for c in d.get("claims") or ()),
            recommends={str(k): dict(v) for k, v in (d.get("recommends") or {}).items()}, sha256=hashlib.sha256(text.encode()).hexdigest(),
        )
    except (TypeError, ValueError) as e:
        raise StudyError(f"{path}: {e}") from e
    _validate(study, path)
    return study


def _validate(s: Study, path: Path) -> None:
    if not s.id or not all(ch.isalnum() or ch in "-_" for ch in s.id):
        raise StudyError(f"{path}: id '{s.id}' must be non-empty and use only letters, digits, - and _ (it names a folder).")
    if s.primary_metric not in PRIMARY_METRICS:
        raise StudyError(f"{path}: primary_metric '{s.primary_metric}' must be one of {', '.join(PRIMARY_METRICS)}.")
    bad = [m for m in s.secondary_metrics if m not in PRIMARY_METRICS or m == s.primary_metric]
    if bad:
        raise StudyError(f"{path}: secondary_metrics {bad} must be different from the primary metric and one of {', '.join(PRIMARY_METRICS)}.")
    colon = [c for c in s.conditions if ":" in c]
    if colon:
        raise StudyError(f"{path}: write {colon[0]!r} as {colon[0].replace(':', '_')!r}. Studies name conditions the way records "
                         "do, so every comparison lines up with its runs.")
    if len(set(s.conditions)) != len(s.conditions):
        raise StudyError(f"{path}: a condition is listed twice.")
    if s.baseline_condition not in s.conditions:
        raise StudyError(f"{path}: baseline_condition '{s.baseline_condition}' is not in `conditions`.")
    if len(s.conditions) < 2:
        raise StudyError(f"{path}: a comparison needs at least two conditions.")
    if s.repeats < 1:
        raise StudyError(f"{path}: repeats must be at least 1.")
    bad = [e for e in s.efforts if e not in EFFORTS]
    if bad or len(set(s.efforts)) != len(s.efforts):
        raise StudyError(f"{path}: efforts must be unique values from {', '.join(EFFORTS)}; got {list(s.efforts)}.")
    if not 0 < s.analysis.alpha < 1 or s.analysis.bootstrap_resamples < 1000:
        raise StudyError(f"{path}: analysis.alpha must be between 0 and 1 and bootstrap_resamples at least 1000.")
    if s.cost_basis not in ("agent", "recorded"):
        raise StudyError(f"{path}: cost_basis must be 'agent' (same source for every condition) or 'recorded'.")
    if s.parity:
        extra = sorted(set(s.parity) - _PARITY)
        missing = [k for k in ("none", "plugin", "gateway") if k not in s.parity]
        if extra or missing:
            raise StudyError(f"{path}: parity needs none, plugin and gateway (optionally both); got {sorted(s.parity)}.")
        unknown = [v for v in s.parity.values() if v not in s.conditions]
        if unknown:
            raise StudyError(f"{path}: parity names condition(s) {unknown} that are not in `conditions`.")
    for cond, r in s.recommends.items():
        if cond not in s.conditions or cond == s.baseline_condition:
            raise StudyError(f"{path}: recommends names '{cond}', which must be a non-baseline condition of this study.")
        if not set(r) <= _RECOMMEND or not {"rule_id", "kind", "name"} <= set(r) or r["kind"] not in ("optimizer", "profile"):
            raise StudyError(f"{path}: recommends.{cond} needs rule_id, kind (optimizer or profile) and name; reason is optional.")
    for c in s.claims:
        if set(c) != _CLAIM:
            raise StudyError(f"{path}: each claim needs exactly {', '.join(sorted(_CLAIM))}; a claim without a source cannot be checked.")
        if c["condition"] not in s.conditions or c["condition"] == s.baseline_condition:
            raise StudyError(f"{path}: claim condition '{c['condition']}' must be a non-baseline condition of this study.")
        if not isinstance(c["claimed_cost_reduction_pct"], (int, float)) or not 0 < c["claimed_cost_reduction_pct"] < 100:
            raise StudyError(f"{path}: claimed_cost_reduction_pct must be a number between 0 and 100.")
    if s.analysis.multiple_comparison not in ("holm", "none"):
        raise StudyError(f"{path}: analysis.multiple_comparison must be 'holm' or 'none'.")
