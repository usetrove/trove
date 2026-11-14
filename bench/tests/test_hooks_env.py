import json

import pytest

from trovebench.conditions import parse_condition
from trovebench.hooks_env import HooksSetupError, capabilities_for, check_snapshot, install_command


def _report(tmp_path, version="2.1.318", method="live"):
    p = tmp_path / "probe.json"
    p.write_text(json.dumps({"capabilities": {"v": 1, "entries": [
        {"agent": "claude", "agent_version": version, "tool": "Bash", "method": method,
         "input_rewrite": "yes", "output_rewrite": "yes"},
        {"agent": "codex", "agent_version": "", "tool": "Bash", "method": "docs",
         "input_rewrite": "yes", "output_rewrite": "no"},
    ]}}), encoding="utf-8")
    return p


def test_capabilities_are_copied_only_for_the_probed_version(tmp_path):
    out = json.loads(capabilities_for(_report(tmp_path), "2.1.318"))
    assert out["v"] == 1 and [e["tool"] for e in out["entries"]] == ["Bash"]
    assert all(e["method"] == "live" and e["agent"] == "claude" for e in out["entries"])


def test_a_different_agent_version_is_refused_with_both_versions_named(tmp_path):
    with pytest.raises(HooksSetupError, match=r"2\.1\.400.*2\.1\.318"):
        capabilities_for(_report(tmp_path), "2.1.400")


def test_docs_only_or_missing_probe_is_refused(tmp_path):
    with pytest.raises(HooksSetupError, match="live"):
        capabilities_for(_report(tmp_path, method="docs"), "2.1.318")
    with pytest.raises(HooksSetupError, match="hookprobe"):
        capabilities_for(tmp_path / "nope.json", "2.1.318")
    with pytest.raises(HooksSetupError, match="hookprobe"):
        capabilities_for(None, "2.1.318")


def test_snapshot_must_match_the_condition():
    cond = parse_condition("hooks", "claude_code")
    good = {"states": {"hook_repeat_read": "on", "hook_read_limit": "on", "hook_quiet_commands": "on",
                       "noise_normalizer": "on", "hook_large_output": "off"}}
    check_snapshot(json.dumps(good), cond)
    bad = {"states": {**good["states"], "hook_quiet_commands": "shadow"}}
    with pytest.raises(HooksSetupError, match="hook_quiet_commands"):
        check_snapshot(json.dumps(bad), cond)
    with pytest.raises(HooksSetupError, match="missing or unreadable"):
        check_snapshot("", cond)


def test_install_command_uses_the_data_dir_and_syncs():
    cmd = install_command("/trove-data")
    assert cmd.count("TROVE_DATA_DIR=/trove-data") == 2
    assert "--rewrite" in cmd and cmd.endswith("trove hooks sync")


@pytest.mark.parametrize("hook", ["hook_repeat_read", "hook_quiet_commands"])
def test_a_single_hook_condition_is_checked_against_the_snapshot(hook):
    """The harness refuses a run whose hooks did not pick up the condition: only this hook on, the rest off."""
    cond = parse_condition(f"alone_{hook}", "claude_code")
    others = {h: "off" for h in ("hook_repeat_read", "hook_read_limit", "hook_quiet_commands", "hook_large_output")}
    good = {"states": {**others, hook: "on", "noise_normalizer": "off"}}
    check_snapshot(json.dumps(good), cond)
    # Another hook left on would contaminate the comparison, so it must be caught.
    other = next(h for h in others if h != hook)
    with pytest.raises(HooksSetupError, match=other):
        check_snapshot(json.dumps({"states": {**good["states"], other: "on"}}), cond)
    # This hook not picked up means the run would be a second "direct", which must also be caught.
    with pytest.raises(HooksSetupError, match=hook):
        check_snapshot(json.dumps({"states": {**good["states"], hook: "shadow"}}), cond)
