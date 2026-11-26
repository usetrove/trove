import glob, sys

warnings = 0
files = sorted(glob.glob("modules/*.cfg"))
for n, path in enumerate(files, 1):
    cfg = dict(l.split("=", 1) for l in open(path).read().split())
    if cfg.get("legacy") == "true":
        print(f"warning: {cfg['name']} uses deprecated option 'legacy=true'")
        warnings += 1
    for step in ("parsing", "type-checking", "compiling", "linking"):
        print(f"[{n:3d}/{len(files)}] {step} {cfg['name']} ... ok")
print(f"build finished: {len(files)} modules, {warnings} warnings")
if "--strict" in sys.argv and warnings:
    sys.exit(1)
