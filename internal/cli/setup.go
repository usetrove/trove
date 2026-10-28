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
	Verify  []string        `json:"verify,omitempty"`
	TurnOff []string        `json:"turn_off,omitempty"`
}

func newSetup(a *App) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "setup",
		Short: "Point your coding agents at the Trove gateway (shows the change first; asks before applying)",
		Long: "Detects Claude Code and Codex CLI, shows the exact change needed to route each through the local gateway, and applies it\n" +
			"only with your consent. The original file is backed up and `trove undo` reverses everything.\n" +
			"Shell rc files are never touched. Trove never reads or writes your API keys.",
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error { return runSetup(a, yes) },
	}
	c.Flags().BoolVar(&yes, "yes", false, "apply the changes shown without asking")
	return c
}

func runSetup(a *App, yes bool) error {
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
			e, perr = setup.PlanClaudeCode(cfgDir, cfg.Gateway.Port)
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
		return a.emit(rep, "Nothing to change: "+problemSummary(rep.Plans)+"\n\n"+strings.Join(rep.Verify, "\n"))
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
	var b strings.Builder
	fmt.Fprintf(&b, "Applied %d change(s). Backups are in %s.\n\n", len(applied), filepath.Join(dir, "backups"))
	b.WriteString(strings.Join(rep.Verify, "\n") + "\n\n" + strings.Join(rep.TurnOff, "\n"))
	return a.emit(rep, b.String())
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
