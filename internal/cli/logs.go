package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/logs"
	"github.com/usetrove/trove/internal/logs/cchistory"
	"github.com/usetrove/trove/internal/logs/cxhistory"
	"github.com/usetrove/trove/internal/measure"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/store"
)

// historyDays is how far back a scan looks. Files not written to since are skipped.
const historyDays = 30

func claudeSource(a *App) *cchistory.Source {
	return &cchistory.Source{Getenv: a.Getenv, Home: a.HomeDir}
}

func codexSource(a *App) *cxhistory.Source {
	return &cxhistory.Source{Getenv: a.Getenv, Home: a.HomeDir}
}

// detectPlan estimates how the user pays for Claude Code from settings and
// environment names. It never reads credentials.
func detectPlan(a *App, cfg config.Config) logs.PlanResult {
	return logs.DetectClaudeCodePlan(a.Getenv, claudeSource(a).ConfigDir(), cfg.History.Plan)
}

// ScanResult is what a history scan reports.
type ScanResult struct {
	Scan     logs.Summary    `json:"scan"`
	Plan     logs.PlanResult `json:"plan"` // Claude Code
	Codex    logs.PlanResult `json:"codex_plan"`
	FirstRun bool            `json:"first_run"`
	Skipped  bool            `json:"skipped,omitempty"` // no history found; nothing was created
}

