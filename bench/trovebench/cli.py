"""python -m trovebench {plan,run,report,smoke}"""

from __future__ import annotations

import argparse
import dataclasses
import json
import statistics
import sys
from datetime import date
from pathlib import Path

from . import pricing as pricing_mod
from .agent import get_agent
from .config import Config, ConfigError, load_config
from .conditions import ConditionError, parse_conditions
from .cost import estimate
from .docker_env import DockerError
from .docker_run import ContainerRunner, RunEnvError
from .ledger import Ledger
from .manifest import ManifestError, Task, load_tasks
from .plan import RunSpec, build_plan, read_plan, write_plan
from .report import write_all
from .runner import execute
from .sources.http import FetchError
from .validate import NEEDS_VALIDATION, is_validated, validate_task

BENCH = Path(__file__).resolve().parents[1]


class UserError(Exception):
    """Plain-English problem the user can fix; printed without a traceback."""


def _common(p: argparse.ArgumentParser) -> None:
    p.add_argument("--config", type=Path, help="config file (default bench/config.yaml)")
    p.add_argument("--tasks", type=Path, help="folder of task manifests (*.yaml)")
    p.add_argument("--batch", type=Path, help="batch folder (default bench/results/<today>)")
    p.add_argument("--repeats", type=int, help="runs per task per condition (overrides config)")
    p.add_argument("--fake", action="store_true", help="use the fake agent and a mock upstream: no API key, no spend")
    p.add_argument("--dry-run", action="store_true", help="print the plan and cost estimate, then stop")
    p.add_argument("--json", action="store_true", help="print a machine-readable summary")


def build_parser() -> argparse.ArgumentParser:
    ap = argparse.ArgumentParser(prog="trovebench", description="A/B harness for coding agents with and without Trove.")
    sub = ap.add_subparsers(dest="cmd", required=True)
    for name, help_ in (("plan", "show the run plan and cost estimate; spends nothing"), ("run", "execute the plan (resumes automatically)")):
        p = sub.add_parser(name, help=help_)
        _common(p)
        p.add_argument("--yes", action="store_true", help="confirm spending real API money")
    r = sub.add_parser("report", help="rebuild the report and CSVs from a batch folder")
    r.add_argument("--batch", type=Path, required=True)
    r.add_argument("--json", action="store_true")
    s = sub.add_parser("smoke", help="3 tiny tasks x 2 conditions, end to end")
    _common(s)
    s.add_argument("--yes", action="store_true", help="confirm spending real API money")

    f = sub.add_parser("fetch", help="generate task manifests from polyglot or SWE-bench Verified")
    f.add_argument("source", choices=["polyglot", "swebench"])
    f.add_argument("--out", type=Path, help="output folder (default bench/tasks/<source>)")
    f.add_argument("-n", type=int, default=10, help="how many tasks")
    f.add_argument("--seed", type=int, default=1, help="seed for the random subset")
    f.add_argument("--commit", help="polyglot: exact commit to check out and record")
    f.add_argument("--image", default="python:3.9-slim", help="swebench: base Docker image for the environment")
    m = sub.add_parser("mine", help="mine tasks from recently merged PRs that close issues (needs GITHUB_TOKEN)")
    m.add_argument("--repos", type=Path, default=BENCH / "repos.yaml", help="allowlist file (see repos.example.yaml)")
    m.add_argument("--since", required=True, help="only PRs merged on or after this date, YYYY-MM-DD")
    m.add_argument("--out", type=Path, default=BENCH / "tasks" / "mined")
    m.add_argument("--limit", type=int, default=5, help="max tasks per repo")
    v = sub.add_parser("validate", help="prove each task fails on the base and passes with the upstream fix")
    v.add_argument("--tasks", type=Path, required=True)
    v.add_argument("--config", type=Path)
    return ap


def _setup(args, *, smoke: bool = False) -> tuple[Config, list[Task], list, list[RunSpec], Path]:
    cfg = load_config(args.config)
    if args.fake:
        cfg = dataclasses.replace(cfg, agent="fake")
    if smoke:
        cfg = dataclasses.replace(cfg, conditions=("direct", "all_safe"), repeats=1)
    if args.repeats:
        cfg = dataclasses.replace(cfg, repeats=args.repeats)
    tasks_dir = args.tasks or (BENCH / "tasks" / "smoke" if smoke else None)
    if tasks_dir is None:
        raise UserError("Give --tasks <folder of manifests>. The smoke suite lives in bench/tasks/smoke.")
    tasks = load_tasks(tasks_dir)
    if not tasks:
        raise UserError(f"No task manifests (*.yaml) found under {tasks_dir}.")
    conds = parse_conditions(list(cfg.conditions), "claude_code" if cfg.agent == "fake" else cfg.agent)
    specs = build_plan(tasks, conds, cfg.repeats, cfg.order_seed)
    batch = args.batch or (BENCH / "results" / (date.today().isoformat() + ("-smoke" if smoke else "")))
    return cfg, tasks, conds, specs, batch


