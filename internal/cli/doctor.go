package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/usetrove/trove/internal/optimize"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/pricing"
	"github.com/usetrove/trove/internal/redact"
	"github.com/usetrove/trove/internal/store"
)

// Check statuses.
const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
	statusInfo = "info"
)

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

type doctorReport struct {
	OK     bool    `json:"ok"`
	Checks []check `json:"checks"`
}

// agentSpec describes how to detect a supported agent. Detection only: Trove
// never changes an agent's files here.
type agentSpec struct {
	name      string
	binary    string
	configDir func(a *App, home string) string
}

// Verified against official docs on 2026-10-28:
//
//	Claude Code: executable `claude`; user settings in ~/.claude (overridable with CLAUDE_CONFIG_DIR)
//	  https://code.claude.com/docs/en/settings
//	Codex CLI: executable `codex`; user config at ~/.codex/config.toml
//	  https://learn.chatgpt.com/docs/config-file/config-basic
var supportedAgents = []agentSpec{
	{"Claude Code", "claude", func(a *App, home string) string {
		if d := a.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
			return d
		}
		return filepath.Join(home, ".claude")
	}},
	{"Codex CLI", "codex", func(_ *App, home string) string { return filepath.Join(home, ".codex") }},
}

func newDoctor(a *App) *cobra.Command {
	var capabilities bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check Trove's data directory, database, config and detected agents",
		Long: "Check Trove's data directory, database, config and detected agents.\n\n" +
			"With --capabilities, show instead which optimizers each adapter (gateway, hooks, plugin) can run, and why.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if capabilities {
				rows := optimize.Matrix()
				return a.emit(struct {
					VerifiedOn string         `json:"harness_facts_verified_on"`
					Optimizers []optimize.Row `json:"optimizers"`
				}{optimize.VerifiedOn, rows}, "Optimizer support by adapter (harness facts checked "+optimize.VerifiedOn+"):\n\n"+optimize.RenderText(rows))
			}
			rep := runDoctor(a)
			if err := a.emit(rep, rep.text(a)); err != nil {
				return err
			}
			if !rep.OK {
				return errors.New("doctor found problems; see the fixes listed above")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&capabilities, "capabilities", false, "show which optimizers each adapter can run")
	return cmd
}

func runDoctor(a *App) doctorReport {
	var cs []check
	dir, err := a.ResolveDataDir()
	if err != nil {
		cs = append(cs, check{"data directory", statusFail, err.Error(), "pass --data-dir or set TROVE_DATA_DIR"})
		return finish(cs)
	}
	cs = append(cs, checkDataDir(dir)...)
	cs = append(cs, checkDatabase(dir))
	cs = append(cs, checkConfig(a, dir))
	cs = append(cs, checkPricing(time.Now()))
	cs = append(cs, checkAgents(a)...)
	return finish(cs)
}

// checkPricing warns when the built-in prices were last verified long ago, so
// dollar estimates may be out of date. It never fails: old prices still work.
func checkPricing(now time.Time) check {
	const name = "pricing data"
	tbl, err := pricing.Default()
	if err != nil {
		return check{name, statusFail, "the built-in price list could not be read: " + err.Error(), "reinstall Trove"}
	}
	stale := tbl.Stale(now, pricing.MaxAge)
	if len(stale) == 0 {
		return check{name, statusOK, "version " + pricing.DataVersion + ", all prices verified within 30 days", ""}
	}
	return check{name, statusWarn,
		fmt.Sprintf("version %s: %d price(s) were last verified more than 30 days ago, so dollar figures may be out of date", pricing.DataVersion, len(stale)),
		"upgrade Trove to get newer prices; run `trove pricing show` to see each price's verified date"}
}

func finish(cs []check) doctorReport {
	ok := true
	for i, c := range cs {
		if c.Status == statusFail {
			ok = false
		}
		// Error text can echo user-supplied values (for example a bad TROVE_ variable).
		cs[i].Detail, cs[i].Fix = redact.String(c.Detail), redact.String(c.Fix)
	}
	return doctorReport{OK: ok, Checks: cs}
}

