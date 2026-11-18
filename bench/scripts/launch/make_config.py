"""Write a run config derived from bench/config.yaml, so the pinned Claude Code version lives in one place.

    python bench/scripts/launch/make_config.py <out.yaml> <model> <batch_cap_usd> <run_cap_usd> <condition>...
"""

import sys
from pathlib import Path

import yaml

out, model, batch_cap, run_cap, *conditions = sys.argv[1:]
base = yaml.safe_load(Path("bench/config.yaml").read_text(encoding="utf-8"))
base.update(model=model, repeats=1, conditions=conditions)
base["limits"] = {**base.get("limits", {}), "batch_cap_usd": float(batch_cap), "run_cap_usd": float(run_cap),
                  "run_timeout_s": 2400}
base.setdefault("ship_rule", {})["reference"] = "passthrough"
Path(out).parent.mkdir(parents=True, exist_ok=True)
Path(out).write_text(yaml.safe_dump(base, sort_keys=False), encoding="utf-8")
print(out)
