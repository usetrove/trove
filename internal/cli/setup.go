package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/setup"
)

type setupPlanJSON struct {
	Agent       string `json:"agent"`
	Path        string `json:"path,omitempty"`
	Summary     string `json:"summary,omitempty"`
	Adds        string `json:"adds,omitempty"`
	Note        string `json:"note,omitempty"`
	AlreadyDone bool   `json:"already_configured,omitempty"`
	Problem     string `json:"problem,omitempty"`
}

type setupReport struct {
	Plans   []setupPlanJSON `json:"plans"`
	Applied bool            `json:"applied"`
	Changes []setup.Change  `json:"changes,omitempty"`
	// StatusLine is the optional Claude Code status-line step; nil when Claude
	// Code is not set up.
	StatusLine        *setupPlanJSON `json:"status_line,omitempty"`
	StatusLineApplied bool           `json:"status_line_applied,omitempty"`
	Verify            []string       `json:"verify,omitempty"`
	TurnOff           []string       `json:"turn_off,omitempty"`
	// Explain says what Trove records, that nothing leaves the machine, that it
	// starts observe-only, and how to see results.
	Explain []string `json:"explain,omitempty"`
}

func newSetup(a *App) *cobra.Command {
	var yes, statusLine bool
	c := &cobra.Command{
		Use:   "setup",
		Short: "Point your coding agents at the Trove gateway (shows the change first; asks before applying)",
		Long: "Detects Claude Code and Codex CLI, shows the exact change needed to route each through the local gateway, and applies it\n" +
			"only with your consent. The original file is backed up and `trove undo` reverses everything.\n" +
			"Shell rc files are never touched. Trove never reads or writes your API keys.\n" +
			"For Claude Code it then offers, as a separate step, to show your spend and budget pace in the status line.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runSetup(a, yes, statusLine) },
	}
	c.Flags().BoolVar(&yes, "yes", false, "apply the changes shown without asking")
	c.Flags().BoolVar(&statusLine, "status-line", false, "also add Trove to Claude Code's status line without asking (--yes alone does not)")
	return c
}

