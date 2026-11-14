import csv
import importlib.util
import json
import sqlite3
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("hook_activity", Path(__file__).parent.parent / "tools" / "hook_activity.py")
ha = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ha)


def make_run(batch: Path, run_id: str, condition: str, db_events=(), spool=()):
    d = batch / "runs" / run_id / "trove-data"
    (d / "hooks").mkdir(parents=True)
    if db_events:
        con = sqlite3.connect(d / "trove.db")
        con.execute("CREATE TABLE hook_events (id INTEGER PRIMARY KEY, ts INTEGER, session_key TEXT, hook TEXT, kind TEXT, bytes INTEGER, tokens INTEGER)")
        for hook, kind in db_events:
            con.execute("INSERT INTO hook_events(ts, session_key, hook, kind, bytes, tokens) VALUES (1,'s',?,?,0,0)", (hook, kind))
        con.commit()
        con.close()
    if spool:
        (d / "hooks" / "events.jsonl").write_text("\n".join(json.dumps({"t": 1, "s": "s", "h": h, "k": k}) for h, k in spool) + "\ntorn {\n", encoding="utf-8")


def write_csv(batch: Path, rows):
    with (batch / "runs.csv").open("w", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=["run_id", "condition"])
        w.writeheader()
        w.writerows(rows)


def test_counts_runs_where_a_hook_really_acted(tmp_path):
    make_run(tmp_path, "a", "alone_hook_repeat_read", db_events=[("hook_repeat_read", "blocked")] * 3)
    make_run(tmp_path, "b", "alone_hook_repeat_read", spool=[("hook_repeat_read", "blocked")])          # not ingested yet
    make_run(tmp_path, "c", "alone_hook_repeat_read")                                                   # never acted
    make_run(tmp_path, "d", "alone_hook_repeat_read", db_events=[("hook_repeat_read", "would_block")])  # shadow only
    make_run(tmp_path, "e", "direct")
    write_csv(tmp_path, [{"run_id": r, "condition": c} for r, c in
                         [("a", "alone_hook_repeat_read"), ("b", "alone_hook_repeat_read"), ("c", "alone_hook_repeat_read"),
                          ("d", "alone_hook_repeat_read"), ("e", "direct")]])
    s = ha.summarise(tmp_path)
    c = s["alone_hook_repeat_read"]
    assert (c["runs"], c["acted_runs"], c["would_only_runs"]) == (4, 2, 1)
    assert c["acts"]["hook_repeat_read"] == 4
    assert s["direct"]["acted_runs"] == 0


def test_a_run_with_no_saved_data_counts_as_a_run_that_did_not_act(tmp_path):
    write_csv(tmp_path, [{"run_id": "missing", "condition": "alone_hook_quiet_commands"}])
    c = ha.summarise(tmp_path)["alone_hook_quiet_commands"]
    assert (c["runs"], c["acted_runs"]) == (1, 0)


def test_the_report_explains_how_to_read_a_rare_hook(tmp_path):
    make_run(tmp_path, "a", "alone_hook_quiet_commands")
    write_csv(tmp_path, [{"run_id": "a", "condition": "alone_hook_quiet_commands"}])
    text = ha.render(ha.summarise(tmp_path))
    assert "alone_hook_quiet_commands | 1 | 0 (0%)" in text and "rarely had anything to do" in text


def test_a_database_without_the_table_is_not_an_error(tmp_path):
    d = tmp_path / "runs" / "a" / "trove-data"
    d.mkdir(parents=True)
    sqlite3.connect(d / "trove.db").close()
    write_csv(tmp_path, [{"run_id": "a", "condition": "hooks"}])
    assert ha.summarise(tmp_path)["hooks"]["acted_runs"] == 0
