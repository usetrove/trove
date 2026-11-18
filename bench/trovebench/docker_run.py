"""One run = one fresh container. See README for the flow diagram."""

from __future__ import annotations

import json
import os
import shutil
import tempfile
import threading
import time
from dataclasses import dataclass, field
from contextlib import contextmanager
from datetime import date
from pathlib import Path
from typing import Callable, Iterator

from . import docker_env as de
from .agent.base import Agent, AgentResult, combine_results
from .collect import build_record, read_gateway_db
from .conditions import Condition, feature_prompt, feature_setup, recording_config_toml, render_config_toml
from .hooks_env import HooksSetupError, capabilities_for, check_snapshot, install_command
from .manifest import Task
from .plan import RunSpec
from .pricing import PricingTable
from .records import RunRecord
from .redact import redact

KEY_ENV = "ANTHROPIC_API_KEY"
FAKE_KEY = "sk-ant-api03-benchfake-not-a-real-key-0000000000"
TROVE_DATA = "/trove-data"
# A recording-only gateway keeps its own data dir, so hook-side settings never reach it (Condition.recording_gateway).
GW_DATA = "/trove-gw"
GRADE_TIMEOUT_S = 900


class RunEnvError(RuntimeError):
    pass


def _session_spend(results: list[AgentResult], agent: Agent) -> float:
    """Estimated spend so far in a multi-prompt session, from the agent's own reports."""
    if getattr(agent, "resume_reports_session_total", True):
        last = next((r for r in reversed(results) if r.parsed and r.cost_usd_estimate is not None), None)
        return last.cost_usd_estimate if last else 0.0
    return sum(r.cost_usd_estimate or 0.0 for r in results if r.parsed)


def _say(msg: str) -> None:
    print(msg, flush=True)   # flush, or `| tee` shows nothing until the end


HEARTBEAT_S = 60


@contextmanager
def heartbeat(what: str, log: Callable[[str], None], every_s: float = HEARTBEAT_S) -> Iterator[None]:
    """Print "<what> ... still going (Nm Ss)" every `every_s` seconds while a long step runs."""
    stop = threading.Event()
    t0 = time.monotonic()

    def beat() -> None:
        while not stop.wait(every_s):
            m, sec = divmod(int(time.monotonic() - t0), 60)
            log(f"      {what} ... still going ({m}m{sec:02d}s)")

    th = threading.Thread(target=beat, daemon=True)
    th.start()
    try:
        yield
    finally:
        stop.set()
        th.join(timeout=1)