func runSetup(a *App, yes, statusLine bool) error {
	dir, cfg, err := loadConfig(a)
	if err != nil {
		return err
	}
	home, err := a.HomeDir()
	if err != nil {
		return errors.New("cannot find your home directory; set HOME and try again")
	}
	var edits []*setup.Edit
	rep := setupReport{}
	claudeDir := "" // set when Claude Code is installed and its settings can be edited
	for _, spec := range supportedAgents {
		cfgDir := spec.configDir(a, home)
		_, onPath := a.LookPath(spec.binary)
		_, statErr := os.Stat(cfgDir)
		if onPath != nil && statErr != nil {
			continue // not installed
		}
		var e *setup.Edit
		var perr error
		if spec.binary == "codex" {
			e, perr = setup.PlanCodex(cfgDir, cfg.Gateway.Port)
		} else {
			e, perr = setup.PlanClaudeCode(cfgDir, cfg.Gateway.Port, cfg.Upstreams.Anthropic)
			if perr == nil {
				claudeDir = cfgDir
			}
		}
		switch {
		case perr != nil:
			rep.Plans = append(rep.Plans, setupPlanJSON{Agent: spec.name, Problem: perr.Error()})
		default:
			rep.Plans = append(rep.Plans, setupPlanJSON{Agent: spec.name, Path: e.Path, Summary: e.Summary, Adds: e.Show, Note: e.Note, AlreadyDone: e.NoOp})
			edits = append(edits, e)
		}
	}
	if len(rep.Plans) == 0 {
		return a.emit(rep, "No supported agents found (looked for Claude Code and Codex CLI). Install one, or point any API client at\n  Anthropic: "+
			setup.GatewayURL(cfg.Gateway.Port, "/anthropic")+"\n  OpenAI:    "+setup.GatewayURL(cfg.Gateway.Port, "/openai/v1")+"\nwith an API key.")
	}

	pending := 0
	for _, e := range edits {
		if !e.NoOp {
			pending++
		}
	}
	if !a.JSON {
		a.printPlans(rep.Plans)
	}
	if pending == 0 {
		rep.Verify, rep.TurnOff = verifySteps(cfg.Gateway.Port), turnOffSteps()
		rep.Explain = explainSteps(dir, cfg)
		statusText, err := statusLineStep(a, &rep, dir, claudeDir, statusLine)
		if err != nil {
			return err
		}
		return a.emit(rep, "Nothing to change: "+problemSummary(rep.Plans)+"\n\n"+statusText+strings.Join(rep.Verify, "\n")+"\n\n"+strings.Join(rep.Explain, "\n"))
	}
	consent := yes
	if !consent && !a.JSON {
		consent = a.confirm(fmt.Sprintf("Apply %d change(s)? Originals are backed up and `trove undo` reverses them.", pending))
	}
	if !consent {
		return a.emit(rep, "No changes made. Re-run with --yes (or answer y) to apply.")
	}

	if err := ensureConfigFile(dir); err != nil {
		return err
	}
	applied, err := setup.Apply(dir, edits, time.Now())
	rep.Applied, rep.Changes = len(applied) > 0, applied
	if err != nil {
		_ = a.emit(rep, "")
		return fmt.Errorf("%v. Run `trove undo` to reverse anything already applied", err)
	}
	rep.Verify, rep.TurnOff = verifySteps(cfg.Gateway.Port), turnOffSteps()
	rep.Explain = explainSteps(dir, cfg)
	statusText, err := statusLineStep(a, &rep, dir, claudeDir, statusLine)
	if err != nil {
		_ = a.emit(rep, "")
		return fmt.Errorf("%v. Run `trove undo` to reverse anything already applied", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Applied %d change(s). Backups are in %s.\n\n", len(rep.Changes), filepath.Join(dir, "backups"))
	b.WriteString(statusText)
	b.WriteString(strings.Join(rep.Verify, "\n") + "\n\n" + strings.Join(rep.TurnOff, "\n") + "\n\n" + strings.Join(rep.Explain, "\n"))
	return a.emit(rep, b.String())
}

// explainSteps is the closing note of setup: what is recorded, where it stays,
// that Trove starts by only watching, and how to see results.
func explainSteps(dir string, cfg config.Config) []string {
	return []string{
		"What happens next:",
		"  Trove records metadata for each request that goes through the gateway: token counts, sizes, hashes, model names and timings.",
		"  It never stores your prompts, code, file paths, tool output or API keys.",
		"  Everything stays in " + dir + " on this machine. Trove sends nothing anywhere; the only network traffic is your own requests going on to your provider.",
		fmt.Sprintf("  Trove starts in observe-only mode. No optimizer is on and no request is changed. It estimates what each optimizer would save, and offers one only after a baseline of %d days and %d sessions.",
			cfg.Measure.BaselineDays, cfg.Measure.BaselineSessions),
		"  To see results, run `trove` (or `trove report`). Until you have made a few requests through the gateway it will say there is nothing to show yet.",
	}
}

// statusLineCommand is what Claude Code will run for its status line. It uses the
// absolute path of this binary so it works without trove on PATH, and passes the
// data directory only if the user chose a non-default one.
func statusLineCommand(a *App, dir string) string {
	bin := "trove"
	if a.Executable != nil {
		if p, err := a.Executable(); err == nil && p != "" {
			bin = p
		}
	}
	quote := func(s string) string {
		if strings.ContainsAny(s, " \t") {
			return `"` + s + `"`
		}
		return s
	}
	cmd := quote(bin) + " status-line --format claude"
	if a.DataDir != "" || a.Getenv("TROVE_DATA_DIR") != "" {
		cmd += " --data-dir " + quote(dir)
	}
	return cmd
}

// statusLineStep offers to add Trove to Claude Code's status line. It runs after
// the gateway change has been applied, plans from the file as it now is, and
// applies only with its own consent: the --status-line flag, or a yes at the
// prompt. --yes alone does not agree to it. It returns text for the summary.
func statusLineStep(a *App, rep *setupReport, dir, claudeDir string, flag bool) (string, error) {
	if claudeDir == "" {
		return "", nil
	}
	e, err := setup.PlanStatusLine(claudeDir, statusLineCommand(a, dir))
	var exists *setup.StatusLineExistsError
	switch {
	case errors.As(err, &exists):
		note := "You already have a Claude Code status line, so Trove left it alone. To add Trove's, call `trove status-line --format claude` from your own script (it reads Claude Code's JSON on stdin)."
		rep.StatusLine = &setupPlanJSON{Agent: "Claude Code status line", Path: exists.Path, Note: note}
		return note + "\n\n", nil
	case err != nil:
		rep.StatusLine = &setupPlanJSON{Agent: "Claude Code status line", Problem: err.Error()}
		return "Status line not added: " + err.Error() + "\n\n", nil
	}
	rep.StatusLine = &setupPlanJSON{Agent: "Claude Code status line", Path: e.Path, Summary: e.Summary, Adds: e.Show, Note: e.Note, AlreadyDone: e.NoOp}
	if e.NoOp {
		return "Claude Code's status line already shows Trove.\n\n", nil
	}
	if !a.JSON {
		a.printPlans([]setupPlanJSON{*rep.StatusLine})
	}
	consent := flag
	if !consent && !a.JSON {
		consent = a.confirm("Add Trove to Claude Code's status line? Your settings file is backed up first and `trove undo` removes it.")
	}
	if !consent {
		return "Status line not added. Add it later with `trove setup --status-line`.\n\n", nil
	}
	if err := ensureConfigFile(dir); err != nil {
		return "", err
	}
	applied, err := setup.Apply(dir, []*setup.Edit{e}, time.Now())
	rep.Changes = append(rep.Changes, applied...)
	if err != nil {
		return "", err
	}
	rep.StatusLineApplied = len(applied) > 0
	return "Added Trove to Claude Code's status line. It appears after your next reply.\n\n", nil
}

func problemSummary(ps []setupPlanJSON) string {
	var parts []string
	for _, p := range ps {
		switch {
		case p.Problem != "":
			parts = append(parts, p.Agent+": "+p.Problem)
		case p.AlreadyDone:
			parts = append(parts, p.Agent+" is already pointed at the gateway")
		}
	}
	return strings.Join(parts, "; ") + "."
}

func (a *App) printPlans(ps []setupPlanJSON) {
	for _, p := range ps {
		_, _ = fmt.Fprintf(a.Out, "%s\n", a.paint("1", p.Agent))
		switch {
		case p.Problem != "":
			_, _ = fmt.Fprintf(a.Out, "  cannot set up: %s\n\n", p.Problem)
		case p.AlreadyDone:
			_, _ = fmt.Fprintf(a.Out, "  already pointed at the gateway (%s)\n\n", p.Path)
		default:
			_, _ = fmt.Fprintf(a.Out, "  %s\n  file: %s\n  adds:\n", p.Summary, p.Path)
			for _, l := range strings.Split(strings.TrimRight(p.Adds, "\n"), "\n") {
				_, _ = fmt.Fprintf(a.Out, "      %s\n", l)
			}
			_, _ = fmt.Fprintf(a.Out, "  note: %s\n\n", p.Note)
		}
	}
}

func ensureConfigFile(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create %s: %v", dir, err)
	}
	p := filepath.Join(dir, config.FileName)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.WriteFile(p, []byte(config.DefaultTOML), 0o600); err != nil {
		return fmt.Errorf("cannot write %s: %v", p, err)
	}
	return nil
}

func verifySteps(port int) []string {
	return []string{
		"To verify it works:",
		"  1. Start the gateway:   trove gateway start        (leave it running, or use --background)",
		fmt.Sprintf("  2. Check it responds:   curl http://127.0.0.1:%d/trove/health", port),
		"  3. Claude Code: with ANTHROPIC_API_KEY set, run `claude`, then /status: it should show",
		fmt.Sprintf("     \"Anthropic base URL: http://127.0.0.1:%d/anthropic\". Send a message.", port),
		"     Codex CLI: with OPENAI_API_KEY set, run `codex` and send a message.",
		"  4. Run `trove gateway status`: the request count should have gone up.",
	}
}

func turnOffSteps() []string {
	return []string{
		"To turn it off:",
		"  trove undo             restores the files Trove changed, exactly as they were",
		"  trove gateway stop     stops the gateway (agents pointed at it will fail until you undo)",
	}
}

func newUndo(a *App) *cobra.Command {
	return &cobra.Command{
		Use:   "undo",
		Short: "Reverse everything `trove setup` changed",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			dir, err := a.ResolveDataDir()
			if err != nil {
				return err
			}
			res, err := setup.Undo(dir)
			if err != nil {
				return err
			}
			if len(res) == 0 {
				return a.emit(map[string]any{"undone": []setup.UndoResult{}}, "Nothing to undo: Trove has not changed any agent settings.")
			}
			var b strings.Builder
			for _, r := range res {
				fmt.Fprintf(&b, "%s: %s (%s)\n", r.Agent, r.Detail, r.Path)
			}
			b.WriteString("Your agents talk to their providers directly again. Stop the gateway with `trove gateway stop` if it is running.")
			return a.emit(map[string]any{"undone": res}, b.String())
		},
	}
}
