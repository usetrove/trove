package hooks

import (
	"context"
	"os"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/histscan"
	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/cchistory"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/regress"
	"github.com/usetrove/trove/internal/store"
)

// regressScanBudget bounds the history read before the regression watch runs (at most every
// regress.RefreshEvery).
const regressScanBudget = 700 * time.Millisecond

// regressScanDays is how far back that read looks at files: the watch's recent days plus one.
const regressScanDays = regress.RecentDays + 1

// regressStep keeps the regression watch current for the status line, at a session end: when the saved
// result is older than regress.RefreshEvery, it reads the newest Claude Code history (time-capped,
// incremental) and recomputes. It reads history only where Trove already does (history on, and history
// files already scanned once by `trove report` or `trove logs scan`), so it never starts reading a new
// folder on its own. Best effort and advice only: it changes nothing but regress.json.
func regressStep(ctx context.Context, db *store.DB, dir string, cfg config.Config, getenv func(string) string, now time.Time) {
	if cur := regress.ReadCache(dir); cur != nil && now.UnixMilli()-cur.ComputedAt < regress.RefreshEvery.Milliseconds() && cur.ComputedAt <= now.UnixMilli() {
		return
	}
	tbl, err := pricing.Default()
	if err != nil {
		tbl = nil
	}
	if cfg.History.Enabled && historyRead(ctx, db) {
		if getenv == nil {
			getenv = os.Getenv
		}
		src := &cchistory.Source{Getenv: getenv, Home: os.UserHomeDir}
		plan := logs.DetectClaudeCodePlan(getenv, src.ConfigDir(), cfg.History.Plan).Plan
		sc, cancel := context.WithTimeout(ctx, regressScanBudget)
		_, _ = histscan.New(db, tbl, src, nil, plan, logs.PlanUnknown).Scan(sc, logs.Options{Since: now.AddDate(0, 0, -regressScanDays)})
		cancel()
	}
	_ = regress.Refresh(ctx, db, dir, tbl, now, true)
}

// historyRead is true once Trove has scanned any Claude Code history file.
func historyRead(ctx context.Context, db *store.DB) bool {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT 1 FROM history_files WHERE source = 'claude_code' LIMIT 1)`).Scan(&n); err != nil {
		return false
	}
	return n > 0
}
