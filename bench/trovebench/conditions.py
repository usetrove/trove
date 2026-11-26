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

import re
import shlex
from dataclasses import dataclass

OPTIMIZERS = (
    "volatile_prefix_mover",
    "noise_normalizer",
    "output_trimmer",
    "duplicate_result_collapser",
    "cache_breakpoints",
)

# Gateway optimizers outside the safe (T1) set. verbosity_steer is T2 (it asks for shorter replies) and
# model_router is T3 (it sends tool turns to a cheaper model), so `all_safe` never includes them; they are on
# only in conditions that name them. Listing them here makes every condition pin them explicitly.
BEHAVIOR_OPTIMIZERS = ("verbosity_steer", "model_router", "effort_tool_turns", "effort_routine_prompts", "thinking_clear")
ALL_GATEWAY = OPTIMIZERS + BEHAVIOR_OPTIMIZERS
# Settings profiles that change something for Claude Code (`trove settings plan --task X --allow-unproven`, checked
# 2026-11-18 on Claude Code 2.1.327): quick-fix and exploration both set outputStyle = Concise in the project's
# .claude/settings.json. feature and refactor change nothing for Claude Code, so a condition for them would
# measure nothing.
PROFILE_TASKS = ("quick-fix", "exploration")
MODEL_RE = re.compile(r"^[a-z0-9][a-z0-9.-]{2,80}$")

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
    "hook_output_dedup",
    "hook_read_outline",
    "hook_output_preview",
)
HOOKS_ON = ("hook_repeat_read", "hook_read_limit", "hook_quiet_commands", "noise_normalizer")

# The whole plugin, as a user who installs it gets it: the hooks plus Trove's MCP server. With the MCP server
# registered, trimmed output can be fetched back (trove_fetch_full), so output_trimmer, hook_output_dedup and
# hook_read_outline may really be on instead of being clamped to shadow (internal/optimize/pin.go), and
# hook_large_output's hint points at a trove_run tool that exists. The MCP tool definitions are sent with every
# request, so their tokens are paid in these conditions, as a real user pays them (PRINCIPLES.md rule 15).
PLUGIN_ON = HOOKS_ON + ("hook_output_dedup", "hook_read_outline", "hook_large_output", "output_trimmer")

# Hook-side optimizers that can be switched on by themselves (`alone:<hook>`), so the benefit or harm of each
# is attributable to it: the `hooks` condition turns four on together and cannot say which one did what.
# hook_large_output advises running a command through trove_run, a tool that exists only when Trove's MCP
# server is registered, which a benchmark run does not do, so measuring it would measure a hint that points
# at nothing.
# Hooks that need Trove's MCP server: hook_large_output's hint names trove_run, and hook_output_dedup and
# hook_read_outline shorten output only when it can be fetched back with trove_fetch_full (otherwise they are
# clamped to shadow). Their solo conditions register the MCP server, as the plugin does (plan item 10.6).
# hook_output_preview (B30, added 2026-11-25) shortens long tool output to a preview the agent can expand with
# trove_fetch_full, so it needs the server too.
MCP_HOOKS = ("hook_large_output", "hook_output_dedup", "hook_read_outline", "hook_output_preview")
HOOK_ALONE = ("hook_repeat_read", "hook_read_limit", "hook_quiet_commands") + MCP_HOOKS
_HOOK_UNMEASURABLE: dict[str, str] = {}

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
    uses_mcp: bool = False                 # `trove mcp install` registers Trove's MCP server with the agent
    route_to: str = ""                     # model_router's cheaper model (optimize.route_to); "" leaves it idle
    # The gateway only records: it gets its own data dir with every optimizer off, so hook-side settings that
    # share a name with a gateway optimizer (noise_normalizer, output_trimmer) act in the hooks alone.
    recording_gateway: bool = False
    # A Trove feature installed in the task's repo before the agent runs (plan item 7.1): "scout", "cascade" or
    # "profile:<task type>". Each runs behind a gateway that only records, so it is priced like every condition.
    feature: str = ""
    # The model the agent is started with in this condition, when it differs from the study's model: the
    # "cheaper default model" a model_default fix would set (plan item 7.5). "" means the study's model.
    model: str = ""

    @property
    def measurable(self) -> bool:
        return self.unsupported_reason is None

    def effective_states(self) -> dict[str, str]:
        """Expected pinned state of every optimizer; the runner asserts the gateway agrees."""
        # Hooks and gateway read one config unless the gateway only records, so a hook-side "on" for a name the
        # gateway also runs (noise_normalizer, output_trimmer) is on in the gateway too.
        shared = self.hooks_on if self.uses_gateway and not self.recording_gateway else ()
        return {o: ("on" if o in self.on or o in shared else "off") for o in ALL_GATEWAY}

    def hook_states(self) -> dict[str, str]:
        """Expected state of every hook-side optimizer, for the runner to assert."""
        names = HOOK_OPTIMIZERS + tuple(o for o in self.hooks_on if o not in HOOK_OPTIMIZERS)
        return {o: ("on" if o in self.hooks_on else "off") for o in names}


