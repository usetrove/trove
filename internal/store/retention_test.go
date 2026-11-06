package store

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

func schemaTables(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// A new table is a retention decision: delete by age, or keep with a reason.
func TestEveryTableHasARetentionDecision(t *testing.T) {
	db := openTemp(t)
	aged, kept := RetentionDecisions()
	for _, tbl := range schemaTables(t, db) {
		_, a := aged[tbl]
		_, k := kept[tbl]
		switch {
		case a && k:
			t.Errorf("table %q has both an age rule and a keep reason", tbl)
		case !a && !k:
			t.Errorf("table %q has no retention decision: add a rule to retentionRules in retention.go, or a reason to retentionKept", tbl)
		}
	}
	have := map[string]bool{}
	for _, tbl := range schemaTables(t, db) {
		have[tbl] = true
	}
	for tbl := range aged {
		if !have[tbl] {
			t.Errorf("retention rule for %q, which is not a table", tbl)
		}
	}
	for tbl, why := range kept {
		if !have[tbl] {
			t.Errorf("keep reason for %q, which is not a table", tbl)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("keep reason for %q is empty", tbl)
		}
	}
}

type colInfo struct {
	name, typ string
	pk        int
}

func columns(t *testing.T, db *DB, table string) []colInfo {
	t.Helper()
	rows, err := db.Query(`SELECT name, type, pk FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []colInfo
	for rows.Next() {
		var c colInfo
		_ = rows.Scan(&c.name, &c.typ, &c.pk)
		out = append(out, c)
	}
	return out
}

// insertRow inserts one row whose timestamp columns are at ts (unix ms, or a day for
// day tables) and everything else is a harmless filler. tag keeps primary keys apart.
func insertRow(t *testing.T, db *DB, table string, tsCols map[string]any, tag string) {
	t.Helper()
	var names []string
	var marks []string
	var args []any
	cols := columns(t, db, table)
	enums := enumChecks(t, db, table)
	for _, c := range cols {
		if v, ok := tsCols[c.name]; ok {
			names, marks, args = append(names, c.name), append(marks, "?"), append(args, v)
			continue
		}
		if c.pk == 1 && strings.EqualFold(c.typ, "INTEGER") && len(pkCols(cols)) == 1 {
			continue // rowid alias
		}
		var v any = 1
		if strings.EqualFold(c.typ, "TEXT") {
			v = "x-" + tag
		}
		if allowed, ok := enums[c.name]; ok {
			v = allowed
		}
		names, marks, args = append(names, c.name), append(marks, "?"), append(args, v)
	}
	q := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, table, strings.Join(names, ","), strings.Join(marks, ","))
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("cannot insert a filler row into %s: %v", table, err)
	}
}

var enumRE = regexp.MustCompile(`(\w+)\s+IN\s*\(\s*'([^']*)'`)

// enumChecks maps a column to the first value its CHECK (col IN ('a', 'b')) allows.
func enumChecks(t *testing.T, db *DB, table string) map[string]string {
	t.Helper()
	var sqlText string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&sqlText); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, m := range enumRE.FindAllStringSubmatch(sqlText, -1) {
		out[m[1]] = m[2]
	}
	return out
}

func pkCols(cols []colInfo) []string {
	var out []string
	for _, c := range cols {
		if c.pk > 0 {
			out = append(out, c.name)
		}
	}
	return out
}

func count(t *testing.T, db *DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPurgeDeletesOldRowsFromEveryAgedTable(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	old, fresh := now.AddDate(0, 0, -200), now.AddDate(0, 0, -1)
	aged, _ := RetentionDecisions()
	for _, r := range retentionRules {
		switch r.Table {
		case "fixes", "settings_profiles", "task_installs":
			continue // child rows hang off fixes; see TestPurgeKeepsActiveFixesAndExpiresRevertedOnes
		}
		t.Run(r.Table, func(t *testing.T) {
			db := openTemp(t)
			ts := func(when time.Time) map[string]any {
				m := map[string]any{}
				for _, c := range aged[r.Table] {
					if r.Day {
						m[c] = when.Format("2006-01-02")
					} else {
						m[c] = when.UnixMilli()
					}
				}
				return m
			}
			insertRow(t, db, r.Table, ts(old), "old")
			insertRow(t, db, r.Table, ts(fresh), "new")
			if _, err := db.Purge(context.Background(), 90, now); err != nil {
				t.Fatal(err)
			}
			if got := count(t, db, r.Table); got != 1 {
				t.Fatalf("%s holds %d rows after the purge, want only the recent one", r.Table, got)
			}
		})
	}
}

func TestPurgeKeepsActiveFixesAndExpiresRevertedOnes(t *testing.T) {
	db := openTemp(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	oldMs, freshMs := now.AddDate(0, 0, -200).UnixMilli(), now.AddDate(0, 0, -1).UnixMilli()
	fix := func(id string, applied int64, reverted any) {
		if _, err := db.Exec(`INSERT INTO fixes (change_id, fix_id, kind, agent, path_hash, applied_at, reverted_at, est_tokens, est_usd, label)
			VALUES (?, 'f', 'k', 'a', 'h', ?, ?, 0, 0, 'measured')`, id, applied, reverted); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO settings_profiles (change_id, fix_id, task, harness, keys, harness_version, catalog_version)
			VALUES (?, 'f', 't', 'h', '[]', '1', '1')`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO task_installs (change_id, fix_id, feature, role, scout_model, worker_model, escalation_model, overhead_request, overhead_use)
			VALUES (?, 'f', 'f', 'r', '', '', '', 0, 0)`, id); err != nil {
			t.Fatal(err)
		}
	}
	fix("active-old", oldMs, nil)         // still in place: state, never expires
	fix("reverted-old", oldMs, oldMs)     // reverted long ago: expires with its rows
	fix("reverted-fresh", oldMs, freshMs) // reverted recently: stays
	if _, err := db.Purge(context.Background(), 90, now); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"fixes", "settings_profiles", "task_installs"} {
		rows, err := db.Query(`SELECT change_id FROM ` + tbl + ` ORDER BY change_id`)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for rows.Next() {
			var id string
			_ = rows.Scan(&id)
			got = append(got, id)
		}
		_ = rows.Close()
		if strings.Join(got, ",") != "active-old,reverted-fresh" {
			t.Errorf("%s keeps %v, want [active-old reverted-fresh]", tbl, got)
		}
	}
}

func TestPurgeRejectsNonsenseRetention(t *testing.T) {
	if _, err := openTemp(t).Purge(context.Background(), 0, time.Now()); err == nil {
		t.Fatal("0 days would delete everything; it must be refused")
	}
}

func TestPurgeIfDueRunsOncePerDay(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.Local)
	ran, err := db.PurgeIfDue(ctx, 90, now)
	if err != nil || !ran {
		t.Fatalf("first call: ran=%v err=%v, want it to run", ran, err)
	}
	if ran, _ := db.PurgeIfDue(ctx, 90, now.Add(time.Hour)); ran {
		t.Error("a second call within a day must not purge again")
	}
	if ran, _ := db.PurgeIfDue(ctx, 90, now.Add(25*time.Hour)); !ran {
		t.Error("a call after a day must purge again")
	}
}
