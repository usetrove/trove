"""Write a run config derived from bench/config.yaml, so the pinned Claude Code version lives in one place.

    python bench/scripts/launch/make_config.py <out.yaml> <model> <batch_cap_usd> <run_cap_usd> <route_to or -> <condition>...
"""

import sys
from pathlib import Path

import yaml

out, model, batch_cap, run_cap, route_to, *conditions = sys.argv[1:]
base = yaml.safe_load(Path("bench/config.yaml").read_text(encoding="utf-8"))
base.update(model=model, repeats=1, conditions=conditions)
if route_to != "-":
    base["route_to"] = route_to
base["limits"] = {**base.get("limits", {}), "batch_cap_usd": float(batch_cap), "run_cap_usd": float(run_cap),
                  "run_timeout_s": 2400}
ship = base.setdefault("ship_rule", {})
ship["reference"] = conditions[0]
Path(out).parent.mkdir(parents=True, exist_ok=True)
Path(out).write_text(yaml.safe_dump(base, sort_keys=False), encoding="utf-8")
print(out)