def _cmd_run(args, *, smoke: bool = False, plan_only: bool = False) -> int:
    cfg, tasks, conds, specs, batch = _setup(args, smoke=smoke)
    batch.mkdir(parents=True, exist_ok=True)
    plan_path = batch / "plan.json"
    if plan_path.exists() and read_plan(plan_path) != specs:
        raise UserError(f"{plan_path} holds a different plan from the one these settings produce. "
                        "Resume with the original settings, or pick a new --batch folder.")
    ledger = Ledger(batch / "ledger.jsonl")
    table = pricing_mod.load()
    done = {rid for rid in ledger.records if ledger.done(rid)}
    observed = [r.cost_usd for r in ledger.records.values() if r.cost_usd is not None and r.status == "ok"]
    est = estimate(specs, done, table, cfg.model, run_cap_usd=cfg.run_cap_usd, batch_cap_usd=cfg.batch_cap_usd,
                   profile=cfg.profile, observed_mean_usd=statistics.fmean(observed) if observed else None)
    skipped = [c for c in conds if not c.measurable]
    summary = {
        "batch": str(batch), "tasks": len(tasks), "runs_planned": est.n_runs, "runs_remaining": est.n_remaining,
        "conditions": [c.name for c in conds if c.measurable],
        "not_measurable": {c.name: c.unsupported_reason for c in skipped},
        "expected_cost_usd_estimated": round(est.expected_usd, 4), "worst_case_usd": round(est.worst_case_usd, 2),
        "batch_cap_usd": est.batch_cap_usd, "basis": est.basis, "fake": args.fake,
    }
    if args.json:
        print(json.dumps(summary, indent=2))
    else:
        print(f"{est.n_runs} runs planned ({est.n_remaining} remaining): {len(tasks)} task(s) x "
              f"{len(summary['conditions'])} condition(s) x {cfg.repeats} repeat(s), randomized order.")
        for c in skipped:
            print(f"  skipped {c.name}: {c.unsupported_reason}")
        print(f"Estimated cost: ${est.expected_usd:.2f} ({est.basis}).")
        print(f"Worst case if every run hits its ${cfg.run_cap_usd:.2f} cap: ${est.worst_case_usd:.2f}. Batch cap: ${est.batch_cap_usd:.2f}.")
    if plan_only or args.dry_run:
        return 0
    unvalidated = [t.id for t in tasks if t.source in NEEDS_VALIDATION and not is_validated(t)]
    if unvalidated:
        raise UserError(f"{len(unvalidated)} task(s) have not passed validation (first: {unvalidated[0]}). "
                        "Run `python -m trovebench validate --tasks <folder>` first; it proves each task is solvable "
                        "before any agent is paid to try it. Remove tasks that are rejected.")
    if not args.fake:
        if est.expected_usd > est.batch_cap_usd:
            raise UserError(f"The estimate (${est.expected_usd:.2f}) is above the batch cap (${est.batch_cap_usd:.2f}). "
                            "Lower the number of tasks or repeats, or raise limits.batch_cap_usd in the config.")
        if not args.yes:
            raise UserError("This will spend real API money on your key. Re-run with --yes to confirm "
                            f"(hard stop at ${est.batch_cap_usd:.2f}).")
    write_plan(plan_path, specs)
    (batch / "config.used.yaml").write_text(_config_text(args.config), encoding="utf-8", newline="\n")

    runner = ContainerRunner(
        agent=get_agent(cfg.agent), model=cfg.model, run_cap_usd=cfg.run_cap_usd, run_timeout_s=cfg.run_timeout_s,
        batch_dir=batch, tasks={t.id: t for t in tasks}, conditions={c.name: c for c in conds if c.measurable},
        table=table, claude_version=cfg.claude_version, fake=args.fake,
    )
    print("Preparing binaries and task images (the first build can take a while)...")
    runner.prepare(sorted({s.task_id for s in specs}))
    versions = runner.versions()
    versions["config_sha256"] = cfg.sha256
    (batch / "versions.json").write_text(json.dumps(versions, indent=2, sort_keys=True) + "\n", encoding="utf-8", newline="\n")

    result = execute(specs, ledger, runner.run_one, batch_cap_usd=cfg.batch_cap_usd, run_cap_usd=cfg.run_cap_usd)
    _write_report(batch, cfg, conds, versions, len(tasks), ledger)
    print(f"Ran {result.ran}, skipped {result.skipped_done} already done, spent ${result.spent_usd:.4f}. Stop reason: {result.stop_reason}.")
    print(f"Report: {batch / 'report.md'}")
    return 0 if result.complete else 3


