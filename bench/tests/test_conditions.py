import tomllib
from pathlib import Path

import pytest

from trovebench.conditions import (
    ALL_GATEWAY,
    OPTIMIZERS,
    ConditionError,
    parse_condition,
    parse_conditions,
    render_config_toml,
)

GOLDEN = Path(__file__).parent / "golden"


def test_direct_has_no_gateway_or_config():
    c = parse_condition("direct", "claude_code")
    assert not c.uses_gateway
    with pytest.raises(ConditionError, match="no gateway"):
        render_config_toml(c)


def test_passthrough_all_off():
    c = parse_condition("passthrough", "claude_code")
    assert c.uses_gateway and c.on == ()
    assert set(c.effective_states().values()) == {"off"}


def test_all_safe_only_includes_supported_for_claude_code():
    c = parse_condition("all_safe", "claude_code")
    assert c.on == ("noise_normalizer", "duplicate_result_collapser")


@pytest.mark.parametrize("name", ["volatile_prefix_mover", "cache_breakpoints"])
def test_unsupported_alone_is_marked_not_errored(name):
    c = parse_condition(f"alone:{name}", "claude_code")
    assert not c.measurable
    assert c.unsupported_reason
    with pytest.raises(ConditionError, match="cannot be run"):
        render_config_toml(c)


def test_unknown_specs():
    with pytest.raises(ConditionError, match="unknown optimizer"):
        parse_condition("alone:bogus", "claude_code")
    with pytest.raises(ConditionError, match="unknown condition"):
        parse_condition("fast", "claude_code")
    with pytest.raises(ConditionError, match="unknown agent"):
        parse_condition("direct", "vim")


def test_duplicate_condition_rejected():
    with pytest.raises(ConditionError, match="twice"):
        parse_conditions(["direct", "direct"], "claude_code")


@pytest.mark.parametrize("spec", ["passthrough", "alone:noise_normalizer", "all_safe"])
def test_rendered_toml_parses_and_matches_golden(spec):
    c = parse_condition(spec, "claude_code")
    text = render_config_toml(c)
    parsed = tomllib.loads(text)
    assert parsed["measure"] == {"baseline_days": 0, "baseline_sessions": 0, "interleave": False}
    # The experiment assigner must be off, or sessions are sent to the "off" arm and "on" becomes shadow.
    assert parsed["experiment"] == {"enabled": False}
    assert set(parsed["optimizers"]) == set(ALL_GATEWAY)
    golden = GOLDEN / f"config_{c.name}.toml"
    assert text == golden.read_text(encoding="utf-8"), f"update {golden} if the change is intended"


def test_states_never_include_trimmer_on():
    for spec in ("alone:noise_normalizer", "all_safe", "passthrough"):
        assert parse_condition(spec, "claude_code").effective_states()["output_trimmer"] == "off"


def test_hooks_alone_has_no_gateway_but_has_hook_states():
    c = parse_condition("hooks", "claude_code")
    assert not c.uses_gateway and c.uses_hooks
    assert set(c.effective_states().values()) == {"off"}  # no request-level optimizer
    on = {k for k, v in c.hook_states().items() if v == "on"}
    assert on == {"hook_repeat_read", "hook_read_limit", "hook_quiet_commands", "noise_normalizer"}
    cfg = tomllib.loads(render_config_toml(c))
    assert cfg["optimizers"]["hook_quiet_commands"] == "on"
    assert cfg["optimizers"]["duplicate_result_collapser"] == "off"


def test_hooks_gateway_runs_both_and_existing_conditions_are_unchanged():
    c = parse_condition("hooks_gateway", "claude_code")
    assert c.uses_gateway and c.uses_hooks and c.on == ("noise_normalizer", "duplicate_result_collapser")
    cfg = tomllib.loads(render_config_toml(c))
    assert cfg["optimizers"]["hook_read_limit"] == "on" and cfg["optimizers"]["duplicate_result_collapser"] == "on"
    # A gateway-only condition must not mention hook optimizers at all.
    plain = tomllib.loads(render_config_toml(parse_condition("all_safe", "claude_code")))
    assert not any(k.startswith("hook_") for k in plain["optimizers"])


# ---- one hook by itself (gate G8 in docs/autopilot-eligibility.md) -----------------------------------------------

import pytest as _pytest  # noqa: E402

from trovebench.conditions import HOOK_ALONE, HOOK_OPTIMIZERS  # noqa: E402


@_pytest.mark.parametrize("hook", HOOK_ALONE)
@_pytest.mark.parametrize("spelling", ["alone:{}", "alone_{}"])
def test_a_single_hook_is_on_by_itself_with_no_gateway(hook, spelling):
    c = parse_condition(spelling.format(hook), "claude_code")
    assert c.name == f"alone_{hook}" and c.measurable
    assert c.uses_hooks and not c.uses_gateway
    assert c.hooks_on == (hook,)
    on = {k for k, v in c.hook_states().items() if v == "on"}
    assert on == {hook}, "every other hook, and every request-level optimizer, must stay off"
    assert set(c.effective_states().values()) == {"off"}
    cfg = tomllib.loads(render_config_toml(c))
    assert cfg["optimizers"][hook] == "on"
    assert [k for k, v in cfg["optimizers"].items() if v == "on"] == [hook]
    assert cfg["experiment"] == {"enabled": False}  # the condition must hold exactly its listed state


@_pytest.mark.parametrize("hook", HOOK_ALONE)
def test_single_hook_config_matches_golden(hook):
    text = render_config_toml(parse_condition(f"alone_{hook}", "claude_code"))
    golden = GOLDEN / f"config_alone_{hook}.toml"
    assert text == golden.read_text(encoding="utf-8"), f"update {golden} if the change is intended"


