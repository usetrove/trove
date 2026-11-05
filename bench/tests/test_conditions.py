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