def _config_text(path: Path | None) -> str:
    from .config import DEFAULT_PATH

    return (path or DEFAULT_PATH).read_text(encoding="utf-8")


def _write_report(batch: Path, cfg: Config, conds, versions: dict, n_tasks: int, ledger: Ledger) -> None:
    write_all(batch, ledger.final_records(), conds, cfg.ship_rule, versions, (batch / "config.used.yaml").read_text(encoding="utf-8"), n_tasks)


def _cmd_report(args) -> int:
    import tempfile

    batch: Path = args.batch
    for need in ("ledger.jsonl", "versions.json", "config.used.yaml"):
        if not (batch / need).exists():
            raise UserError(f"{batch / need} is missing; this does not look like a trovebench batch folder.")
    cfg = load_config(batch / "config.used.yaml")
    conds = parse_conditions(list(cfg.conditions), "claude_code" if cfg.agent == "fake" else cfg.agent)
    versions = json.loads((batch / "versions.json").read_text(encoding="utf-8"))
    ledger = Ledger(batch / "ledger.jsonl")
    n_tasks = len({r.task_id for r in ledger.records.values()})
    _write_report(batch, cfg, conds, versions, n_tasks, ledger)
    print(f"Report: {batch / 'report.md'}")
    return 0


def _cmd_fetch(args) -> int:
    import subprocess

    from .sources import polyglot, swebench
    from .sources.http import get as http_get

    out = args.out or (BENCH / "tasks" / args.source)
    if args.source == "polyglot":
        cache = BENCH / "cache" / "polyglot-benchmark"
        if not cache.exists():
            cache.parent.mkdir(parents=True, exist_ok=True)
            subprocess.run(["git", "clone", "-q", polyglot.REPO_URL, str(cache)], check=True)
        if args.commit:
            subprocess.run(["git", "-C", str(cache), "checkout", "-q", args.commit], check=True)
        commit = subprocess.run(["git", "-C", str(cache), "rev-parse", "HEAD"], check=True, capture_output=True, text=True).stdout.strip()
        tasks = polyglot.generate(cache, commit, out, n=args.n, seed=args.seed)
        print(f"Wrote {len(tasks)} polyglot (Python) task(s) to {out}, pinned to {commit[:10]}. "
              "Exercise text is copyright Exercism: keep these local.")
    else:
        rows, rev = swebench.fetch_rows(lambda u: http_get(u))
        tasks = swebench.generate(rows, rev, out, n=args.n, seed=args.seed, image=args.image)
        print(f"Wrote {len(tasks)} SWE-bench Verified task(s) to {out}, dataset revision {rev[:10]}. "
              "Run `validate` next: environments are built from the repo and many older instances will be rejected.")
    return 0


def _cmd_mine(args) -> int:
    from .sources import mine
    from .sources.http import get as http_get

    specs = mine.load_repos(args.repos)
    total = 0
    for spec in specs:
        tasks, skips = mine.mine_repo(spec, http_get, args.since, args.out, limit=args.limit)
        total += len(tasks)
        print(f"{spec.repo}: {len(tasks)} task(s)")
        for s in skips:
            print(f"  skipped {s}")
    print(f"Wrote {total} task(s) to {args.out}. Run `validate --tasks {args.out}` before using them.")
    return 0


def _cmd_validate(args) -> int:
    cfg = load_config(args.config)
    agent = get_agent(cfg.agent)
    bad = 0
    for t in load_tasks(args.tasks):
        v = validate_task(t, agent, cfg.claude_version)
        print(f"{v.status.upper():9} {v.task_id}: {v.reason}")
        bad += v.status == "rejected"
    return 1 if bad else 0


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.cmd == "fetch":
            return _cmd_fetch(args)
        if args.cmd == "mine":
            return _cmd_mine(args)
        if args.cmd == "validate":
            return _cmd_validate(args)
        if args.cmd == "plan":
            return _cmd_run(args, plan_only=True)
        if args.cmd == "run":
            return _cmd_run(args)
        if args.cmd == "smoke":
            return _cmd_run(args, smoke=True)
        return _cmd_report(args)
    except (UserError, ConfigError, ManifestError, ConditionError, DockerError, RunEnvError, pricing_mod.PricingError,
            NotImplementedError, ValueError, FetchError) as e:
        print(f"trovebench: {e}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
