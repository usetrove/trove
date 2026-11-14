import tomllib
from pathlib import Path

import pytest

from trovebench.conditions import (
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


@pytest.mark.parametrize("name", ["volatile_prefix_mover", "cache_breakpoints", "output_trimmer"])
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
    assert set(parsed["optimizers"]) == set(OPTIMIZERS)
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


def test_hook_large_output_cannot_be_measured_alone_and_says_why():
    c = parse_condition("alone:hook_large_output", "claude_code")
    assert not c.measurable and "MCP" in c.unsupported_reason
    with _pytest.raises(ConditionError, match="cannot be run"):
        render_config_toml(c)


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
