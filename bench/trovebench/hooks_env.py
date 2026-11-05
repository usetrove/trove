"""Setting up Trove's agent hooks inside a benchmark container.

A hooks condition only measures something if the hooks really rewrite. Two things
keep them in shadow mode otherwise, and this module checks both before any money
is spent:

  * the optimizer states in the hooks' snapshot (hooks/config.json) must match
    the condition, and
  * the agent version in the container must be one a live capability probe has
    passed (`go run ./scripts/hookprobe --live`). Hooks treat an unprobed agent
    as unable to rewrite, so the probe result is copied in, but only when its
    version equals the container's.
"""

from __future__ import annotations

import json
from pathlib import Path

from .conditions import Condition


class HooksSetupError(RuntimeError):
    pass


def capabilities_for(report_path: Path | None, agent_version: str) -> str:
    """The hooks/capabilities.json text for a container running `agent_version`.

    `report_path` is the hookprobe JSON (docs/hook-capabilities.json). Raises
    HooksSetupError when there is no live result for exactly that version, because
    running a hooks condition on an unprobed agent would silently measure shadow
    mode.
    """
    if report_path is None or not Path(report_path).exists():
        raise HooksSetupError(
            "A hooks condition needs a live hook probe result for the agent version in the container. "
            "Run: go run ./scripts/hookprobe --live --max-usd 0.25  (then pass its JSON file)."
        )
    report = json.loads(Path(report_path).read_text(encoding="utf-8"))
    entries = [e for e in report.get("capabilities", {}).get("entries", [])
               if e.get("agent") == "claude"]
    live = [e for e in entries if e.get("method") == "live" and e.get("agent_version") == agent_version]
    if not live:
        probed = sorted({e.get("agent_version", "?") for e in entries if e.get("method") == "live"}) or ["none"]
        raise HooksSetupError(
            f"The container runs Claude Code {agent_version}, but the live hook probe result is for "
            f"{', '.join(probed)}. Rerun the probe on {agent_version}, or pin the benchmark to the probed version."
        )
    # Only this agent's live entries are carried over. Codex is docs-only and not used here.
    return json.dumps({"v": 1, "entries": live}, indent=2) + "\n"


def check_snapshot(snapshot_text: str, cond: Condition) -> None:
    """Fail loudly if the hooks' snapshot does not say what the condition needs."""
    try:
        states = json.loads(snapshot_text)["states"]
    except (json.JSONDecodeError, KeyError, TypeError) as e:
        raise HooksSetupError(
            "The hooks' snapshot (hooks/config.json) is missing or unreadable, so every hook would be off. "
            "`trove hooks sync` did not write it."
        ) from e
    want = {k: v for k, v in cond.hook_states().items() if k in states or v == "on"}
    bad = {k: (states.get(k), v) for k, v in want.items() if states.get(k) != v}
    if bad:
        raise HooksSetupError(
            f"Trove's hooks did not pick up condition '{cond.name}': {bad} (got, wanted). "
            "Check that config.toml was written before `trove hooks sync`."
        )


def install_command(data_dir: str) -> str:
    """Shell command that installs the rewriting hooks for Claude Code and refreshes the snapshot."""
    return (f"TROVE_DATA_DIR={data_dir} trove hooks install --yes --agent claude --rewrite && "
            f"TROVE_DATA_DIR={data_dir} trove hooks sync")