@_pytest.mark.parametrize("hook", ["hook_large_output", "hook_output_dedup", "hook_read_outline", "hook_output_preview"])
def test_hooks_that_need_the_mcp_server_register_it(hook):
    # plan item 10.6: without the MCP server these are clamped to shadow (or hint at a tool that does not exist)
    c = parse_condition(f"alone:{hook}", "claude_code")
    assert c.measurable and c.uses_mcp and c.hooks_on == (hook,)
    assert not parse_condition("alone:hook_repeat_read", "claude_code").uses_mcp


def test_output_trimmer_alone_registers_the_mcp_server_but_stays_out_of_all_safe():
    c = parse_condition("alone:output_trimmer", "claude_code")
    assert c.measurable and c.uses_mcp and c.on == ("output_trimmer",)
    assert "output_trimmer" not in parse_condition("all_safe", "claude_code").on


def test_all_safe_never_includes_behaviour_or_routing():
    on = parse_condition("all_safe", "claude_code").on
    assert "verbosity_steer" not in on and "model_router" not in on
    states = parse_condition("all_safe", "claude_code").effective_states()
    assert states["verbosity_steer"] == "off" and states["model_router"] == "off"   # pinned explicitly


def test_passthrough_plugin_is_the_whole_plugin_behind_a_recording_gateway():
    c = parse_condition("passthrough_plugin", "claude_code")
    assert c.uses_gateway and c.recording_gateway and c.uses_hooks and c.uses_mcp and c.on == ()
    assert set(c.effective_states().values()) == {"off"}       # the gateway itself changes nothing
    assert {"hook_output_dedup", "hook_read_outline", "output_trimmer", "noise_normalizer"} <= set(c.hooks_on)


def test_trove_full_turns_on_everything_and_names_the_route():
    c = parse_condition("trove_full:claude-haiku-5-5", "claude_code")
    assert c.name == "trove_full" and c.route_to == "claude-haiku-5-5" and c.uses_mcp and not c.recording_gateway
    st = c.effective_states()
    for o in ("noise_normalizer", "duplicate_result_collapser", "verbosity_steer", "model_router", "output_trimmer"):
        assert st[o] == "on"
    assert st["volatile_prefix_mover"] == "off" and st["cache_breakpoints"] == "off"   # custom agents only
    assert st["effort_tool_turns"] == "off"      # measured by itself (alone:effort_tool_turns)
    cfg = tomllib.loads(render_config_toml(c))
    assert cfg["optimize"] == {"route_to": "claude-haiku-5-5"}
    for bad in ("trove_full", "trove_full:Not A Model", "alone:model_router"):
        with _pytest.raises(ConditionError):
            parse_condition(bad, "claude_code")
    r = parse_condition("alone:model_router:claude-haiku-5-5", "claude_code")
    assert r.name == "alone_model_router" and r.on == ("model_router",) and r.route_to == "claude-haiku-5-5"


def test_recording_gateway_config_turns_every_gateway_optimizer_off():
    from trovebench.conditions import recording_config_toml
    cfg = tomllib.loads(recording_config_toml(parse_condition("passthrough_plugin", "claude_code")))
    assert set(cfg["optimizers"].values()) == {"off"} and "optimize" not in cfg


def test_every_hook_is_either_measurable_alone_or_explained():
    for hook in HOOK_OPTIMIZERS:
        c = parse_condition(f"alone:{hook}", "claude_code")
        assert c.measurable == (hook in HOOK_ALONE)
        assert c.measurable or c.unsupported_reason


def test_the_combined_hooks_condition_is_unchanged():
    c = parse_condition("hooks", "claude_code")
    assert set(c.hooks_on) == {"hook_repeat_read", "hook_read_limit", "hook_quiet_commands", "noise_normalizer"}
    assert not c.uses_gateway


def test_single_hooks_can_sit_in_one_study_with_the_other_conditions():
    names = [c.name for c in parse_conditions(
        ["direct", "alone_hook_repeat_read", "alone_hook_quiet_commands", "hooks", "all_safe"], "claude_code")]
    assert len(set(names)) == 5


# ---- Trove features installed in the repo (plan item 7.1) -------------------------------------------------------

def test_feature_conditions_install_then_check_and_record_through_the_gateway():
    from trovebench.conditions import feature_prompt, feature_setup
    s = parse_condition("task_scout", "claude_code")
    assert s.uses_gateway and s.on == () and not s.uses_hooks and s.feature == "scout"
    assert feature_setup(s, "x") == ["trove tasks apply scout --yes --project .", "test -f .claude/agents/trove-scout.md"]
    assert feature_prompt(s, "fix it") == "fix it"
    c = parse_condition("task_cascade", "claude_code")
    cmds = feature_setup(c, "cd a && python -m pytest -q")
    assert "--test-cmd 'cd a && python -m pytest -q'" in cmds[0] and cmds[1].startswith("test -f")
    assert feature_prompt(c, "fix it") == "/trove-cascade fix it"
    p = parse_condition("profile:quick-fix", "claude_code")
    assert p.name == "profile_quick_fix" and parse_condition("profile_quick_fix", "claude_code").feature == "profile:quick-fix"
    assert "--allow-unproven" in feature_setup(p, "x")[0]
    with pytest.raises(ConditionError, match="profile"):
        parse_condition("profile:feature", "claude_code")     # changes nothing for Claude Code
    assert set(s.effective_states().values()) == {"off"}


def test_default_model_runs_the_whole_session_on_the_cheaper_model():
    c = parse_condition("default_model", "claude_code", route_to="claude-haiku-5-5")
    assert c.model == "claude-haiku-5-5" and c.uses_gateway and c.on == () and not c.route_to
    with pytest.raises(ConditionError, match="route_to"):
        parse_condition("default_model", "claude_code")