def parse_condition(spec: str, agent: str, route_to: str = "") -> Condition:
    supported = supported_optimizers(agent)
    if spec.startswith("alone_"):      # the name a condition carries in records and study files
        spec = "alone:" + spec[len("alone_"):]
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
    if spec == "passthrough_plugin":
        # The whole plugin (hooks + MCP server), with a gateway that only records cost: the like-for-like way to
        # price a plugin-only run when the study prices every condition from the gateway's records.
        return Condition("passthrough_plugin", uses_gateway=True, uses_hooks=True, hooks_on=PLUGIN_ON, uses_mcp=True,
                         recording_gateway=True)
    if spec == "default_model":
        # The whole session on the study's cheaper model (route_to), as if the default model were changed: the
        # comparison that gives `trove optimize suggest` its model_default prior.
        if not route_to:
            raise ConditionError("default_model needs the cheaper model: the study names it in route_to.")
        return Condition("default_model", uses_gateway=True, model=route_to)
    if spec in ("task_scout", "task_cascade"):
        # Trove's scout subagent, or its test-gated cascade (cheap worker first, strong model on failing tests).
        return Condition(spec, uses_gateway=True, feature=spec.removeprefix("task_"))
    if spec.startswith("profile_") or spec.startswith("profile:"):
        # Records spell the condition profile_quick_fix; the profile itself is quick-fix.
        task = spec.split(":", 1)[1] if ":" in spec else spec.removeprefix("profile_").replace("_", "-")
        if task not in PROFILE_TASKS:
            raise ConditionError(f"'{spec}': unknown settings profile '{task}'. Use one of {', '.join(PROFILE_TASKS)}.")
        return Condition(f"profile_{task.replace('-', '_')}", uses_gateway=True, feature=f"profile:{task}")
    if spec.startswith("trove_full:"):
        # Everything Trove offers an API-key user of this agent: the whole plugin, every safe gateway optimizer,
        # verbosity_steer (T2) and model_router (T3) stepping tool turns down to the named model.
        model = spec.split(":", 1)[1]
        if not MODEL_RE.match(model):
            raise ConditionError(f"'{spec}': '{model}' is not a model ID. Write trove_full:<model>, e.g. trove_full:claude-haiku-5-5.")
        # effort_tool_turns is left out: on tool turns already routed to the cheaper model it saves little, and it
        # has its own condition (alone:effort_tool_turns) so its effect on success is measured by itself.
        return Condition("trove_full", uses_gateway=True, on=supported + ("verbosity_steer", "model_router"), uses_hooks=True,
                         hooks_on=PLUGIN_ON, uses_mcp=True, route_to=model)
    if spec == "trove_full" and route_to:
        return parse_condition(f"trove_full:{route_to}", agent)
    if spec == "trove_full":
        raise ConditionError("trove_full needs the cheaper model for model_router: write trove_full:<model>, e.g. trove_full:claude-haiku-5-5.")
    if spec.startswith("alone:model_router:") or spec.startswith("alone_model_router:"):
        model = spec.split(":", 2)[-1] if spec.startswith("alone:") else spec.split(":", 1)[1]
        if not MODEL_RE.match(model):
            raise ConditionError(f"'{spec}': '{model}' is not a model ID.")
        return Condition("alone_model_router", uses_gateway=True, on=("model_router",), route_to=model)
    if spec in ("alone:model_router", "alone_model_router") and route_to:
        return parse_condition(f"alone:model_router:{route_to}", agent)
    if spec in ("alone:model_router", "alone_model_router"):
        raise ConditionError("alone:model_router needs the cheaper model: write alone:model_router:<model>.")
    if spec in ("alone:verbosity_steer", "alone_verbosity_steer"):
        return Condition("alone_verbosity_steer", uses_gateway=True, on=("verbosity_steer",))
    if spec in ("alone:effort_tool_turns", "alone_effort_tool_turns"):
        # Lowers the top-level effort on tool turns (plan item 7.4); acts only on models whose effort cache check said
        # keeps (Sonnet 5.5, Haiku 5.5). It stays on the top-level path: per-message effort (checked 2026-11-24 on
        # platform.claude.com/docs/en/build-with-claude/effort-per-message) cannot act on a tool turn, because a user
        # turn holding only tool results "doesn't count as new input", so the change would wait for the next prompt.
        # The per-prompt variant is its own condition, alone:effort_routine_prompts.
        return Condition("alone_effort_tool_turns", uses_gateway=True, on=("effort_tool_turns",))
    if spec in ("alone:effort_routine_prompts", "alone_effort_routine_prompts"):
        # Low effort, by per-message effort, for the turn after a routine prompt; models documented to support it.
        return Condition("alone_effort_routine_prompts", uses_gateway=True, on=("effort_routine_prompts",))
    if spec in ("alone:thinking_clear", "alone_thinking_clear"):
        # Clears old thinking in very long sessions (context editing); its shadow estimate decides promotion, and this
        # measures what it does to success.
        return Condition("alone_thinking_clear", uses_gateway=True, on=("thinking_clear",))
    if spec.startswith("alone:"):
        name = spec.split(":", 1)[1]
        if name in HOOK_OPTIMIZERS:
            # One hook by itself: no gateway (the agent talks to the provider directly, as in `hooks`), the
            # hooks installed, and only this one switched on. Everything else stays off.
            label = spec.replace(":", "_")
            if name in _HOOK_UNMEASURABLE:
                return Condition(label, uses_gateway=False, uses_hooks=True, unsupported_reason=_HOOK_UNMEASURABLE[name])
            return Condition(label, uses_gateway=False, uses_hooks=True, hooks_on=(name,), uses_mcp=name in MCP_HOOKS)
        if name not in OPTIMIZERS:
            raise ConditionError(f"'{spec}': unknown optimizer '{name}'. Known: {', '.join(OPTIMIZERS + HOOK_OPTIMIZERS)}.")
        if name in _GATED:
            # output_trimmer may be on only when trimmed output can be fetched back, so the MCP server is
            # registered for this condition (plan item 10.6). It stays out of all_safe, whose meaning is fixed.
            return Condition(spec.replace(":", "_"), uses_gateway=True, on=(name,), uses_mcp=True)
        if name in supported:
            return Condition(spec.replace(":", "_"), uses_gateway=True, on=(name,))
        else:
            why = f"{name} only acts for custom agents, and {agent} is never a custom agent"
        return Condition(spec.replace(":", "_"), uses_gateway=True, unsupported_reason=why)
    raise ConditionError(
        f"unknown condition '{spec}'. Use direct, passthrough, all_safe, hooks, hooks_gateway, passthrough_plugin, "
        f"trove_full:<model>, alone:model_router:<model>, alone:verbosity_steer or alone:<optimizer> "
        f"(a gateway optimizer, or one of {', '.join(HOOK_ALONE)})."
    )


