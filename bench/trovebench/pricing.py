"""Read Trove's own pricing table (internal/pricing/pricing.json).

Used for the dry-run estimate and to cost the `direct` condition, which has no
gateway. Gateway conditions use the gateway's recorded cost_usd instead.
Standard tier, 5-minute cache writes. Values derived here are estimates.
"""

from __future__ import annotations

import json
from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from pathlib import Path

DEFAULT_PATH = Path(__file__).resolve().parents[2] / "internal" / "pricing" / "pricing.json"


class PricingError(ValueError):
    pass


@dataclass(frozen=True)
class Rates:
    """USD per million tokens."""
    input: Decimal
    output: Decimal
    cache_read: Decimal
    cache_write: Decimal
    long_context_threshold: int | None = None
    long: "Rates | None" = None


@dataclass(frozen=True)
class PricingTable:
    data_version: str
    entries: tuple[dict, ...]

    def rates(self, model: str, at: date) -> Rates:
        cands = [
            e for e in self.entries
            if e["provider"] == "anthropic"
            and model in (e["model"], *e.get("aliases", []))
            and date.fromisoformat(e["effective_from"]) <= at
        ]
        if not cands:
            raise PricingError(
                f"No price for model '{model}' on {at} in pricing.json (data_version {self.data_version}). "
                "Use an exact model ID listed there, or update internal/pricing/pricing.json first."
            )
        e = max(cands, key=lambda e: e["effective_from"])
        std = e["standard"]
        long = None
        if "long_context" in e:
            lc = e["long_context"]
            long = _rates(lc)
            return Rates(**_asdict(_rates(std)), long_context_threshold=lc["threshold_tokens"], long=long)
        return _rates(std)


def _asdict(r: Rates) -> dict:
    return dict(input=r.input, output=r.output, cache_read=r.cache_read, cache_write=r.cache_write)


def _rates(t: dict) -> Rates:
    cw = t["cache_write"]
    cw = cw["5m"] if isinstance(cw, dict) else cw
    return Rates(Decimal(t["input"]), Decimal(t["output"]), Decimal(t["cache_read"]), Decimal(cw))


def load(path: Path | None = None) -> PricingTable:
    p = path or DEFAULT_PATH
    try:
        data = json.loads(p.read_text(encoding="utf-8"))
    except FileNotFoundError as e:
        raise PricingError(f"Pricing table not found at {p}. Run bench from a Trove checkout.") from e
    if data.get("schema_version") != 1:
        raise PricingError(f"{p}: schema_version {data.get('schema_version')} is not supported (expected 1).")
    return PricingTable(data["data_version"], tuple(data["entries"]))


def request_cost(
    rates: Rates, *, input_uncached: int, cache_write: int, cache_read: int, output: int, long_context: bool = False
) -> Decimal:
    r = rates.long if (long_context and rates.long is not None) else rates
    total = (
        r.input * input_uncached + r.cache_write * cache_write + r.cache_read * cache_read + r.output * output
    )
    return total / Decimal(1_000_000)
