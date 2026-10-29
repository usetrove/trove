"""One run = one fresh container. See README for the flow diagram."""

from __future__ import annotations

import json
import os
import shutil
import tempfile
import time
from dataclasses import dataclass, field
from datetime import date
from pathlib import Path

from . import docker_env as de
from .agent.base import Agent
from .collect import build_record, read_gateway_db
from .conditions import Condition, render_config_toml
from .manifest import Task
from .plan import RunSpec
from .pricing import PricingTable
from .records import RunRecord
from .redact import redact

KEY_ENV = "ANTHROPIC_API_KEY"
FAKE_KEY = "sk-ant-api03-benchfake-not-a-real-key-0000000000"
TROVE_DATA = "/trove-data"
GRADE_TIMEOUT_S = 900


class RunEnvError(RuntimeError):
    pass


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

    # --- batch setup ----------------------------------------------------------------
    def prepare(self, task_ids: list[str]) -> None:
        self.arch = de.host_arch()
        if not self.arch:
            raise RunEnvError("Could not tell the Docker host CPU architecture (expected amd64 or arm64).")
        bin_dir = self.batch_dir / "bin"
        bin_dir.mkdir(parents=True, exist_ok=True)
        self.trove_bin = bin_dir / "trove"
        de.go_build_linux("./cmd/trove", self.trove_bin, self.arch)
        if self.fake:
            self.fake_bin = bin_dir / "benchfake"
            de.go_build_linux("./bench/tools/benchfake", self.fake_bin, self.arch)
        for tid in task_ids:
            if tid not in self.images:
                self.images[tid] = de.build_task_image(self.tasks[tid], self.agent, self.claude_version)

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
        c.exec("mkdir -p /bench", workdir="/")
        (td / "prompt.txt").write_text(task.prompt, encoding="utf-8", newline="\n")
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

        gw_addr = ""
        if cond.uses_gateway:
            (td / "config.toml").write_text(render_config_toml(cond), encoding="utf-8", newline="\n")
            c.put(self.trove_bin, "/usr/local/bin/trove")
            c.exec(f"chmod +x /usr/local/bin/trove && mkdir -p {TROVE_DATA}", workdir="/")
            c.put(td / "config.toml", f"{TROVE_DATA}/config.toml")
            up = f"TROVE_UPSTREAMS_ANTHROPIC=http://{mock_addr} " if self.fake else ""
            r = c.exec(
                f"(nohup env TROVE_DATA_DIR={TROVE_DATA} {up}trove gateway start --listen 127.0.0.1:0 "
                f">/tmp/gateway.log 2>&1 &); for i in $(seq 1 150); do [ -s {TROVE_DATA}/gateway.json ] && break; sleep 0.1; done; "
                f"cat {TROVE_DATA}/gateway.json", workdir="/")
            try:
                gw_addr = json.loads(r.out.strip())["addr"]
            except (json.JSONDecodeError, KeyError) as e:
                log = c.exec("tail -c 600 /tmp/gateway.log", workdir="/").out
                raise RunEnvError(f"the Trove gateway did not start: {log}") from e
            h = c.exec(f"curl -fsS http://{gw_addr}/trove/health", workdir="/")
            if h.rc != 0:
                raise RunEnvError("the Trove gateway started but its health check failed")
            self._assert_configured(c, cond)
            agent_env.update(self.agent.base_url_env(f"http://{gw_addr}/anthropic"))
        elif self.fake:
            agent_env.update(self.agent.base_url_env(f"http://{mock_addr}"))

        timeout = min(task.timeout_s, self.run_timeout_s)
        t0 = time.monotonic()
        res = c.exec(self.agent.command(model=self.model, max_budget_usd=self.run_cap_usd, prompt_path="/bench/prompt.txt"),
                     timeout=timeout, env=agent_env)
        wall = time.monotonic() - t0
        (run_dir / "agent_stdout.txt").write_text(redact(res.out, secrets), encoding="utf-8", newline="\n")
        (run_dir / "agent_stderr_tail.txt").write_text(redact(res.err[-4000:], secrets), encoding="utf-8", newline="\n")

        grade = c.exec(" && ".join([*task.grading_setup, task.success_cmd]) if task.grading_setup else task.success_cmd,
                       timeout=GRADE_TIMEOUT_S)
        (run_dir / "grading_tail.txt").write_text(redact((grade.out + grade.err)[-4000:], secrets), encoding="utf-8", newline="\n")

        stats = None
        if cond.uses_gateway:
            c.exec(f"pid=$(sed -n 's/.*\"pid\":\\([0-9]*\\).*/\\1/p' {TROVE_DATA}/gateway.json); "
                   f"kill -TERM $pid 2>/dev/null; for i in $(seq 1 150); do [ -e {TROVE_DATA}/gateway.json ] || break; sleep 0.1; done",
                   workdir="/")
            c.get(TROVE_DATA, run_dir / "trove-data")
            (run_dir / "gateway.log").write_text(redact(c.exec("tail -c 4000 /tmp/gateway.log", workdir="/").out, secrets),
                                                 encoding="utf-8", newline="\n")
            stats = read_gateway_db(run_dir / "trove-data" / "trove.db")

        parsed = self.agent.parse(res.out)
        rec = build_record(
            run_id=spec.run_id, task_id=spec.task_id, condition=spec.condition, seed=spec.seed,
            passed=grade.rc == 0, timed_out=res.timed_out, wall_s=wall, agent=parsed, gateway=stats,
            expected_states=cond.effective_states() if cond.uses_gateway else None,
            table=self.table, today=date.today(), agent_exit_ok=res.rc == 0,
        )
        (run_dir / "record.json").write_text(json.dumps(rec.as_row(), indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")
        return rec

    def _assert_configured(self, c: de.Container, cond: Condition) -> None:
        """Before spending anything, confirm the gateway read the condition's config as intended."""
        r = c.exec(f"TROVE_DATA_DIR={TROVE_DATA} trove optimize status --json", workdir="/")
        try:
            got = {o["name"]: o["configured"] for o in json.loads(r.out)["optimizers"]}
        except (json.JSONDecodeError, KeyError, TypeError) as e:
            raise RunEnvError(f"could not read `trove optimize status --json`: {r.err[-300:]}") from e
        want = cond.effective_states()
        bad = {k: (got.get(k), v) for k, v in want.items() if got.get(k) != v}
        if bad:
            raise RunEnvError(f"Trove did not apply condition '{cond.name}': {bad} (got, wanted). Check the Trove version in use.")