def parse_conditions(specs: list[str], agent: str, route_to: str = "") -> list[Condition]:
    out = [parse_condition(s, agent, route_to) for s in specs]
    names = [c.name for c in out]
    if len(set(names)) != len(names):
        raise ConditionError("a condition is listed twice; each condition may appear once.")
    return out


def recording_config_toml(cond: Condition, *, retention_days: int = 3650) -> str:
    """config.toml for a recording-only gateway: every gateway optimizer off, nothing else set."""
    return render_config_toml(Condition(cond.name, uses_gateway=True), retention_days=retention_days)


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
    if cond.route_to:
        lines += ["", "[optimize]", f'route_to = "{cond.route_to}"']
    lines += [
        "",
        "[measure]",
        "baseline_days = 0",
        "baseline_sessions = 0",
        "interleave = false",
        "",
        # The experiment assigner would send half the sessions to the "off" arm, which the gate
        # turns into shadow. A condition must hold exactly its listed states, so it is switched off.
        "[experiment]",
        "enabled = false",
        "",
    ]
    return "\n".join(lines)


def feature_setup(cond: Condition, task_success_cmd: str) -> list[str]:
    """Shell commands, run in the task's repo inside the container, that install the condition's Trove feature.

    Every command is a documented `trove` command run with --yes, which writes only inside the repo and the
    container's own Claude Code settings; the container is thrown away after the run. The cascade gets the task's
    own test command, because it refuses to escalate on a guess. Settings profiles pass --allow-unproven because
    the benchmark is how a profile's values earn evidence.
    """
    f = cond.feature
    if not f:
        return []
    # Each install is followed by a check that its file exists. `trove` queues a change instead of applying it
    # when it sees a recently active session; a fresh container has none, and the check makes sure of it, so an
    # install that changed nothing fails the run instead of being measured as a no-op.
    if f == "scout":
        return ["trove tasks apply scout --yes --project .", "test -f .claude/agents/trove-scout.md"]
    if f == "cascade":
        return [f"trove tasks apply cascade --yes --project . --test-cmd {shlex.quote(task_success_cmd)}",
                "test -f .claude/commands/trove-cascade.md"]
    if f.startswith("profile:"):
        return [f"trove settings apply --task {shlex.quote(f.split(':', 1)[1])} --allow-unproven --yes --project .",
                "test -f .claude/settings.json"]
    raise ConditionError(f"unknown feature {f!r} in condition {cond.name!r}.")


def feature_prompt(cond: Condition, prompt: str) -> str:
    """The first prompt for the condition: the cascade is started with its slash command (checked live
    2026-11-18: a project command runs from `claude -p "/name args"`); every other condition gets the task as is."""
    return f"/trove-cascade {prompt}" if cond.feature == "cascade" else prompt
