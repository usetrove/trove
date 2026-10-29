"""Docker plumbing: build binaries and task images, run one fresh container per run.

The API key is never put on a command line: `-e NAME` makes docker read the value
from this process's environment. Everything we capture is redacted before it is
stored or raised.
"""

from __future__ import annotations

import hashlib
import json
import os
import shlex
import subprocess
import uuid
from dataclasses import dataclass
from pathlib import Path

from .agent.base import Agent
from .manifest import Task
from .redact import redact

REPO_ROOT = Path(__file__).resolve().parents[2]
PKGS = "git curl ca-certificates bash coreutils"


class DockerError(RuntimeError):
    pass


def docker(*args: str, timeout: float | None = None, input: str | None = None, check: bool = True,
           secrets: tuple[str, ...] = ()) -> subprocess.CompletedProcess:
    try:
        p = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=timeout, input=input,
                           encoding="utf-8", errors="replace")
    except FileNotFoundError as e:
        raise DockerError("Docker is not installed or not on PATH. Install Docker Desktop or Docker Engine, then retry.") from e
    except subprocess.TimeoutExpired as e:
        raise DockerError(f"docker {args[0]} timed out after {timeout:.0f}s.") from e
    if check and p.returncode != 0:
        raise DockerError(redact(f"docker {args[0]} failed (exit {p.returncode}): {p.stderr.strip()[-800:]}", secrets))
    return p


def host_arch() -> str:
    out = docker("info", "--format", "{{.Architecture}}", timeout=30).stdout.strip()
    return {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(out, "")


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    h.update(path.read_bytes())
    return h.hexdigest()


def git(*args: str, cwd: Path = REPO_ROOT) -> str:
    p = subprocess.run(["git", *args], cwd=cwd, capture_output=True, text=True, encoding="utf-8")
    return p.stdout.strip() if p.returncode == 0 else ""


def go_build_linux(pkg: str, out: Path, arch: str) -> None:
    env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch)
    p = subprocess.run(["go", "build", "-trimpath", "-o", str(out), pkg], cwd=REPO_ROOT, env=env,
                       capture_output=True, text=True, encoding="utf-8")
    if p.returncode != 0:
        raise DockerError(f"go build {pkg} failed: {p.stderr.strip()[-800:]}. Install Go and run from a Trove checkout.")


def context_hash(root: Path) -> str:
    h = hashlib.sha256()
    for p in sorted(root.rglob("*")):
        if p.is_file():
            h.update(p.relative_to(root).as_posix().encode())
            h.update(p.read_bytes())
    return h.hexdigest()


def render_dockerfile(task: Task, agent: Agent, agent_version: str | None) -> str:
    lines = [
        f"FROM {task.image}",
        "ENV DEBIAN_FRONTEND=noninteractive",
        f"RUN apt-get update && apt-get install -y --no-install-recommends {PKGS} && rm -rf /var/lib/apt/lists/*",
        *agent.install_lines(agent_version),
        "WORKDIR /work",
    ]
    if task.repo.startswith("local:"):
        lines += [
            "COPY repo/ /work/",
            "RUN git init -q && git add -A && git -c user.name=bench -c user.email=bench@localhost commit -qm base",
        ]
    else:
        r, c = shlex.quote(task.repo), shlex.quote(task.base_commit)
        lines.append(f"RUN git init -q . && git remote add origin {r} && git fetch -q --depth 1 origin {c} && git checkout -q FETCH_HEAD")
    for cmd in task.setup:
        lines.append("RUN " + json.dumps(["bash", "-lc", cmd]))
    return "\n".join(lines) + "\n"


def build_task_image(task: Task, agent: Agent, agent_version: str | None) -> tuple[str, str]:
    """Build (or reuse) the image for a task. Returns (tag, image id). The tag embeds a hash of the
    Dockerfile and any local source, so a changed task never silently reuses a stale image."""
    dockerfile = render_dockerfile(task, agent, agent_version)
    ctx = Path(task.base_dir) / task.repo.split(":", 1)[1] if task.repo.startswith("local:") else None
    if ctx is not None and not ctx.is_dir():
        raise DockerError(f"{task.id}: local repo folder {ctx} does not exist.")
    digest = hashlib.sha256(dockerfile.encode() + (context_hash(ctx).encode() if ctx else b"")).hexdigest()[:12]
    tag = f"trovebench/{task.id}:{digest}"
    have = docker("image", "inspect", "--format", "{{.Id}}", tag, check=False)
    if have.returncode == 0:
        return tag, have.stdout.strip()
    import shutil
    import tempfile

    with tempfile.TemporaryDirectory() as td:
        build_ctx = Path(td)
        if ctx is not None:
            shutil.copytree(ctx, build_ctx / "repo")
        docker("build", "-t", tag, "-f", "-", str(build_ctx), input=dockerfile, timeout=3600)
    return tag, docker("image", "inspect", "--format", "{{.Id}}", tag).stdout.strip()


@dataclass(frozen=True)
class ExecResult:
    rc: int
    out: str
    err: str
    timed_out: bool


class Container:
    """One fresh container. Always removed, even on error."""

    def __init__(self, image: str, run_id: str, *, secret_env: tuple[str, ...] = (), secrets: tuple[str, ...] = (),
                 memory: str = "4g", cpus: str = "2"):
        self.name = f"tb-{run_id[:40].replace('.', '-')}-{uuid.uuid4().hex[:6]}"
        self.image, self.secret_env, self.secrets, self.memory, self.cpus = image, secret_env, secrets, memory, cpus

    def __enter__(self) -> "Container":
        args = ["run", "-d", "--init", "--name", self.name, "--memory", self.memory, "--cpus", self.cpus]
        for k in self.secret_env:
            args += ["-e", k]           # value comes from our environment, never the command line
        docker(*args, self.image, "sleep", "infinity", timeout=120, secrets=self.secrets)
        return self

    def __exit__(self, *exc) -> None:
        docker("rm", "-f", self.name, check=False, timeout=120)

    def exec(self, script: str, *, timeout: int | None = None, env: dict[str, str] | None = None,
             workdir: str = "/work") -> ExecResult:
        args = ["exec", "-w", workdir]
        for k in self.secret_env:
            args += ["-e", k]
        for k, v in (env or {}).items():
            args += ["-e", f"{k}={v}"]
        inner = script
        if timeout:
            inner = f"timeout -k 15 {int(timeout)} bash -lc {shlex.quote(script)}"
        try:
            p = docker(*args, self.name, "bash", "-lc", inner, timeout=(timeout + 90) if timeout else None,
                       check=False, secrets=self.secrets)
        except DockerError:
            return ExecResult(124, "", "docker exec did not return in time", True)
        timed_out = bool(timeout) and p.returncode in (124, 137)
        return ExecResult(p.returncode, redact(p.stdout, self.secrets), redact(p.stderr, self.secrets), timed_out)

    def put(self, src: Path, dest: str) -> None:
        docker("cp", str(src), f"{self.name}:{dest}", timeout=300)

    def get(self, src: str, dest: Path) -> None:
        docker("cp", f"{self.name}:{src}", str(dest), timeout=300)
