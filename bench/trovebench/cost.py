"""Dry-run cost estimate. Every figure here is an ESTIMATE; nothing is spent."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from typing import Sequence

from .plan import RunSpec
from .pricing import PricingTable, request_cost


@dataclass(frozen=True)
class Profile:
    """Assumed tokens for one agent run, summed over all its API calls.
    A rough prior for a small bug-fix task; replace with `observed_mean_usd` after a first batch."""
    uncached_input: int = 30_000
    cache_write: int = 60_000
    cache_read: int = 1_200_000
    output: int = 20_000

    @staticmethod
    def from_dict(d: dict) -> "Profile":
        return Profile(**d)


@dataclass(frozen=True)
class Estimate:
    n_runs: int
    n_remaining: int
    expected_usd: float          # estimated
    worst_case_usd: float        # remaining runs x per-run cap
    batch_cap_usd: float
    basis: str                   # what the expected figure is based on

    @property
    def worst_case_fits(self) -> bool:
        return self.worst_case_usd <= self.batch_cap_usd


def estimate(
    specs: Sequence[RunSpec],
    already_done: set[str],
    table: PricingTable,
    model: str,
    *,
    run_cap_usd: float,
    batch_cap_usd: float,
    profile: Profile = Profile(),
    observed_mean_usd: float | None = None,
    today: date | None = None,
) -> Estimate:
    remaining = [s for s in specs if s.run_id not in already_done]
    if observed_mean_usd is not None:
        per_run = Decimal(str(observed_mean_usd))
        basis = "mean cost of runs already finished in this batch"
    else:
        rates = table.rates(model, today or date.today())
        per_run = request_cost(
            rates, input_uncached=profile.uncached_input, cache_write=profile.cache_write,
            cache_read=profile.cache_read, output=profile.output,
        )
        basis = (f"assumed profile of {profile.uncached_input + profile.cache_write + profile.cache_read:,} input "
                 f"and {profile.output:,} output tokens per run at {model} list prices (pricing {table.data_version})")
    return Estimate(
        n_runs=len(specs),
        n_remaining=len(remaining),
        expected_usd=float(per_run * len(remaining)),
        worst_case_usd=run_cap_usd * len(remaining),
        batch_cap_usd=batch_cap_usd,
        basis=basis,
    )