func checkDataDir(dir string) []check {
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return []check{{"data directory", statusWarn, dir + " does not exist yet", "it is created automatically the first time Trove stores data (for example `trove setup`)"}}
	case err != nil:
		return []check{{"data directory", statusFail, fmt.Sprintf("cannot read %s: %v", dir, err), "check the folder's permissions"}}
	case !fi.IsDir():
		return []check{{"data directory", statusFail, dir + " is a file, not a folder", "move it aside or choose another --data-dir"}}
	}
	if probs := store.PermissionProblems(dir); len(probs) > 0 {
		return []check{{"data directory", statusFail, "permissions are too open: " + strings.Join(probs, "; "), "run the chmod command shown; Trove data should be readable only by you"}}
	}
	return []check{{"data directory", statusOK, dir + " (private)", ""}}
}

func checkDatabase(dir string) check {
	const name = "database"
	path := filepath.Join(dir, store.DBFileName)
	if _, err := os.Stat(path); err != nil {
		return check{name, statusWarn, "no database yet at " + path, "it is created on first use"}
	}
	db, err := store.OpenNoMigrate(dir)
	if err != nil {
		return check{name, statusFail, err.Error(), ""}
	}
	defer func() { _ = db.Close() }()
	ctx := context.Background()
	cur, err := db.CurrentVersion(ctx)
	if err != nil {
		return check{name, statusFail, "cannot read schema version: " + err.Error(), "if the file is damaged, move it aside; Trove will create a new one"}
	}
	latest, err := store.LatestVersion()
	if err != nil {
		return check{name, statusFail, err.Error(), ""}
	}
	switch {
	case cur == latest:
		return check{name, statusOK, fmt.Sprintf("opens; schema version %d is current", cur), ""}
	case cur < latest:
		return check{name, statusWarn, fmt.Sprintf("schema version %d is behind %d", cur, latest), "migrations apply automatically the next time Trove starts"}
	default:
		return check{name, statusFail, fmt.Sprintf("schema version %d is newer than this Trove (%d)", cur, latest), "upgrade Trove, or use a different --data-dir"}
	}
}

func checkConfig(a *App, dir string) check {
	path := filepath.Join(dir, config.FileName)
	if _, err := config.Load(dir, a.Getenv); err != nil {
		return check{"config", statusFail, err.Error(), "edit " + path + " or the TROVE_ variables named above"}
	}
	if _, err := os.Stat(path); err != nil {
		return check{"config", statusOK, "valid (no config file; using defaults)", ""}
	}
	return check{"config", statusOK, "valid: " + path, ""}
}

func checkAgents(a *App) []check {
	home, herr := a.HomeDir()
	var out []check
	for _, s := range supportedAgents {
		var found []string
		if _, err := a.LookPath(s.binary); err == nil {
			found = append(found, "`"+s.binary+"` on PATH")
		}
		if herr == nil {
			if d := s.configDir(a, home); dirExists(d) {
				found = append(found, "settings folder present")
			}
		}
		name := "agent: " + s.name
		if len(found) == 0 {
			out = append(out, check{name, statusInfo, "not detected", ""})
		} else {
			out = append(out, check{name, statusInfo, "detected (" + strings.Join(found, ", ") + ")", ""})
		}
	}
	return out
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func (r doctorReport) text(a *App) string {
	var b strings.Builder
	for _, c := range r.Checks {
		label := map[string]string{
			statusOK:   a.paint("32", "ok  "),
			statusWarn: a.paint("33", "warn"),
			statusFail: a.paint("31", "FAIL"),
			statusInfo: "info",
		}[c.Status]
		fmt.Fprintf(&b, "[%s] %s: %s\n", label, c.Name, c.Detail)
		if c.Fix != "" && c.Status != statusOK {
			fmt.Fprintf(&b, "       what to do: %s\n", c.Fix)
		}
	}
	if r.OK {
		b.WriteString("\nNo problems found.")
	} else {
		b.WriteString("\nProblems found.")
	}
	return b.String()
}