// scanHistory reads new agent history into the database. It creates the
// database only if there is history to store. all=true ignores the 30-day limit.
func scanHistory(ctx context.Context, a *App, dir string, cfg config.Config, all, announce bool) (ScanResult, error) {
	res := ScanResult{Plan: detectPlan(a, cfg), Codex: logs.DetectCodexPlan(a.Getenv, cfg.History.Plan)}
	src, cdx := claudeSource(a), codexSource(a)
	files, err := src.Discover(ctx, nil)
	if more, cerr := cdx.Discover(ctx, nil); cerr == nil {
		files = append(files, more...)
	}
	if err != nil {
		return res, err
	}
	db, err := openExisting(dir)
	if err != nil {
		return res, err
	}
	if len(files) == 0 && db == nil {
		res.Skipped = true
		return res, nil
	}
	if db == nil {
		if db, err = store.Open(dir); err != nil {
			return res, fmt.Errorf("cannot open Trove's database: %v. Run `trove doctor` for details", err)
		}
	}
	defer func() { _ = db.Close() }()

	var n int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_files`).Scan(&n)
	res.FirstRun = n == 0
	if res.FirstRun && announce && !a.JSON && len(files) > 0 {
		_, _ = fmt.Fprintf(a.Err, "Reading your agent history from the last %d days (first run only). Trove only reads it and stores counts, never your prompts or code.\n", historyDays)
	}
	tbl, perr := pricing.Default()
	if perr != nil {
		tbl = nil
	}
	in := &logs.Ingester{
		DB:       db,
		Sources:  []logs.Source{src, cdx},
		Versions: logs.ParserVersions{logs.SourceClaudeCode: cchistory.ParserVersion, logs.SourceCodex: cxhistory.ParserVersion},
		Pricing:  tbl,
		Plan: func(source string) logs.Plan {
			if source == logs.SourceCodex {
				return res.Codex.Plan
			}
			return res.Plan.Plan
		},
		Annotate: logs.NewAuditor(tbl).Annotate,
	}
	var since time.Time
	if !all {
		since = time.Now().AddDate(0, 0, -historyDays)
	}
	res.Scan, err = in.Scan(ctx, logs.Options{Since: since})
	return res, err
}

func newLogs(a *App) *cobra.Command {
	c := &cobra.Command{
		Use:   "logs",
		Short: "Read your agents' local session history",
		Long: "Reads the session history Claude Code already keeps on disk, so Trove can report\n" +
			"without the gateway. It is read-only and stores only counts, sizes and hashes.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	var all bool
	scan := &cobra.Command{
		Use:   "scan",
		Short: "Read new history now",
		Long:  fmt.Sprintf("Reads history written since the last scan. By default only files changed in the last %d days are read; use --all for everything.", historyDays),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, cfg, err := loadConfig(a)
			if err != nil {
				return err
			}
			res, err := scanHistory(cmd.Context(), a, dir, cfg, all, true)
			if err != nil {
				return fmt.Errorf("cannot read agent history: %v. Run `trove logs status` to see what Trove found", err)
			}
			return a.emit(res, scanText(res))
		},
	}
	scan.Flags().BoolVar(&all, "all", false, "read all history, not just the last 30 days")
	status := &cobra.Command{
		Use:   "status",
		Short: "Show what history Trove has read",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return runLogsStatus(cmd.Context(), a) },
	}
	c.AddCommand(scan, status)
	return c
}

func scanText(r ScanResult) string {
	if r.Skipped {
		return "No agent history found. Run Claude Code once, then try again. Trove looks in ~/.claude/projects (or CLAUDE_CONFIG_DIR)."
	}
	s := r.Scan
	var b strings.Builder
	fmt.Fprintf(&b, "Read %d history files (%d with new data, %d unchanged): %d requests in %.1fs.\n", s.Files, s.Read, s.Unchanged, s.Events, float64(s.DurationMS)/1000)
	if s.SkippedLines > 0 {
		fmt.Fprintf(&b, "%d log lines could not be read and were skipped.\n", s.SkippedLines)
	}
	if s.UnknownFormat > 0 {
		fmt.Fprintf(&b, "%d files come from a Claude Code version Trove was not written for; they were read best-effort.\n", s.UnknownFormat)
	}
	if s.Errors > 0 {
		fmt.Fprintf(&b, "%d files could not be opened.\n", s.Errors)
	}
	fmt.Fprintf(&b, "Plan (estimated from local settings): %s.", r.Plan.Plan)
	return b.String()
}

type logsStatus struct {
	Files        int               `json:"files"`
	Requests     int               `json:"requests"`
	SkippedLines int64             `json:"skipped_lines"`
	LastScan     string            `json:"last_scan,omitempty"`
	Plan         logs.PlanResult   `json:"plan"`
	Detectors    []string          `json:"detectors_run"`
	NotAvailable map[string]string `json:"detectors_not_available"`
}

func runLogsStatus(ctx context.Context, a *App) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	st := logsStatus{Plan: detectPlan(a, cfg), Detectors: logs.AuditDetectors, NotAvailable: logs.SkippedDetectors}
	db, err := openExisting(dir)
	if err != nil {
		return err
	}
	if db != nil {
		defer func() { _ = db.Close() }()
		fs, err := measure.HistoryFileStats(ctx, db)
		if err != nil {
			return err
		}
		st.Files, st.SkippedLines = fs.Files, fs.SkippedLines
		if fs.Latest > 0 {
			st.LastScan = time.UnixMilli(fs.Latest).Format(time.DateTime)
		}
		_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM history_requests`).Scan(&st.Requests)
	}
	var b strings.Builder
	if st.Files == 0 {
		b.WriteString("No history read yet. Run `trove logs scan`, or just `trove`.\n")
	} else {
		fmt.Fprintf(&b, "%d history files, %d requests, last scan %s.\n", st.Files, st.Requests, st.LastScan)
	}
	if st.SkippedLines > 0 {
		fmt.Fprintf(&b, "%d log lines could not be read and were skipped.\n", st.SkippedLines)
	}
	ev := "none"
	if len(st.Plan.Evidence) > 0 {
		ev = strings.Join(st.Plan.Evidence, ", ")
	}
	fmt.Fprintf(&b, "Plan (estimated from local settings, never from credentials): %s. Evidence: %s.\n", st.Plan.Plan, ev)
	fmt.Fprintf(&b, "Waste checks run on history: %s.\n", strings.Join(st.Detectors, ", "))
	b.WriteString("Not available from history:")
	keys := make([]string, 0, len(st.NotAvailable))
	for k := range st.NotAvailable {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "\n  %s: %s", k, st.NotAvailable[k])
	}
	return a.emit(st, b.String())
}
