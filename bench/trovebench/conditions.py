"""Experiment conditions and the Trove config.toml each one needs.

Facts this module encodes (from internal/optimize and internal/measure, verified
by reading the code, not assumed):
  * optimizer names and their fixed order are PipelineNames in optimize/optimizer.go;
  * volatile_prefix_mover and cache_breakpoints only act for agents marked custom,
    and Claude Code / Codex can never be marked custom;
  * output_trimmer is gated to shadow, config validation rejects "on";
  * measure.Gate downgrades "on" to "shadow" until baseline_days and
    baseline_sessions have passed, so a bench run must set both to 0.
"""

from __future__ import annotations

from dataclasses import dataclass

OPTIMIZERS = (
    "volatile_prefix_mover",
    "noise_normalizer",
    "output_trimmer",
    "duplicate_result_collapser",
    "cache_breakpoints",
)

# Hook-side optimizers (internal/hooks). They run in the agent's own hooks, with
# no gateway, so they work for subscription users. noise_normalizer and
# output_trimmer also run there on tool output, but they are named above because
# the gateway runs them too; for a hooks condition they are set in the same
# [optimizers] table. output_trimmer needs the MCP server registered, which a
# benchmark run does not do, so it stays out of the hooks conditions.
HOOK_OPTIMIZERS = (
    "hook_repeat_read",
    "hook_read_limit",
    "hook_quiet_commands",
    "hook_large_output",
)
HOOKS_ON = ("hook_repeat_read", "hook_read_limit", "hook_quiet_commands", "noise_normalizer")

# Optimizers that can be "on" for each agent. Everything else is reported as
# "not measurable with this agent" rather than silently measured as a no-op.
_CUSTOM_ONLY = {"volatile_prefix_mover", "cache_breakpoints"}
_GATED = {"output_trimmer"}


def supported_optimizers(agent: str) -> tuple[str, ...]:
    if agent in ("claude_code", "codex"):
        return tuple(o for o in OPTIMIZERS if o not in _CUSTOM_ONLY and o not in _GATED)
    raise ConditionError(f"unknown agent '{agent}'. Use claude_code (codex is a stub for now).")


class ConditionError(ValueError):
    pass


@dataclass(frozen=True)
class Condition:
    name: str                              # stable label used in files and reports
    uses_gateway: bool
    on: tuple[str, ...] = ()               # optimizers set to "on"
    unsupported_reason: str | None = None  # set when it cannot be measured with this agent
    uses_hooks: bool = False               # `trove hooks install --rewrite` is run for the agent
    hooks_on: tuple[str, ...] = ()         # hook-side optimizers set to "on"

    @property
    def measurable(self) -> bool:
        return self.unsupported_reason is None

    def effective_states(self) -> dict[str, str]:
        """Expected pinned state of every optimizer; the runner asserts the gateway agrees."""
        return {o: ("on" if o in self.on else "off") for o in OPTIMIZERS}

    def hook_states(self) -> dict[str, str]:
        """Expected state of every hook-side optimizer, for the runner to assert."""
        names = HOOK_OPTIMIZERS + tuple(o for o in self.hooks_on if o not in HOOK_OPTIMIZERS)
        return {o: ("on" if o in self.hooks_on else "off") for o in names}


def parse_condition(spec: str, agent: str) -> Condition:
    supported = supported_optimizers(agent)
    if spec == "direct":
        return Condition("direct", uses_gateway=False)
    if spec == "passthrough":
        return Condition("passthrough", uses_gateway=True)
    if spec == "all_safe":
        return Condition("all_safe", uses_gateway=True, on=supported)
    if spec == "hooks":
        # Hooks alone: no gateway, so the agent talks to the provider directly.
        return Condition("hooks", uses_gateway=False, uses_hooks=True, hooks_on=HOOKS_ON)
    if spec == "hooks_gateway":
        return Condition("hooks_gateway", uses_gateway=True, on=supported, uses_hooks=True, hooks_on=HOOKS_ON)
    if spec.startswith("alone:"):
        name = spec.split(":", 1)[1]
        if name not in OPTIMIZERS:
            raise ConditionError(f"'{spec}': unknown optimizer '{name}'. Known: {', '.join(OPTIMIZERS)}.")
        if name in _GATED:
            why = f"{name} is gated to shadow mode until the MCP fetch tool exists, so it cannot be switched on"
        elif name in supported:
            return Condition(spec.replace(":", "_"), uses_gateway=True, on=(name,))
        else:
            why = f"{name} only acts for custom agents, and {agent} is never a custom agent"
        return Condition(spec.replace(":", "_"), uses_gateway=True, unsupported_reason=why)
    raise ConditionError(
        f"unknown condition '{spec}'. Use direct, passthrough, all_safe, hooks, hooks_gateway or alone:<optimizer>."
    )


def parse_conditions(specs: list[str], agent: str) -> list[Condition]:
    out = [parse_condition(s, agent) for s in specs]
    names = [c.name for c in out]
    if len(set(names)) != len(names):
        raise ConditionError("a condition is listed twice; each condition may appear once.")
    return out


def render_config_toml(cond: Condition, *, retention_days: int = 3650) -> str:
    """The data-dir config.toml for a gateway condition.

    The baseline gate and interleaving are switched off so that "on" really means
    on from the first request. Unlisted optimizers are off, but we list them all
    explicitly so the file documents the condition.
    """
    if not cond.uses_gateway and not cond.uses_hooks:
        raise ConditionError(f"condition '{cond.name}' has no gateway and no hooks, so it has no config.toml.")
    if not cond.measurable:
        raise ConditionError(f"condition '{cond.name}' cannot be run: {cond.unsupported_reason}.")
    lines = [
        f"# Generated by trovebench for condition '{cond.name}'. Do not edit.",
        f"retention_days = {retention_days}",
        "",
        "[optimizers]",
    ]
    states = dict(cond.effective_states())
    if cond.uses_hooks:
        states.update(cond.hook_states())
    for name, state in states.items():
        lines.append(f'{name} = "{state}"')
    lines += [
        "",
        "[measure]",
        "baseline_days = 0",
        "baseline_sessions = 0",
        "interleave = false",
        "",
    ]
    return "\n".join(lines)