@dataclass
class ContainerRunner:
    agent: Agent
    model: str
    run_cap_usd: float
    run_timeout_s: int
    batch_dir: Path
    tasks: dict[str, Task]
    conditions: dict[str, Condition]
    table: PricingTable
    claude_version: str | None
    fake: bool = False
    images: dict[str, tuple[str, str]] = field(default_factory=dict)   # task id -> (tag, image id)
    trove_bin: Path | None = None
    fake_bin: Path | None = None
    arch: str = ""
    # JSON written by `go run ./scripts/hookprobe --live`; required by hooks conditions.
    hook_capabilities: Path | None = None
    log: Callable[[str], None] = _say

    # --- batch setup ----------------------------------------------------------------
    def prepare(self, task_ids: list[str]) -> None:
        self.arch = de.host_arch()
        if not self.arch:
            raise RunEnvError("Could not tell the Docker host CPU architecture (expected amd64 or arm64).")
        bin_dir = self.batch_dir / "bin"
        bin_dir.mkdir(parents=True, exist_ok=True)
        self.trove_bin = bin_dir / "trove"
        self.log("  building the Trove binary ...")
        de.go_build_linux("./cmd/trove", self.trove_bin, self.arch)
        if self.fake:
            self.fake_bin = bin_dir / "benchfake"
            de.go_build_linux("./bench/tools/benchfake", self.fake_bin, self.arch)
        for n, tid in enumerate(task_ids, 1):
            if tid not in self.images:
                self.log(f"  [{n}/{len(task_ids)}] task image for {tid} (first time downloads it) ...")
                with heartbeat("image", self.log):
                    self.images[tid] = de.build_task_image(self.tasks[tid], self.agent, self.claude_version)
        self.log(f"  {len(task_ids)} task image(s) ready.")

    def versions(self) -> dict:
        any_image = next(iter(self.images.values()))[0] if self.images else None
        reported = ""
        if any_image:
            p = de.docker("run", "--rm", any_image, "bash", "-lc", self.agent.version_command(), check=False, timeout=120)
            reported = (p.stdout or p.stderr).strip()
        return {
            "agent": self.agent.name,
            "agent_version_pinned": self.claude_version,
            "agent_version_reported": reported,
            "model": self.model,
            "trove_commit": de.git("rev-parse", "HEAD"),
            "trove_dirty": bool(de.git("status", "--porcelain")),
            "trove_binary_sha256": de.sha256_file(self.trove_bin) if self.trove_bin else "",
            "pricing_data_version": self.table.data_version,
            "dataset_commits": sorted({t.dataset_commit for t in self.tasks.values() if t.dataset_commit}),
            "image_ids": {k: v[1] for k, v in sorted(self.images.items())},
            "docker_arch": self.arch,
            "fake_agent": self.fake,
        }

    # --- one run --------------------------------------------------------------------
    def run_one(self, spec: RunSpec) -> RunRecord:
        task, cond = self.tasks[spec.task_id], self.conditions[spec.condition]
        tag, _ = self.images[spec.task_id]
        key = FAKE_KEY if self.fake else os.environ.get(KEY_ENV, "")
        if not key:
            raise RunEnvError(f"{KEY_ENV} is not set. Export your own API key in this shell; it is passed to the container by name and never written anywhere.")
        env_backup = os.environ.get(KEY_ENV)
        os.environ[KEY_ENV] = key       # docker reads the value from our environment
        run_dir = self.batch_dir / "runs" / spec.run_id
        shutil.rmtree(run_dir, ignore_errors=True)   # a retry of an errored run starts clean
        run_dir.mkdir(parents=True)
        secrets = (key,)
        try:
            with de.Container(tag, spec.run_id, secret_env=(KEY_ENV,), secrets=secrets) as c, \
                    tempfile.TemporaryDirectory() as td:
                return self._run_in(c, Path(td), run_dir, spec, task, cond, secrets)
        finally:
            if env_backup is None:
                os.environ.pop(KEY_ENV, None)
            else:
                os.environ[KEY_ENV] = env_backup

    def _run_in(self, c: de.Container, td: Path, run_dir: Path, spec: RunSpec, task: Task, cond: Condition,
                secrets: tuple[str, ...]) -> RunRecord:
        self.log("      container up; setting up (gateway/hooks if the condition uses them) ...")
        c.exec("mkdir -p /bench", workdir="/")
        (td / "prompt.txt").write_text(feature_prompt(cond, task.prompt), encoding="utf-8", newline="\n")
        c.put(td / "prompt.txt", "/bench/prompt.txt")
        if task.repo.startswith("local:"):
            sol = Path(task.base_dir) / "solution.sh"
            if self.fake and sol.exists():
                c.put(sol, "/bench/solution.sh")
        agent_env = dict(self.agent.static_env())
        mock_addr = ""
        if self.fake:
            c.put(self.fake_bin, "/usr/local/bin/benchfake")
            c.exec("chmod +x /usr/local/bin/benchfake", workdir="/")
            r = c.exec("(nohup benchfake mock -addr-file /tmp/mock.addr >/tmp/mock.log 2>&1 &); "
                       "for i in $(seq 1 100); do [ -s /tmp/mock.addr ] && break; sleep 0.1; done; cat /tmp/mock.addr",
                       workdir="/")
            mock_addr = r.out.strip()
            if not mock_addr:
                raise RunEnvError("the mock upstream did not start")

        if cond.uses_mcp and not self.fake:
            # Before the gateway starts: it opens the recovery store only when the MCP server is registered.
            self._setup_mcp(c, td, cond)

        gw_addr = ""
        gw_dir = GW_DATA if cond.recording_gateway else TROVE_DATA
        if cond.uses_gateway:
            gw_cfg = recording_config_toml(cond) if cond.recording_gateway else render_config_toml(cond)
            (td / "gateway.toml").write_text(gw_cfg, encoding="utf-8", newline="\n")
            c.put(self.trove_bin, "/usr/local/bin/trove")
            c.exec(f"chmod +x /usr/local/bin/trove && mkdir -p {gw_dir}", workdir="/")
            c.put(td / "gateway.toml", f"{gw_dir}/config.toml")
            up = f"TROVE_UPSTREAMS_ANTHROPIC=http://{mock_addr} " if self.fake else ""
            r = c.exec(
                f"(nohup env TROVE_DATA_DIR={gw_dir} {up}trove gateway start --listen 127.0.0.1:0 "
                f">/tmp/gateway.log 2>&1 &); for i in $(seq 1 150); do [ -s {gw_dir}/gateway.json ] && break; sleep 0.1; done; "
                f"cat {gw_dir}/gateway.json", workdir="/")
            try:
                gw_addr = json.loads(r.out.strip())["addr"]
            except (json.JSONDecodeError, KeyError) as e:
                log = c.exec("tail -c 600 /tmp/gateway.log", workdir="/").out
                raise RunEnvError(f"the Trove gateway did not start: {log}") from e
            h = c.exec(f"curl -fsS http://{gw_addr}/trove/health", workdir="/")
            if h.rc != 0:
                raise RunEnvError("the Trove gateway started but its health check failed")
            self._assert_configured(c, cond, gw_dir)
            agent_env.update(self.agent.base_url_env(f"http://{gw_addr}/anthropic"))
        elif self.fake:
            agent_env.update(self.agent.base_url_env(f"http://{mock_addr}"))

        if cond.uses_hooks:
            self._setup_hooks(c, td, cond)
        if cond.feature and not self.fake:
            # The feature's files must exist before the agent starts (rule 5: features change only between sessions).
            for cmd in feature_setup(cond, task.success_cmd):
                r = c.exec(f"TROVE_DATA_DIR={TROVE_DATA} {cmd}")
                if r.rc != 0:
                    raise RunEnvError(f"installing {cond.feature} failed in the container: {(r.out + r.err)[-300:]}")

        timeout = min(task.timeout_s, self.run_timeout_s)
        prompts = ["/bench/prompt.txt"]
        for i, text in enumerate(task.followups, start=1):
            (td / f"followup_{i}.txt").write_text(text, encoding="utf-8", newline="\n")
            c.put(td / f"followup_{i}.txt", f"/bench/followup_{i}.txt")
            prompts.append(f"/bench/followup_{i}.txt")
        t0 = time.monotonic()
        self.log(f"      agent working ({len(prompts)} prompt(s), limit {timeout // 60} min, ${self.run_cap_usd:.2f}) ...")
        results, outs, errs = [], [], []
        res = None
        session = ""
        with heartbeat("agent", self.log):
            for i, path in enumerate(prompts):
                left_s = timeout - (time.monotonic() - t0)
                spent = _session_spend(results, self.agent)
                left_usd = self.run_cap_usd - spent
                if i > 0 and (left_s < 30 or left_usd <= 0.01 or not session):
                    why = "time" if left_s < 30 else ("budget" if left_usd <= 0.01 else "no session id to resume")
                    self.log(f"      stopping before follow-up {i}: out of {why}")
                    break
                res = c.exec(self.agent.command(model=cond.model or self.model, max_budget_usd=round(left_usd, 4), prompt_path=path,
                                                   effort=spec.effort, bare=not (cond.uses_hooks or cond.uses_mcp or cond.feature), resume=session,
                                                   persist=len(prompts) > 1),
                             timeout=max(1, int(left_s)), env=agent_env)
                outs.append(res.out)
                errs.append(res.err)
                results.append(self.agent.parse(res.out))
                session = results[-1].session_id or session
                if res.timed_out or res.rc != 0 or results[-1].is_error:
                    break      # a failed prompt ends the session; grading still runs on what was done
        wall = time.monotonic() - t0
        sep = "\n----- next prompt -----\n"
        (run_dir / "agent_stdout.txt").write_text(redact(sep.join(outs), secrets), encoding="utf-8", newline="\n")
        (run_dir / "agent_stderr_tail.txt").write_text(redact(sep.join(errs)[-4000:], secrets), encoding="utf-8", newline="\n")

        self.log(f"      agent finished in {wall:.0f}s; grading ...")
        with heartbeat("grading", self.log):
            grade = c.exec(" && ".join([*task.grading_setup, task.success_cmd]) if task.grading_setup else task.success_cmd,
                           timeout=GRADE_TIMEOUT_S)
        (run_dir / "grading_tail.txt").write_text(redact((grade.out + grade.err)[-4000:], secrets), encoding="utf-8", newline="\n")

        stats = None
        if cond.uses_gateway:
            c.exec(f"pid=$(sed -n 's/.*\"pid\":\\([0-9]*\\).*/\\1/p' {gw_dir}/gateway.json); "
                   f"kill -TERM $pid 2>/dev/null; for i in $(seq 1 150); do [ -e {gw_dir}/gateway.json ] || break; sleep 0.1; done",
                   workdir="/")
            c.get(gw_dir, run_dir / "trove-data")
            if cond.recording_gateway and cond.uses_hooks:
                c.get(TROVE_DATA, run_dir / "trove-hooks-data")
            (run_dir / "gateway.log").write_text(redact(c.exec("tail -c 4000 /tmp/gateway.log", workdir="/").out, secrets),
                                                 encoding="utf-8", newline="\n")
            stats = read_gateway_db(run_dir / "trove-data" / "trove.db")

        if cond.uses_hooks and not cond.uses_gateway:
            # No gateway database here: cost comes from the agent's own output. Keep the
            # hooks' counters and log next to the run for inspection.
            c.get(TROVE_DATA, run_dir / "trove-data")

        parsed = combine_results(results, session_totals=getattr(self.agent, "resume_reports_session_total", True))
        rec = build_record(
            run_id=spec.run_id, task_id=spec.task_id, condition=spec.condition, seed=spec.seed,
            passed=grade.rc == 0, timed_out=res.timed_out, wall_s=wall, agent=parsed, gateway=stats,
            expected_states=cond.effective_states() if cond.uses_gateway else None,
            table=self.table, today=date.today(), agent_exit_ok=res.rc == 0, effort=spec.effort,
        )
        (run_dir / "record.json").write_text(json.dumps(rec.as_row(), indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")
        return rec

    def _setup_hooks(self, c: de.Container, td: Path, cond: Condition) -> None:
        """Install the rewriting hooks and prove they are on before the agent runs.

        Hooks need three things the gateway does not: the hooks in the agent's settings,
        a snapshot of the optimizer states, and a live probe result for this exact agent
        version (otherwise they stay in shadow). Each is checked here.
        """
        if not cond.uses_gateway or cond.recording_gateway:
            (td / "config.toml").write_text(render_config_toml(cond), encoding="utf-8", newline="\n")
            c.put(self.trove_bin, "/usr/local/bin/trove")
            c.exec(f"chmod +x /usr/local/bin/trove && mkdir -p {TROVE_DATA}", workdir="/")
            c.put(td / "config.toml", f"{TROVE_DATA}/config.toml")
        if self.fake:
            return      # the fake agent runs no hooks, so there is nothing to install or prove
        try:
            ver = c.exec(self.agent.version_command(), workdir="/").out.strip().split()
            caps = capabilities_for(self.hook_capabilities, ver[0] if ver else "")
            (td / "capabilities.json").write_text(caps, encoding="utf-8", newline="\n")
            c.exec(f"mkdir -p {TROVE_DATA}/hooks", workdir="/")
            c.put(td / "capabilities.json", f"{TROVE_DATA}/hooks/capabilities.json")
            r = c.exec(install_command(TROVE_DATA), workdir="/")
            if r.rc != 0:
                raise HooksSetupError(f"`trove hooks install --rewrite` failed in the container: {(r.out + r.err)[-300:]}")
            snap = c.exec(f"cat {TROVE_DATA}/hooks/config.json", workdir="/").out
            check_snapshot(snap, cond)
        except HooksSetupError as e:
            raise RunEnvError(str(e)) from e

    def _setup_mcp(self, c: de.Container, td: Path, cond: Condition) -> None:
        """Register Trove's MCP server with the agent, as the plugin does. Whether it took effect is proven later:
        an optimizer that needs it is clamped to shadow without it, which the hook snapshot check and the
        gateway's pinned states both catch before the run is counted."""
        (td / "config.toml").write_text(render_config_toml(cond), encoding="utf-8", newline="\n")
        c.put(self.trove_bin, "/usr/local/bin/trove")
        c.exec(f"chmod +x /usr/local/bin/trove && mkdir -p {TROVE_DATA}", workdir="/")
        c.put(td / "config.toml", f"{TROVE_DATA}/config.toml")
        r = c.exec(f"TROVE_DATA_DIR={TROVE_DATA} trove mcp install --yes --agent claude")
        if r.rc != 0:
            raise RunEnvError(f"`trove mcp install` failed in the container: {(r.out + r.err)[-300:]}")

    def _assert_configured(self, c: de.Container, cond: Condition, data_dir: str = TROVE_DATA) -> None:
        """Before spending anything, confirm the gateway read the condition's config as intended."""
        r = c.exec(f"TROVE_DATA_DIR={data_dir} trove optimize status --json", workdir="/")
        try:
            got = {o["name"]: o["configured"] for o in json.loads(r.out)["optimizers"]}
        except (json.JSONDecodeError, KeyError, TypeError) as e:
            raise RunEnvError(f"could not read `trove optimize status --json`: {r.err[-300:]}") from e
        want = cond.effective_states()
        bad = {k: (got.get(k), v) for k, v in want.items() if got.get(k) != v}
        if bad:
            raise RunEnvError(f"Trove did not apply condition '{cond.name}': {bad} (got, wanted). Check the Trove version in use.")
