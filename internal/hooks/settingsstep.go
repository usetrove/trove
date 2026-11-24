package hooks

import (
	"context"
	"os"
	"time"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/histscan"
	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/cchistory"
	"github.com/usetrove/trove/internal/measure/limitfit"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// settingsScanBudget bounds the history read before the settings policy judges (at most every six hours).
const settingsScanBudget = 500 * time.Millisecond

// settingsStep runs Autopilot's settings policy at a session end (docs/autopilot.md, "Settings policy").
// Best effort: any failure leaves the settings and autopilot.json as they were. The session id and folder
// are used in memory only, to tell this session's transcript from others' and to read the project's settings.
func settingsStep(ctx context.Context, db *store.DB, dir string, cfg config.Config, in Input, getenv func(string) string, now time.Time) {
	f, err := config.LoadAutopilot(dir)
	if err != nil || !autopilot.SettingsDue(f, now) {
		return
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	src := &cchistory.Source{Getenv: getenv, Home: os.UserHomeDir}
	home, _ := os.UserHomeDir()
	tbl, perr := pricing.Default()
	if perr != nil {
		tbl = nil
	}
	if cfg.History.Enabled && f.SettingsActive() {
		plan := logs.DetectClaudeCodePlan(getenv, src.ConfigDir(), cfg.History.Plan).Plan
		sc, cancel := context.WithTimeout(ctx, settingsScanBudget)
		_, _ = histscan.New(db, tbl, src, nil, plan, logs.PlanUnknown).Scan(sc, logs.Options{Since: now.AddDate(0, 0, -31)})
		cancel()
	}
	fe := &fixer.Env{Home: home, ProjectDir: in.Cwd, ClaudeDir: src.ConfigDir(), DataDir: dir, Getenv: getenv, Now: now,
		DB: db.DB, Pricing: tbl, Cfg: cfg.Fixer}
	_, _ = autopilot.AdvanceSettings(ctx, dir, autopilot.SettingsDeps{DB: db, Fixer: fe, Pricing: tbl,
		Fit: limitfit.ReadCache(dir), SessionID: in.SessionID}, now)
}
