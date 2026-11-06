package store

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// Retention. Every table in the schema has exactly one decision here: either an
// age rule (rows older than retention_days are deleted) or a documented reason
// it is kept. TestEveryTableHasARetentionDecision fails when a table has none,
// so a new table cannot silently live forever. docs/data.md lists the same
// decisions per table, and TestDataDocMatchesSchema keeps the two together.

// retentionRule deletes rows older than the cutoff. SQL has one placeholder:
// the cutoff in unix milliseconds, or a local "2006-01-02" day for Day rules.
type retentionRule struct {
	Table string
	SQL   string
	Cols  []string // the timestamp columns the rule reads
	Day   bool
}

// retentionRules run in order. Child rows go before the fixes they belong to.
var retentionRules = []retentionRule{
	{"requests", `DELETE FROM requests WHERE ts < ?`, []string{"ts"}, false},
	{"sessions", `DELETE FROM sessions WHERE COALESCE(ended_at, started_at) < ?`, []string{"started_at", "ended_at"}, false},
	{"session_pins", `DELETE FROM session_pins WHERE pinned_at < ?`, []string{"pinned_at"}, false},
	{"history_requests", `DELETE FROM history_requests WHERE ts < ?`, []string{"ts"}, false},
	{"experiment_assignments", `DELETE FROM experiment_assignments WHERE assigned_at < ?`, []string{"assigned_at"}, false},
	{"session_features", `DELETE FROM session_features WHERE recorded_at < ?`, []string{"recorded_at"}, false},
	{"guardrail_events", `DELETE FROM guardrail_events WHERE ts < ?`, []string{"ts"}, false},
	{"receipts", `DELETE FROM receipts WHERE ended_at < ?`, []string{"ended_at"}, false},
	{"hook_events", `DELETE FROM hook_events WHERE ts < ?`, []string{"ts"}, false},
	{"adapter_events", `DELETE FROM adapter_events WHERE ts < ?`, []string{"ts"}, false},
	{"experiment_results", `DELETE FROM experiment_results WHERE computed_at < ?`, []string{"computed_at"}, false},
	{"repo_features", `DELETE FROM repo_features WHERE computed_at < ?`, []string{"computed_at"}, false},
	{"usage_daily", `DELETE FROM usage_daily WHERE day < ?`, []string{"day"}, true},
	// A fix that is still in place is state, not history, so only reverted fixes expire,
	// together with the rows that describe them.
	{"settings_profiles", `DELETE FROM settings_profiles WHERE change_id IN (SELECT change_id FROM fixes WHERE reverted_at IS NOT NULL AND reverted_at < ?)`, []string{"fixes.reverted_at"}, false},
	{"task_installs", `DELETE FROM task_installs WHERE change_id IN (SELECT change_id FROM fixes WHERE reverted_at IS NOT NULL AND reverted_at < ?)`, []string{"fixes.reverted_at"}, false},
	{"fixes", `DELETE FROM fixes WHERE reverted_at IS NOT NULL AND reverted_at < ?`, []string{"reverted_at"}, false},
}

// retentionKept lists tables that are not deleted by age, with the reason.
// Everything here is current state or settings, a counter without content, or
// a checkpoint, and `trove data delete` removes all of it.
var retentionKept = map[string]string{
	"optimizations":     "current optimizer state, one row per optimizer",
	"measurements":      "latest computed measurement windows, replaced as they are recomputed",
	"budgets":           "the user's own budget settings",
	"settings":          "install settings: repo-key salt, install time, last retention run",
	"external_tools":    "third-party tools the user installed, kept while installed",
	"history_files":     "per-file read checkpoints (a path hash and offsets), kept so purged history is not re-read",
	"repo_tuning":       "the tuning in force for a repo, current state until `trove tune reset`",
	"schema_migrations": "schema version history",
}

// RetentionDecisions returns every table's decision, for the docs test.
func RetentionDecisions() (aged map[string][]string, kept map[string]string) {
	aged = map[string][]string{}
	for _, r := range retentionRules {
		aged[r.Table] = r.Cols
	}
	return aged, retentionKept
}

var errRetentionDays = errors.New("retention days must be at least 1")

const retentionLastRunKey = "retention_last_run"

// Purge deletes rows older than days, relative to now, from every table with an
// age rule. It returns the number of request rows removed.
func (d *DB) Purge(ctx context.Context, days int, now time.Time) (int64, error) {
	if days < 1 {
		return 0, errRetentionDays
	}
	cutoff := now.AddDate(0, 0, -days)
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var requests int64
	for _, r := range retentionRules {
		var arg any = cutoff.UnixMilli()
		if r.Day {
			arg = cutoff.Local().Format("2006-01-02")
		}
		res, err := tx.ExecContext(ctx, r.SQL, arg)
		if err != nil {
			return 0, err
		}
		if r.Table == "requests" {
			requests, _ = res.RowsAffected()
		}
	}
	return requests, tx.Commit()
}

// PurgeIfDue runs Purge at most once per 24 hours across all of Trove's
// processes, so any command that opens the store can call it cheaply. It reports
// whether it ran.
func (d *DB) PurgeIfDue(ctx context.Context, days int, now time.Time) (bool, error) {
	var v string
	if err := d.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, retentionLastRunKey).Scan(&v); err == nil {
		if last, perr := strconv.ParseInt(v, 10, 64); perr == nil && now.UnixMilli()-last < int64(24*time.Hour/time.Millisecond) {
			return false, nil
		}
	}
	if _, err := d.Purge(ctx, days, now); err != nil {
		return false, err
	}
	_, err := d.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, retentionLastRunKey, strconv.FormatInt(now.UnixMilli(), 10))
	return true, err
}
