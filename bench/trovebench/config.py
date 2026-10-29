"""bench/config.yaml: strict loading so a typo never silently changes an experiment."""

from __future__ import annotations

import hashlib
from dataclasses import dataclass
from pathlib import Path

import yaml

from .cost import Profile
from .stats import ShipRule

DEFAULT_PATH = Path(__file__).resolve().parents[1] / "config.yaml"
_TOP = {"agent", "model", "claude_version", "repeats", "order_seed", "limits", "conditions", "ship_rule", "estimate"}
_LIMITS = {"batch_cap_usd", "run_cap_usd", "run_timeout_s"}


class ConfigError(ValueError):
    pass


@dataclass(frozen=True)
class Config:
    agent: str
    model: str
    claude_version: str | None
    repeats: int
    order_seed: int
    batch_cap_usd: float
    run_cap_usd: float
    run_timeout_s: int
    conditions: tuple[str, ...]
    ship_rule: ShipRule
    profile: Profile
    sha256: str


def load_config(path: Path | None = None) -> Config:
    p = path or DEFAULT_PATH
    raw_text = p.read_text(encoding="utf-8")
    d = yaml.safe_load(raw_text) or {}
    if not isinstance(d, dict):
        raise ConfigError(f"{p}: expected a YAML mapping.")
    extra = sorted(set(d) - _TOP)
    if extra:
        raise ConfigError(f"{p}: unknown key(s) {', '.join(extra)}. Allowed: {', '.join(sorted(_TOP))}.")
    limits = d.get("limits") or {}
    extra = sorted(set(limits) - _LIMITS)
    if extra:
        raise ConfigError(f"{p}: unknown limits key(s) {', '.join(extra)}.")
    try:
        cfg = Config(
            agent=str(d["agent"]), model=str(d["model"]),
            claude_version=str(d["claude_version"]) if d.get("claude_version") else None,
            repeats=int(d.get("repeats", 3)), order_seed=int(d.get("order_seed", 1)),
            batch_cap_usd=float(limits["batch_cap_usd"]), run_cap_usd=float(limits["run_cap_usd"]),
            run_timeout_s=int(limits.get("run_timeout_s", 1800)),
            conditions=tuple(d["conditions"]),
            ship_rule=ShipRule.from_dict(d.get("ship_rule") or {}),
            profile=Profile.from_dict(d.get("estimate") or {}),
            sha256=hashlib.sha256(raw_text.encode()).hexdigest(),
        )
    except KeyError as e:
        raise ConfigError(f"{p}: missing required key {e}. See the default bench/config.yaml.") from e
    except (TypeError, ValueError) as e:
        raise ConfigError(f"{p}: {e}") from e
    if cfg.repeats < 1 or cfg.batch_cap_usd <= 0 or cfg.run_cap_usd <= 0 or cfg.run_timeout_s < 30:
        raise ConfigError(f"{p}: repeats must be >= 1, caps must be > 0 and run_timeout_s >= 30.")
    if cfg.run_cap_usd > cfg.batch_cap_usd:
        raise ConfigError(f"{p}: limits.run_cap_usd ({cfg.run_cap_usd}) is above batch_cap_usd ({cfg.batch_cap_usd}), so no run could ever start.")
    if "passthrough" not in cfg.conditions and cfg.ship_rule.reference == "passthrough":
        raise ConfigError(f"{p}: ship_rule.reference is 'passthrough' but that condition is not in `conditions`.")
    return cfg
