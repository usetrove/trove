import pathlib, subprocess, sys

left = [str(p) for p in pathlib.Path("pkg").rglob("*.py") if "legacy_total" in p.read_text()]
if left:
    print("still uses legacy_total:", ", ".join(left))
    sys.exit(1)
sys.exit(subprocess.call([sys.executable, "-m", "unittest", "-q"]))
